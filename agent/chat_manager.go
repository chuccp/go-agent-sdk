package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

// ChatManager 聊天会话管理器
type ChatManager struct {
	chats              map[string]*chatSession
	lock               *sync.RWMutex
	unifiedChatService *chat.UnifiedChatService
	toolExecutors      map[string]ToolExecutor
	system             string
}

func NewChatManager() *ChatManager {
	return &ChatManager{
		chats:              make(map[string]*chatSession),
		lock:               new(sync.RWMutex),
		unifiedChatService: chat.NewUnifiedChatService(),
		toolExecutors:      make(map[string]ToolExecutor),
	}
}

func (m *ChatManager) AddTool(exec ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.toolExecutors[exec.Definition().Name] = exec
}
func (m *ChatManager) System(system string) {
	m.system = system
}
func (m *ChatManager) RegisterLLM(provider string, chatService chat.IChatService, isDefault bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.unifiedChatService.Register(provider, chatService, isDefault)
}

func (m *ChatManager) GetChat(id string) *ChatClient {
	m.lock.RLock()
	c, ok := m.chats[id]
	m.lock.RUnlock()
	if ok {
		return c.newClient()
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if c, ok = m.chats[id]; ok {
		return c.newClient()
	}
	session := newChatSession(id, m.unifiedChatService, m.toolExecutors, m.system)
	m.chats[id] = session
	return session.newClient()
}
