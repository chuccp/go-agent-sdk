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
	msg  *chat.Message
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
	//// 从持久化存储加载历史记录
	//if historyStore != nil {
	//	if msgs, err := historyStore.LoadHistory(id); err != nil {
	//		log.Printf("[chatSession] load history failed for %s: %v", id, err)
	//	} else if len(msgs) > 0 {
	//		s.history = msgs
	//		s.seq = uint(len(msgs))
	//	}
	//}
	return s
}
func (s *chatSession) getSeq() uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}
func (s *chatSession) newClient() *ChatClient {
	chatClient := &ChatClient{queue: util.NewQueue[bool](), handler: s}
	s.mu.Lock()
	s.chatClients.Append(chatClient)
	s.mu.Unlock()
	return chatClient
}

func (s *chatSession) DeleteClient(client *ChatClient) {
	s.mu.Lock()
	if removed, ok := s.chatClients.Remove(client); ok {
		removed.queue.Close()
	}
	s.mu.Unlock()
}

func (s *chatSession) SendMessage(message *chat.Message, opt ...Option) error {
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
		s.addEvent(chat.NewMessageSentEvent(qm.id, s.id))
	} else {
		// 消息没有立马发出，通知发送者将消息标记为队列待处理
		s.addEvent(chat.NewMessageQueuedEvent(qm.id, s.id))
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
		// 队列消息已使用，通知发送者将对应消息显示在对话框
		s.addEvent(chat.NewMessageConsumedEvent(qm.id, s.id))
		//s.history = append(s.history, *qm.msg)
		if len(qm.opts) > 0 {
			turnOpts = qm.opts
		}
	}

	//if len(s.history) == 0 {
	//	return nil
	//}

	// 合并会话级选项与 per-turn 选项（per-turn 优先）
	effective := s.opts
	if len(turnOpts) > 0 {
		merged := *s.opts
		for _, o := range turnOpts {
			o(&merged)
		}
		effective = &merged
	}

	messages := &chat.Request{
		System: s.system,
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

	// 截断上下文：仅保留最近的 N 条消息
	//history := s.history
	//if effective != nil && effective.MaxContext > 0 && len(history) > effective.MaxContext {
	//	history = history[len(history)-effective.MaxContext:]
	//}
	//messages.Messages = make([]chat.Message, len(history))
	//copy(messages.Messages, history)

	if len(s.toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(s.toolExecutors))
		for _, exec := range s.toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}

//// saveHistory 将当前历史保存到持久化存储
//func (s *chatSession) saveHistory() {
//	if s.historyStore == nil {
//		return
//	}
//	if err := s.historyStore.SaveHistory(s.id, s.history); err != nil {
//		log.Printf("[chatSession] save history failed for %s: %v", s.id, err)
//	}
//}

func (s *chatSession) addEvent(event *chat.ClientEvent) {
	s.events.Add(event)
	s.flush()
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
