package agent

import (
	"context"
	"sync"
	"time"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
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
	sessionTimeout uint
	clientTimeout  uint
	sessions       *Sessions
	lastTime       int64
}

func (s *Session) WriteBlocks(blocks ...chat.Block) {
	s.lastTime = util.GetSecondTime()
	s.loop.HandleMessage(blocks)
}

func newSession(id string, config *Config, sessions *Sessions) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	transfer := NewTransfer(id, config.compressor, config.historyStore)
	sessionContext := &SessionContext{
		Context:   ctx,
		sessionId: id,
		chat:      config.chat,
		opts:      config.config,
		transfer:  transfer,
	}
	s := &Session{
		sessionContext: sessionContext,
		ctx:            ctx,
		cancel:         cancel,
		sessions:       sessions,
		transfer:       transfer,
		lastTime:       util.GetSecondTime(),
	}
	s.loop = NewLoopBuilder(0, sessionContext).
		Config(config.config).
		Store(transfer.GetStore()).
		ToolExecutor(config.toolExecutors...).
		Build()
	util.Go(func() {
		s.checkTimeout()
	})
	return s
}

func (s *Session) checkTimeout() {
	if s.sessionTimeout == 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(s.sessionTimeout) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if util.GetSecondTime()-s.lastTime > int64(s.sessionTimeout) {
				s.Destroy()
				return
			}
		}
	}
}

func (s *Session) SessionTimeout(sessionTimeout uint) {
	s.sessionTimeout = sessionTimeout
}

// ClientTimeout 秒
func (s *Session) ClientTimeout(clientTimeout uint) {
	s.clientTimeout = clientTimeout
}

// LoadMessagesAfter 从持久化存储加载历史记录。
func (s *Session) LoadMessagesAfter(since uint64) ([]*Event, error) {
	return s.transfer.LoadMessagesAfter(since)
}

// CreateClient 创建一个事件消费客户端（订阅委托给 SessionContext）。
func (s *Session) CreateClient(ctx context.Context, start uint64) *Client {
	client := s.transfer.GetChatClient(ctx, start, s)
	client.clientTimeout = s.clientTimeout
	return client
}

// Stop 停止当前轮次（只对单轮生效），后续用户消息不受影响。
func (s *Session) Stop() {
	s.loop.Stop()
}

// Destroy 销毁Session
func (s *Session) Destroy() {
	s.sessions.Remove(s.sessionContext.sessionId)
	s.cancel()
}
