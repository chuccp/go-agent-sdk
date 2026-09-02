package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

// Agent agent管理器
type Agent struct {
	sessions *Sessions
	lock     *sync.RWMutex
	config   *Config
}

func NewAgent() *Agent {
	return &Agent{
		sessions: NewSessions(),
		lock:     new(sync.RWMutex),
		config:   NewConfig(),
	}
}
func (m *Agent) ChatOption(opt ...chat.Option) {
	for _, o := range opt {
		o(m.config.config)
	}
}
func (m *Agent) AddTools(exec ...ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.toolExecutors = append(m.config.toolExecutors, exec...)
}

// SystemPrompt 设置全局系统提示词，对之后新建的会话生效。
func (m *Agent) SystemPrompt(systemPrompt string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.systemPrompt = systemPrompt
}

// HistoryStore 设置聊天记录持久化实现。
// 设置后，新建会话会自动加载历史，每轮对话结束后自动保存。
func (m *Agent) HistoryStore(store MessageStore) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.historyStore = store
}

// Compressor 设置上下文压缩策略和持久化实现。
// 设置后，每次 buildRequest 前会调用压缩器对消息列表进行压缩。
// store 可为 nil（无持久化，重启丢失压缩状态）。
func (m *Agent) Compressor(c Compressor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.compressor = c
}

func (m *Agent) RegisterChat(chatService chat.Service) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.chat.Register(chatService)
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

// SessionTimeout 秒
func (m *Agent) SessionTimeout(sessionTimeout uint) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.sessionTimeout = sessionTimeout
}

// ClientTimeout 秒
func (m *Agent) ClientTimeout(clientTimeout uint) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.config.clientTimeout = clientTimeout
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
