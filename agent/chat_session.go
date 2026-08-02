package agent

import (
	"context"
	"log"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// queuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
type queuedMessage struct {
	id   uint
	msg  *chat.RevMessage
	opts []Option // 本次消息附带的per-turn选项覆盖
}

// chatSession 完整会话实体，管理消息队列、对话历史和 LLM 调用
type chatSession struct {
	id            string
	mu            sync.Mutex
	registry      *chat.ProviderRegistry
	inbox         *util.SliceQueueSafe[*queuedMessage]
	events        *chat.Store
	running       bool
	chatClients   *util.SliceArray[*ChatClient]
	toolExecutors map[string]ToolExecutor
	system        string
	opts          *Options
	cancel        context.CancelFunc
	seq           uint
}

func newChatSession(id string, registry *chat.ProviderRegistry, toolExecutors map[string]ToolExecutor, system string, opts *Options, historyStore chat.HistoryStore) *chatSession {
	s := &chatSession{
		id:            id,
		registry:      registry,
		inbox:         util.NewSliceQueueSafe[*queuedMessage](),
		events:        chat.NewStore(id, historyStore),
		running:       false,
		chatClients:   new(util.SliceArray[*ChatClient]),
		toolExecutors: toolExecutors,
		system:        system,
		opts:          opts,
	}
	return s
}
func (s *chatSession) History() []*chat.Message {
	return s.events.History()
}
func (s *chatSession) getSeq() uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}
func (s *chatSession) newClient(start uint) *ChatClient {
	chatClient := &ChatClient{
		queue:   util.NewQueue[bool](),
		handler: s,
		start:   start,
		offset:  start, // 一次性初始偏移，之后随读取递增
	}
	s.mu.Lock()
	s.chatClients.Append(chatClient)
	s.mu.Unlock()
	return chatClient
}
func (s *chatSession) LoadHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events.LoadHistory()
}

func (s *chatSession) DeleteClient(client *ChatClient) {
	s.mu.Lock()
	s.chatClients.Remove(client)
	client.queue.Close()
	s.mu.Unlock()
}

func (s *chatSession) SendMessage(message *chat.RevMessage, opt ...Option) error {
	qm := &queuedMessage{
		id:   s.getSeq(),
		msg:  message,
		opts: opt,
	}
	err := s.inbox.Write(qm)
	if err != nil {
		return err
	}
	s.mu.Lock()
	started := false
	if !s.running {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.running = true
		started = true
		util.GoWithRecover(func() {
			s.run(ctx)
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := chat.NewErrorEvent("internal error")
			evt.Done = true
			s.addEvent(evt)
		})
	}
	s.mu.Unlock()

	// 在锁外发送事件，避免 addEvent -> flush -> s.mu.Lock 死锁
	if started {
		// 消息可以立马发出，通知发送者将消息显示在对话列表
		s.addEvent(chat.NewMessageSentEvent(qm.id, s.id, message))
	} else {
		// 消息没有立马发出，通知发送者将消息标记为队列待处理
		s.addEvent(chat.NewMessageQueuedEvent(qm.id, s.id, message))
	}
	return nil
}

// build 从队列中取出所有待处理消息，追加到历史记录，构建 LLM 请求
func (s *chatSession) build() *chat.Request {
	var turnOpts []Option
	for {
		qm, err := s.inbox.Read()
		if err != nil {
			break
		}
		// 用户消息入历史（带事件流区间）
		start := s.events.Position()
		msg := qm.msg.ToMessage()
		msg.Start = start
		s.events.AppendHistory(&msg)
		// 队列消息已使用，通知发送者将对应消息显示在对话框
		s.addEvent(chat.NewMessageConsumedEvent(qm.id, s.id, qm.msg))
		s.events.SetLastHistoryOffset(s.events.Position() - start)
		if len(qm.opts) > 0 {
			turnOpts = qm.opts
		}
	}

	// 注入历史上下文
	history := s.events.History()
	if len(history) == 0 {
		return nil
	}

	effective := s.opts
	if len(turnOpts) > 0 {
		merged := *s.opts
		for _, o := range turnOpts {
			o(&merged)
		}
		effective = &merged
	}

	messages := &chat.Request{
		System:   s.system,
		Messages: make([]chat.Message, 0, len(history)),
	}
	for _, m := range history {
		msg := *m
		msg.Content = withoutThinking(m.Content)
		// 剥离后内容为空的消息不发送（避免空 content 报错）
		if len(msg.Content) == 0 {
			continue
		}
		messages.Messages = append(messages.Messages, msg)
	}

	if effective != nil {
		messages.Model = effective.Model
		messages.MaxTokens = effective.MaxTokens
		messages.Temperature = effective.Temperature
		messages.TopP = effective.TopP
		messages.TopK = effective.TopK
		messages.StopSequences = effective.StopSequences
		messages.Stream = effective.Stream
		messages.Thinking = effective.Thinking.toThinkingConfig()
	} else {
		messages.Stream = true
	}

	if len(s.toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(s.toolExecutors))
		for _, exec := range s.toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}

func (s *chatSession) addEvent(event *chat.ClientEvent) {
	s.events.Add(event)
	s.flush()
}

// withoutThinking 从 blocks 中剩离 thinking block。
// 发送给 LLM 的历史不需要思考链：Anthropic 要求历史中的 thinking block 必须携带
// signature 字段原样传回，否则报 400；且空 thinking 会因 omitempty 序列化为
// {"type":"thinking"} 缺少 thinking 字段。思考链仍保留在 history/DB 中供展示，
// 仅不回传给模型。
func withoutThinking(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
		if _, ok := b.(*chat.ThinkingBlock); ok {
			continue
		}
		result = append(result, b)
	}
	return result
}

// flush 通知所有客户端有新事件
func (s *chatSession) flush() {
	for _, sub := range s.chatClients.Slice() {
		err := sub.queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat session: %v", err)
		}
	}
}

func (s *chatSession) ReadEvent(start uint) *chat.EventEntry {
	return s.events.ReadFrom(start)
}
