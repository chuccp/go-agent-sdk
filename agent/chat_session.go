package agent

import (
	"context"
	"log"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// chatSession 完整会话实体，管理消息队列、对话历史和 LLM 调用
type chatSession struct {
	id                 string
	mu                 sync.Mutex
	unifiedChatService *chat.UnifiedChatService
	revQueue           *util.SliceQueueSafe[*chat.Message]
	events             *eventStore
	history            []chat.Message
	isRun              bool
	provider           string
	queues             []*util.Queue[bool]
	toolExecutors      map[string]ToolExecutor
	system             string
	opts               *Options
	cancel             context.CancelFunc
	seq                uint
	historyStore       HistoryStore
}

func newChatSession(id string, unifiedChatService *chat.UnifiedChatService, toolExecutors map[string]ToolExecutor, system string, opts *Options, historyStore HistoryStore) *chatSession {
	s := &chatSession{
		id:                 id,
		unifiedChatService: unifiedChatService,
		revQueue:           util.NewSliceQueueSafe[*chat.Message](),
		events:             newEventStore(),
		history:            make([]chat.Message, 0),
		isRun:              false,
		queues:             make([]*util.Queue[bool], 0),
		toolExecutors:      toolExecutors,
		system:             system,
		opts:               opts,
		historyStore:       historyStore,
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
	queue := util.NewQueue[bool]()
	s.mu.Lock()
	s.queues = append(s.queues, queue)
	s.mu.Unlock()
	return &ChatClient{
		handler: s,
		queue:   queue,
	}
}

func (s *chatSession) DeleteClient(client *ChatClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.queues {
		if q == client.queue {
			s.queues = append(s.queues[:i], s.queues[i+1:]...)
			q.Close()
			return
		}
	}
}

func (s *chatSession) SendMessage(message *chat.Message) error {
	message.MessageID = s.getSeq()
	err := s.revQueue.Write(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	started := false
	if !s.isRun {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.isRun = true
		started = true
		util.GoWithRecover(func() {
			s.run(ctx)
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := NewErrorEvent("internal error"); evt.Done = true; s.addEvent(evt)
		})
	}
	s.mu.Unlock()

	// 在锁外发送事件，避免 addEvent -> flush -> s.mu.Lock 死锁
	if started {
		// 消息可以立马发出，通知发送者将消息显示在对话列表
		s.addEvent(NewMessageSentEvent(message.MessageID, s.id))
	} else {
		// 消息没有立马发出，通知发送者将消息标记为队列待处理
		s.addEvent(NewMessageQueuedEvent(message.MessageID, s.id))
	}
	return nil
}

// build 从队列中取出所有待处理消息，追加到历史记录，构建 LLM 请求
func (s *chatSession) build() *chat.Messages {
	for {
		msg, err := s.revQueue.Read()
		if err != nil {
			break
		}
		// 队列消息已使用，通知发送者将对应消息显示在对话框
		s.addEvent(NewMessageConsumedEvent(msg.MessageID, s.id))
		s.history = append(s.history, *msg)
	}

	if len(s.history) == 0 {
		return nil
	}

	messages := &chat.Messages{
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

func (s *chatSession) addEvent(event *Event) {
	s.events.add(event)
	s.flush()
}

// flush 通知所有客户端有新事件
func (s *chatSession) flush() {
	s.mu.Lock()
	queues := make([]*util.Queue[bool], len(s.queues))
	copy(queues, s.queues)
	s.mu.Unlock()

	for _, queue := range queues {
		err := queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat session: %v", err)
		}
	}
}

func (s *chatSession) ReadEvent(start uint) *EventEntry {
	return s.events.readFrom(start)
}
