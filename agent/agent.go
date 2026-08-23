package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

// Agent agent管理器
type Agent struct {
	sessions      map[string]*Session
	lock          *sync.RWMutex
	registry      *chat.ProviderRegistry
	toolExecutors []ToolExecutor
	system        string
	opts          *chat.Options
	historyStore  HistoryStore
}

func NewAgent() *Agent {
	return &Agent{
		sessions:      make(map[string]*Session),
		lock:          new(sync.RWMutex),
		registry:      chat.NewProviderRegistry(),
		toolExecutors: make([]ToolExecutor, 0),
		opts:          chat.DefaultOptions(),
	}
}
func (m *Agent) ChatOption(opt ...chat.Option) {
	for _, o := range opt {
		o(m.opts)
	}
}
func (m *Agent) AddTools(exec ...ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.toolExecutors = append(m.toolExecutors, exec...)
}

// SetSystem 设置全局系统提示词，对之后新建的会话生效。
func (m *Agent) SetSystem(system string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.system = system
}

// SetHistoryStore 设置聊天记录持久化实现。
// 设置后，新建会话会自动加载历史，每轮对话结束后自动保存。
func (m *Agent) SetHistoryStore(store HistoryStore) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.historyStore = store
}

func (m *Agent) RegisterChat(provider string, chatService chat.Service, isDefault bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.registry.Register(provider, chatService, isDefault)
}

// getOrCreateSession 获取或创建会话（内部方法，调用前需持有 m.lock）。
func (m *Agent) getOrCreateSession(id string) *Session {
	if c, ok := m.sessions[id]; ok {
		return c
	}
	// copy toolExecutors 快照，避免 Session 运行期间 AddTool 引发 data race
	tools := make([]ToolExecutor, len(m.toolExecutors))
	copy(tools, m.toolExecutors)
	sessionContext := &SessionContext{
		sessionId:     id,
		seq:           0,
		transfer:      NewTransfer(id, m.historyStore),
		registry:      m.registry,
		toolExecutors: tools,
		opts:          m.opts,
	}
	session := newSession(sessionContext, func(sessionsId string) {
		m.RemoveSession(sessionsId)
	})
	m.sessions[id] = session
	return session
}

func (m *Agent) History(id string) ([]*chat.Message, error) {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	if err := session.LoadHistory(); err != nil {
		return nil, err
	}
	return session.History(), nil
}

func (m *Agent) GetClient(id string, start uint64) (*Client, error) {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	if err := session.LoadHistory(); err != nil {
		return nil, err
	}
	return session.newClient(start), nil
}

func (m *Agent) GetSession(sessionId string) (*Session, bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	s, ok := m.sessions[sessionId]
	return s, ok
}

// RemoveSession 关闭并移除指定会话。若会话不存在则无操作。
func (m *Agent) RemoveSession(sessionsId string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if session, ok := m.sessions[sessionsId]; ok {
		session.Stop()
		delete(m.sessions, sessionsId)
	}
}
