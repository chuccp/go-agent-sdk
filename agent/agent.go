package agent

import (
	"context"
	"sync"
	"time"
)

// Agent agent管理器
type Agent struct {
	sessions *Sessions
	lock     sync.RWMutex
	config   *Config
}

func (m *Agent) GetOrCreateSession(sessionId string) *Session {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.getOrCreateSession(sessionId)
}

// getOrCreateSession 获取或创建会话（内部方法，调用前需持有 m.lock）。
func (m *Agent) getOrCreateSession(sessionId string) *Session {
	if c, ok := m.sessions.Get(sessionId); ok {
		return c
	}
	session := newSession(sessionId, m.config, m.sessions)
	session.sessionTimeout = m.config.sessionTimeout
	session.clientTimeout = m.config.clientTimeout
	m.sessions.Add(session)
	return session
}

func (m *Agent) GetSession(sessionId string) (*Session, bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	s, ok := m.sessions.Get(sessionId)
	return s, ok
}

// SessionContext 获取或创建指定会话的 SessionContext。
// 用于需要直接访问会话上下文的场景（如工具测试、自定义工具实现）。
func (m *Agent) SessionContext(sessionId string) *SessionContext {
	m.lock.Lock()
	session := m.getOrCreateSession(sessionId)
	m.lock.Unlock()
	return session.sessionContext
}

// RemoveSession 关闭并移除指定会话。若会话不存在则无操作。
func (m *Agent) RemoveSession(sessionsId string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	s, ok := m.sessions.Get(sessionsId)
	if ok {
		s.Destroy()
	}
}

// run 启动后台清理循环：周期性遍历会话，销毁空闲超时的会话。
// 该方法会阻塞，通常放在独立 goroutine 中运行；ctx 取消时退出。
func (m *Agent) run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.sessions.ForEach(func(session *Session) bool {
				session.Destroy()
				return true
			})
			return
		case <-ticker.C:
			m.sessions.ForEach(func(session *Session) bool {
				session.checkTimeout()
				return true
			})
		}
	}
}
