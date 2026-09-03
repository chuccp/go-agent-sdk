package agent

import (
	"context"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

const (
	defaultSessionTimeout = 600
	defaultClientTimeout  = 300
)

// Config 构建期配置。setter 与 Copy 均加锁，可安全地一边配置一边创建 Agent；
// CreateAgent 内部 Copy 一份，之后对原 Config 的修改不影响已创建的 Agent。
type Config struct {
	lock           *sync.RWMutex
	chat           *chat.Chat
	toolExecutors  []ToolExecutor
	systemPrompt   string
	config         *chat.Config
	historyStore   MessageStore
	compressor     Compressor
	sessionTimeout uint
	clientTimeout  uint
}

func (m *Config) ChatOption(opt ...chat.Option) {
	m.lock.Lock()
	defer m.lock.Unlock()
	for _, o := range opt {
		o(m.config)
	}
}
func (m *Config) AddTools(exec ...ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.toolExecutors = append(m.toolExecutors, exec...)
}

// SessionTimeout 秒
func (m *Config) SessionTimeout(sessionTimeout uint) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.sessionTimeout = sessionTimeout
}

// ClientTimeout 秒
func (m *Config) ClientTimeout(clientTimeout uint) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.clientTimeout = clientTimeout
}

// SystemPrompt 设置全局系统提示词，对之后新建的会话生效。
func (m *Config) SystemPrompt(systemPrompt string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.systemPrompt = systemPrompt
}

// HistoryStore 设置聊天记录持久化实现。
// 设置后，新建会话会自动加载历史，每轮对话结束后自动保存。
func (m *Config) HistoryStore(store MessageStore) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.historyStore = store
}

// Compressor 设置上下文压缩策略和持久化实现。
// 设置后，每次 buildRequest 前会调用压缩器对消息列表进行压缩。
// store 可为 nil（无持久化，重启丢失压缩状态）。
func (m *Config) Compressor(c Compressor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.compressor = c
}

func (m *Config) RegisterChat(chatService chat.Service) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.chat.Register(chatService)
}

// Copy 返回 Config 的独立副本：锁、工具列表、chat.Config 各自新建，
// chat / historyStore / compressor 等共享资源沿用原指针。
func (m *Config) Copy() *Config {
	m.lock.RLock()
	defer m.lock.RUnlock()
	config := m.config
	if config != nil {
		config = chat.Combine(config)
	}
	return &Config{
		lock:           new(sync.RWMutex),
		chat:           m.chat,
		toolExecutors:  append([]ToolExecutor(nil), m.toolExecutors...),
		systemPrompt:   m.systemPrompt,
		config:         config,
		historyStore:   m.historyStore,
		compressor:     m.compressor,
		sessionTimeout: m.sessionTimeout,
		clientTimeout:  m.clientTimeout,
	}
}
func (m *Config) CreateAgent(ctx context.Context) *Agent {
	agent := &Agent{
		sessions: NewSessions(),
		config:   m.Copy(),
	}
	util.Go(func() {
		agent.run(ctx)
	})
	return agent
}

func NewConfig() *Config {
	return &Config{
		lock:           new(sync.RWMutex),
		toolExecutors:  make([]ToolExecutor, 0),
		systemPrompt:   "",
		config:         chat.DefaultConfig(),
		historyStore:   nil,
		compressor:     nil,
		chat:           chat.NewChat(),
		sessionTimeout: defaultSessionTimeout,
		clientTimeout:  defaultClientTimeout,
	}

}
