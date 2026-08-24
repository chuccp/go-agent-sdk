package agent

import (
	"context"

	"github.com/chuccp/go-agent-sdk/chat"
)

// Session 会话门面：会话状态集中于 SessionContext，
// 消息处理与主循环编排委托给 processor（messageProcessor）。
type Session struct {
	sessionContext *SessionContext
	loop           *Loop
	ctx            context.Context
	cancel         context.CancelFunc
	removeSession  func(sessionsId string)
}

func (s *Session) WriteBlocks(blocks ...chat.Block) {
	s.loop.HandleMessage(blocks)
}

func newSession(sessionContext *SessionContext, removeSession func(sessionsId string)) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		sessionContext: sessionContext,
		ctx:            ctx,
		cancel:         cancel,
		removeSession:  removeSession,
	}

	loopBuilder := NewLoopBuilder(ctx, sessionContext, 0, sessionContext.GetStore())
	loopBuilder.ToolExecutor(sessionContext.toolExecutors...)
	loopBuilder.Provider(sessionContext.registry.DefaultProvider())
	if sessionContext.compressor != nil {
		loopBuilder.Compressor(sessionContext.compressor)
	}
	loopBuilder.Done(func() {
		sessionContext.Reset()
	})
	loop := loopBuilder.Build()
	s.loop = loop
	return s
}

// History 返回当前会话的完整历史。
func (s *Session) History() []*chat.Message {
	return s.sessionContext.History()

}

// LoadHistory 从持久化存储加载历史记录。
func (s *Session) LoadHistory() error {
	return s.sessionContext.LoadHistory()
}

// newClient 创建一个事件消费客户端（订阅委托给 SessionContext）。
func (s *Session) newClient(start uint64) *Client {
	return s.sessionContext.GetChatClient(start, s)
}

// Stop 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *Session) Stop() {
	s.loop.Stop()
}

// Destroy 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *Session) Destroy() {
	s.cancel()
	s.removeSession(s.sessionContext.sessionId)
}
