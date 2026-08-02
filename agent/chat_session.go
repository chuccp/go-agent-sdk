package agent

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// queuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
type queuedMessage struct {
	id   uint64
	msg  *chat.RevMessage
	opts []Option // 本次消息附带的per-turn选项覆盖
}

// chatSession 完整会话实体，管理消息队列、对话历史和 LLM 调用
type chatSession struct {
	id            string
	clientMutex   sync.Mutex // 保护 chatClients
	runMutex      sync.Mutex // 保护 inbox / running / cancel
	registry      *chat.ProviderRegistry
	inbox         []*queuedMessage // 用户输入消息队列（runMutex 保护）
	events        *chat.Store
	running       bool
	chatClients   *util.SliceArray[*ChatClient]
	toolExecutors map[string]ToolExecutor
	system        string
	opts          *Options
	cancel        context.CancelFunc
	seq           uint64
}

func newChatSession(id string, registry *chat.ProviderRegistry, toolExecutors map[string]ToolExecutor, system string, opts *Options, historyStore chat.HistoryStore) *chatSession {
	s := &chatSession{
		id:            id,
		registry:      registry,
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
func (s *chatSession) getSeq() uint64 {
	return atomic.AddUint64(&s.seq, 1)
}
func (s *chatSession) newClient(start uint) *ChatClient {
	chatClient := &ChatClient{
		queue:   util.NewQueue[bool](),
		handler: s,
		start:   start,
		offset:  start, // 一次性初始偏移，之后随读取递增
	}
	s.clientMutex.Lock()
	s.chatClients.Append(chatClient)
	s.clientMutex.Unlock()
	return chatClient
}
func (s *chatSession) LoadHistory() error {
	return s.events.LoadHistory()
}

func (s *chatSession) DeleteClient(client *ChatClient) {
	s.clientMutex.Lock()
	s.chatClients.Remove(client)
	client.queue.Close()
	s.clientMutex.Unlock()
}

func (s *chatSession) SendMessage(message *chat.RevMessage, opt ...Option) error {
	qm := &queuedMessage{
		id:   s.getSeq(),
		msg:  message,
		opts: opt,
	}
	s.runMutex.Lock()
	s.inbox = append(s.inbox, qm)
	if !s.running {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.running = true
		s.addEvent(chat.NewMessageSentEvent(qm.id, s.id, message))
		util.GoWithRecover(func() {
			s.run(ctx)
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := chat.NewErrorEvent("internal error")
			evt.Done = true
			s.addEvent(evt)
		})
	} else {
		s.addEvent(chat.NewMessageQueuedEvent(qm.id, s.id, message))
	}
	s.runMutex.Unlock()
	return nil
}

// build 从 inbox 中取出所有待处理消息，追加到历史记录，构建 LLM 请求。
// 调用方必须持有 runMutex。
func (s *chatSession) build() *chat.Request {
	var turnOpts []Option
	for _, qm := range s.inbox {
		s.consumeMessage(qm)
		if len(qm.opts) > 0 {
			turnOpts = qm.opts
		}
	}
	s.inbox = s.inbox[:0]

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

// consumeMessage 将一条用户消息追加到历史记录，并发出消费事件。
func (s *chatSession) consumeMessage(qm *queuedMessage) {
	start := s.events.Position()
	msg := qm.msg.ToMessage()
	msg.Start = start
	s.events.AppendHistory(&msg)
	s.addEvent(chat.NewMessageConsumedEvent(qm.id, s.id, qm.msg))
	s.events.SetLastHistoryOffset(s.events.Position() - start)
}

// drainInbox 排干 inbox 中所有剩余消息，将它们写入历史（不丢失）。
// 调用方必须持有 runMutex。
func (s *chatSession) drainInbox() {
	for _, qm := range s.inbox {
		s.consumeMessage(qm)
	}
	s.inbox = s.inbox[:0]
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
	s.clientMutex.Lock()
	clients := s.chatClients.Slice()
	s.clientMutex.Unlock()
	for _, sub := range clients {
		err := sub.queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat session: %v", err)
		}
	}
}

func (s *chatSession) ReadEvent(start uint) *chat.EventEntry {
	return s.events.ReadFrom(start)
}
