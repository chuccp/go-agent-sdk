package agent

import (
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
}

func newChatSession(id string, unifiedChatService *chat.UnifiedChatService, toolExecutors map[string]ToolExecutor) *chatSession {
	return &chatSession{
		id:                 id,
		unifiedChatService: unifiedChatService,
		revQueue:           util.NewSliceQueueSafe[*chat.Message](),
		events:             newEventStore(),
		history:            make([]chat.Message, 0),
		isRun:              false,
		queues:             make([]*util.Queue[bool], 0),
		toolExecutors:      toolExecutors,
	}
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
	err := s.revQueue.Write(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !s.isRun {
		s.isRun = true
		go s.run()
	}
	s.mu.Unlock()
	return nil
}

// build 从队列中取出所有待处理消息，追加到历史记录，构建 LLM 请求
func (s *chatSession) build() *chat.Messages {
	for {
		msg, err := s.revQueue.Read()
		if err != nil {
			break
		}
		s.history = append(s.history, *msg)
	}

	if len(s.history) == 0 {
		return nil
	}

	messages := &chat.Messages{
		Messages: make([]chat.Message, len(s.history)),
		Stream:   true,
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
