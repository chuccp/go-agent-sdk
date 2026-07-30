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
	id  uint
	msg *chat.Message
}

// chatSession 完整会话实体，管理消息队列、对话历史和 LLM 调用
type chatSession struct {
	id            string
	mu            sync.Mutex
	registry      *chat.ProviderRegistry
	inbox         *util.SliceQueueSafe[*queuedMessage]
	events        *chat.EventStore
	history       []chat.Message
	running       bool
	subscribers   []*subscriber
	toolExecutors map[string]ToolExecutor
	system        string
	opts          *Options
	cancel        context.CancelFunc
	seq           uint
	historyStore  HistoryStore
}

func newChatSession(id string, registry *chat.ProviderRegistry, toolExecutors map[string]ToolExecutor, system string, opts *Options, historyStore HistoryStore) *chatSession {
	s := &chatSession{
		id:            id,
		registry:      registry,
		inbox:         util.NewSliceQueueSafe[*queuedMessage](),
		events:        chat.NewEventStore(),
		history:       make([]chat.Message, 0),
		running:       false,
		subscribers:   make([]*subscriber, 0),
		toolExecutors: toolExecutors,
		system:        system,
		opts:          opts,
		historyStore:  historyStore,
	}
	// 从持久化存储加载历史记录
	if historyStore != nil {
		if msgs, err := historyStore.LoadHistory(id); err != nil {
			log.Printf("[chatSession] load history failed for %s: %v", id, err)
		} else if len(msgs) > 0 {
			s.history = msgs
			s.seq = uint(len(msgs))
		}
	}
	return s
}
func (s *chatSession) getSeq() uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}
func (s *chatSession) newClient() *ChatClient {
	sub := &subscriber{queue: util.NewQueue[bool]()}
	s.mu.Lock()
	s.subscribers = append(s.subscribers, sub)
	s.mu.Unlock()
	return &ChatClient{
		handler: s,
		sub:     sub,
	}
}

func (s *chatSession) DeleteClient(client *ChatClient) {
	s.mu.Lock()
	for i, sub := range s.subscribers {
		if sub == client.sub {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			sub.queue.Close()
			break
		}
	}
	minOff := s.minAckOffset()
	s.mu.Unlock()
	// 客户端断开后尝试截断已消费事件
	s.events.Compact(minOff)
}

// minAckOffset 返回所有订阅者中最小的已消费偏移。调用方必须持有 s.mu。
// 无订阅者时返回当前事件总数（可全部截断）。
func (s *chatSession) minAckOffset() uint {
	if len(s.subscribers) == 0 {
		return s.events.Len()
	}
	min := s.subscribers[0].offset
	for _, sub := range s.subscribers[1:] {
		if sub.offset < min {
			min = sub.offset
		}
	}
	return min
}

func (s *chatSession) SendMessage(message *chat.Message) error {
	qm := &queuedMessage{
		id:  s.getSeq(),
		msg: message,
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
	for {
		qm, err := s.inbox.Read()
		if err != nil {
			break
		}
		// 队列消息已使用，通知发送者将对应消息显示在对话框
		s.addEvent(chat.NewMessageConsumedEvent(qm.id, s.id))
		s.history = append(s.history, *qm.msg)
	}

	if len(s.history) == 0 {
		return nil
	}

	messages := &chat.Request{
		Messages: make([]chat.Message, len(s.history)),
		System:   s.system,
	}
	if s.opts != nil {
		messages.Model = s.opts.Model
		messages.MaxTokens = s.opts.MaxTokens
		messages.Temperature = s.opts.Temperature
		messages.TopP = s.opts.TopP
		messages.TopK = s.opts.TopK
		messages.StopSequences = s.opts.StopSequences
		messages.Stream = s.opts.Stream
	} else {
		messages.Stream = true
	}
	copy(messages.Messages, s.history)

	if len(s.toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(s.toolExecutors))
		for _, exec := range s.toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}

// saveHistory 将当前历史保存到持久化存储
func (s *chatSession) saveHistory() {
	if s.historyStore == nil {
		return
	}
	if err := s.historyStore.SaveHistory(s.id, s.history); err != nil {
		log.Printf("[chatSession] save history failed for %s: %v", s.id, err)
	}
}

func (s *chatSession) addEvent(event *chat.ClientEvent) {
	s.events.Add(event)
	s.flush()
}

// flush 通知所有客户端有新事件
func (s *chatSession) flush() {
	s.mu.Lock()
	subs := make([]*subscriber, len(s.subscribers))
	copy(subs, s.subscribers)
	s.mu.Unlock()

	for _, sub := range subs {
		err := sub.queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat session: %v", err)
		}
	}
}

func (s *chatSession) ReadEvent(start uint) *chat.EventEntry {
	return s.events.ReadFrom(start)
}
