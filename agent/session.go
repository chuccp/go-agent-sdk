package agent

import (
	"context"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

type Sessions struct {
	sessions map[string]*Session
	sync.RWMutex
}

func (s *Sessions) Add(session *Session) {
	s.Lock()
	defer s.Unlock()
	s.sessions[session.sessionContext.sessionId] = session
}
func (s *Sessions) Remove(sessionId string) {
	s.Lock()
	defer s.Unlock()
	delete(s.sessions, sessionId)
}
func (s *Sessions) Get(sessionId string) (*Session, bool) {
	s.RLock()
	defer s.RUnlock()
	session, ok := s.sessions[sessionId]
	return session, ok
}
func NewSessions() *Sessions {
	return &Sessions{sessions: make(map[string]*Session)}
}

// Session 会话门面：会话状态集中于 SessionContext，
// 消息处理与主循环编排委托给 processor（messageProcessor）。
type Session struct {
	sessionContext *SessionContext
	loop           *Loop
	ctx            context.Context
	cancel         context.CancelFunc
	transfer       *Transfer
	sessions       *Sessions
}

func (s *Session) WriteBlocks(blocks ...chat.Block) {
	s.loop.HandleMessage(blocks)
}

func newSession(sessionContext *SessionContext, historyStore HistoryStore, compressor Compressor, sessions *Sessions) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		sessionContext: sessionContext,
		ctx:            ctx,
		cancel:         cancel,
		sessions:       sessions,
	}
	transfer := NewTransfer(sessionContext, compressor, historyStore)
	sessionContext.transfer = transfer
	s.transfer = transfer
	loop := NewLoop(ctx, sessionContext, 0, transfer.GetStore())
	loop.done = func() {
		transfer.Reset()
	}
	s.loop = loop
	return s
}

// History 返回当前会话的完整历史。
func (s *Session) History() []*chat.Message {
	return s.transfer.history()

}

// LoadHistory 从持久化存储加载历史记录。
func (s *Session) LoadHistory() error {
	return s.transfer.LoadHistory()
}

// newClient 创建一个事件消费客户端（订阅委托给 SessionContext）。
func (s *Session) newClient(start uint64) *Client {
	return s.transfer.GetChatClient(start, s)
}

// Stop 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *Session) Stop() {
	s.loop.Stop()
}

// Destroy 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *Session) Destroy() {
	s.sessions.Remove(s.sessionContext.sessionId)
	s.cancel()
}
