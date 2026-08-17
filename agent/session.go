package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

// session 会话门面：会话状态集中于 SessionContext，
// 消息处理与主循环编排委托给 processor（messageProcessor）。
type session struct {
	sessionContext *SessionContext
	processor      *messageProcessor
}

func newSession(sessionContext *SessionContext) *session {
	s := &session{
		sessionContext: sessionContext,
	}
	s.processor = newMessageProcessor(sessionContext)
	return s
}

// History 返回当前会话的完整历史。
func (s *session) History() []*chat.Message {
	return s.sessionContext.History()
}

// LoadHistory 从持久化存储加载历史记录。
func (s *session) LoadHistory() error {
	return s.sessionContext.events.LoadHistory()
}

// newClient 创建一个事件消费客户端（订阅委托给 SessionContext）。
func (s *session) newClient(start uint) *Client {
	return s.sessionContext.GetChatClient(start, s)
}

// SendMessage 接收一条用户消息，交给会话主循环处理。
func (s *session) SendMessage(message *chat.RevMessage, opt ...chat.Option) error {
	return s.processor.HandleRevMessage(message, opt...)
}

// ReadEvent 从指定位置读取一个事件（委托给 SessionContext 的事件存储）。
func (s *session) ReadEvent(position *Position) *chat.ClientEvent {
	return s.sessionContext.ReadEvent(position)
}

// DeleteClient 注销事件消费客户端（委托给 SessionContext）。
func (s *session) DeleteClient(client *Client) {
	s.sessionContext.DeleteClient(client)
}

// Stop 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *session) Stop() {
	s.processor.Stop()
}
