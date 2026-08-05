package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// ChatManager 聊天会话管理器
type ChatManager struct {
	chats         map[string]*chatSession
	lock          *sync.RWMutex
	registry      *chat.ProviderRegistry
	toolExecutors map[string]ToolExecutor
	system        string
	opts          *Options
	historyStore  chat.HistoryStore
}

func NewChatManager(opt ...Option) *ChatManager {
	opts := defaultOptions()
	for _, o := range opt {
		o(opts)
	}
	return &ChatManager{
		chats:         make(map[string]*chatSession),
		lock:          new(sync.RWMutex),
		registry:      chat.NewProviderRegistry(),
		toolExecutors: make(map[string]ToolExecutor),
		opts:          opts,
	}
}

func (m *ChatManager) AddTool(exec ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.toolExecutors[exec.Definition().Name] = exec
}

// SetSystem 设置全局系统提示词，对之后新建的会话生效。
func (m *ChatManager) SetSystem(system string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.system = system
}

// SetHistoryStore 设置聊天记录持久化实现。
// 设置后，新建会话会自动加载历史，每轮对话结束后自动保存。
func (m *ChatManager) SetHistoryStore(store chat.HistoryStore) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.historyStore = store
}

func (m *ChatManager) RegisterChat(provider string, chatService chat.ChatService, isDefault bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.registry.Register(provider, chatService, isDefault)
}

// getOrCreateSession 获取或创建会话（内部方法，调用前需持有 m.lock）。
func (m *ChatManager) getOrCreateSession(id string) *chatSession {
	if c, ok := m.chats[id]; ok {
		return c
	}
	// copy toolExecutors 快照，避免 session 运行期间 AddTool 引发 data race
	tools := make(map[string]ToolExecutor, len(m.toolExecutors))
	for k, v := range m.toolExecutors {
		tools[k] = v
	}

	sessionContext := &SessionContext{
		sessionId:     id,
		inbox:         new(util.SliceQueue[*QueuedMessage]),
		running:       false,
		seq:           0,
		events:        chat.NewStore(id, m.historyStore),
		registry:      m.registry,
		toolExecutors: tools,
		system:        m.system,
		opts:          m.opts,
	}

	session := newChatSession(id, m.registry, tools, m.system, m.opts, m.historyStore)
	m.chats[id] = session
	return session
}

func (m *ChatManager) History(id string) ([]*chat.Message, error) {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	if err := session.LoadHistory(); err != nil {
		return nil, err
	}
	return session.History(), nil
}

func (m *ChatManager) GetChat(id string, start uint) (*ChatClient, error) {
	m.lock.Lock()
	session := m.getOrCreateSession(id)
	m.lock.Unlock()
	if err := session.LoadHistory(); err != nil {
		return nil, err
	}
	return session.newClient(start), nil
}

// RemoveChat 关闭并移除指定会话。若会话不存在则无操作。
func (m *ChatManager) RemoveChat(id string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if session, ok := m.chats[id]; ok {
		session.Stop()
		delete(m.chats, id)
	}
}
