package agent

import (
	"context"

	"github.com/chuccp/go-agent-sdk/chat"
)

// session 会话门面：会话状态集中于 SessionContext，
// 消息处理与主循环编排委托给 processor（messageProcessor）。
type session struct {
	sessionContext *SessionContext
	loop           *Loop
}

func (s *session) WriteBlocks(blocks ...chat.Block) {
	s.loop.HandleMessage(blocks)
}

func newSession(sessionContext *SessionContext) *session {
	s := &session{
		sessionContext: sessionContext,
	}
	loopBuilder := NewLoopBuilder(context.Background(), sessionContext, 0, sessionContext.GetStore())
	loopBuilder.ToolExecutor(sessionContext.toolExecutors...)
	loopBuilder.Provider(sessionContext.registry.DefaultProvider())
	loopBuilder.Done(func() {
		sessionContext.Reset()
	})
	loop := loopBuilder.Build()
	s.loop = loop
	return s
}

// History 返回当前会话的完整历史。
func (s *session) History() []*chat.Message {
	return s.sessionContext.History()

}

// LoadHistory 从持久化存储加载历史记录。
func (s *session) LoadHistory() error {
	return s.sessionContext.LoadHistory()
}

// newClient 创建一个事件消费客户端（订阅委托给 SessionContext）。
func (s *session) newClient(start uint64) *Client {
	return s.sessionContext.GetChatClient(start, s)
}

// Stop 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *session) Stop() {
	s.loop.Stop()
}
