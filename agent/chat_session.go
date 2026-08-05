package agent

import (
	"context"
	"log"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// chatSession 完整会话实体，维护客户端订阅，
// 消息接收、主循环、事件存储与运行期状态均委托给 processor（messageProcessor）。
type chatSession struct {
	clientMutex    sync.Mutex // 保护 chatClients
	sessionContext *SessionContext
	processor      *messageProcessor
}

func newChatSession(sessionContext *SessionContext) *chatSession {
	s := &chatSession{
		sessionContext: sessionContext,
	}
	s.processor = newMessageProcessor(sessionContext)
	return s
}

//func (s *chatSession) GetPosition(start uint) *chat.Position {
//	return s.sessionContext.GetPosition(start)
//}

func (s *chatSession) History() []*chat.Message {
	return s.processor.History()
}
func (s *chatSession) newClient(start uint) *ChatClient {
	position := s.sessionContext.GetPosition(start)
	chatClient := &ChatClient{
		queue:    util.NewQueue[bool](),
		handler:  s,
		position: position,
	}
	s.clientMutex.Lock()
	s.sessionContext.Append(chatClient)
	s.clientMutex.Unlock()
	return chatClient
}
func (s *chatSession) LoadHistory() error {
	return s.sessionContext.LoadHistory()
}

func (s *chatSession) DeleteClient(client *ChatClient) {
	s.clientMutex.Lock()
	s.sessionContext.Remove(client)
	client.queue.Close()
	s.clientMutex.Unlock()
	s.sessionContext.RemoveEventPosition(client.position)
}

func (s *chatSession) SendMessage(message *chat.RevMessage, opt ...Option) error {
	s.processor.handleMessage(message, opt...)
	return nil
}

// Stop 取消当前正在运行的会话主循环。
func (s *chatSession) Stop() {
	s.processor.Stop()
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

// Flush 实现 sessionContext 接口，通知所有订阅客户端有新事件可读。
func (s *chatSession) Flush() { s.flush() }

// ChatWithStream 实现 sessionContext 接口，使用默认 provider 发起流式对话请求。
func (s *chatSession) ChatWithStream(ctx context.Context, messages *chat.Request) (*chat.Response, error) {
	provider := s.registry.DefaultProvider()
	return s.registry.ChatWithStream(ctx, provider, messages)
}

// Options 实现 sessionContext 接口。
func (s *chatSession) Options() *Options { return s.opts }

// System 实现 sessionContext 接口。
func (s *chatSession) System() string { return s.system }

// ToolExecutors 实现 sessionContext 接口。
func (s *chatSession) ToolExecutors() map[string]ToolExecutor { return s.toolExecutors }

func (s *chatSession) ReadEvent(position *chat.Position) *chat.ClientEvent {
	return s.processor.ReadEvent(position)
}
