package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

// Agent agent管理器
type Agent struct {
	sessions      *Sessions
	lock          *sync.RWMutex
	registry      *chat.ProviderRegistry
	toolExecutors []ToolExecutor
	system        string
	opts          *chat.Options
	historyStore  HistoryStore
	compressor    Compressor
}

func NewAgent() *Agent {
	return &Agent{
		sessions:      NewSessions(),
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

// SetCompressor 设置上下文压缩策略和持久化实现。
// 设置后，每次 buildRequest 前会调用压缩器对消息列表进行压缩。
// store 可为 nil（无持久化，重启丢失压缩状态）。
func (m *Agent) SetCompressor(c Compressor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.compressor = c
}

func (m *Agent) RegisterChat(provider string, chatService chat.Service, isDefault bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.registry.Register(provider, chatService, isDefault)
}

// getOrCreateSession 获取或创建会话（内部方法，调用前需持有 m.lock）。
func (m *Agent) getOrCreateSession(id string) *Session {
	m.lock.Lock()
	defer m.lock.Unlock()
	if c, ok := m.sessions.Get(id); ok {
		return c
	}
	// copy toolExecutors 快照，避免 Session 运行期间 AddTool 引发 data race
	tools := make([]ToolExecutor, len(m.toolExecutors))
	copy(tools, m.toolExecutors)
	sessionContext := &SessionContext{
		sessionId:     id,
		registry:      m.registry,
		toolExecutors: tools,
		opts:          m.opts,
	}
	session := newSession(sessionContext, m.historyStore, m.compressor, m.sessions)
	m.sessions.Add(session)
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
	s, ok := m.sessions.Get(sessionId)
	return s, ok
}

// SessionContext 获取或创建指定会话的 SessionContext。
// 用于需要直接访问会话上下文的场景（如工具测试、自定义工具实现）。
func (m *Agent) SessionContext(id string) *SessionContext {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	return session.sessionContext
}

// RemoveSession 关闭并移除指定会话。若会话不存在则无操作。
func (m *Agent) RemoveSession(sessionsId string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.sessions.Remove(sessionsId)
}
