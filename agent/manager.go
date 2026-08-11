package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// Manager agent管理器
type Manager struct {
	chats         map[string]*session
	lock          *sync.RWMutex
	registry      *chat.ProviderRegistry
	toolExecutors []ToolExecutor
	system        string
	opts          *chat.Options
	historyStore  HistoryStore
}

func NewManager(opt ...chat.Option) *Manager {
	opts := chat.DefaultOptions()
	for _, o := range opt {
		o(opts)
	}
	return &Manager{
		chats:         make(map[string]*session),
		lock:          new(sync.RWMutex),
		registry:      chat.NewProviderRegistry(),
		toolExecutors: make([]ToolExecutor, 0),
		opts:          opts,
	}
}

func (m *Manager) AddTool(exec ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.toolExecutors = append(m.toolExecutors, exec)
}

// SetSystem 设置全局系统提示词，对之后新建的会话生效。
func (m *Manager) SetSystem(system string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.system = system
}

// SetHistoryStore 设置聊天记录持久化实现。
// 设置后，新建会话会自动加载历史，每轮对话结束后自动保存。
func (m *Manager) SetHistoryStore(store HistoryStore) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.historyStore = store
}

func (m *Manager) RegisterChat(provider string, chatService chat.ChatService, isDefault bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.registry.Register(provider, chatService, isDefault)
}

// getOrCreateSession 获取或创建会话（内部方法，调用前需持有 m.lock）。
func (m *Manager) getOrCreateSession(id string) *session {
	if c, ok := m.chats[id]; ok {
		return c
	}
	// copy toolExecutors 快照，避免 session 运行期间 AddTool 引发 data race
	tools := make([]ToolExecutor, len(m.toolExecutors))
	copy(tools, m.toolExecutors)
	sessionContext := &SessionContext{
		sessionId:     id,
		inbox:         new(util.SliceQueue[*QueuedMessage]),
		running:       false,
		seq:           0,
		events:        NewStore(id, m.historyStore),
		registry:      m.registry,
		chatClients:   new(util.SliceArray[*Client]),
		toolExecutors: tools,
		system:        m.system,
		opts:          m.opts,
		historyStore:  m.historyStore,
		clientMutex:   new(sync.Mutex),
	}
	session := newSession(sessionContext)
	m.chats[id] = session
	return session
}

func (m *Manager) History(id string) ([]*chat.Message, error) {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	if err := session.LoadHistory(); err != nil {
		return nil, err
	}
	return session.History(), nil
}

func (m *Manager) GetClient(id string, start uint) (*Client, error) {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	if err := session.LoadHistory(); err != nil {
		return nil, err
	}
	return session.newClient(start), nil
}

// RemoveChat 关闭并移除指定会话。若会话不存在则无操作。
func (m *Manager) RemoveChat(id string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if session, ok := m.chats[id]; ok {
		session.Stop()
		delete(m.chats, id)
	}
}
