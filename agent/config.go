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

// Config 构建期配置。setter 与 Copy 均加锁，可安全地一边配置一边创建 Server；
// CreateServer 内部 Copy 一份，之后对原 Config 的修改不影响已创建的 Server。
type Config struct {
	lock           *sync.RWMutex
	chat           *chat.Chat
	toolExecutors  []ToolExecutor
	chatConfig     *chat.Config
	historyStore   MessageStore
	compressor     Compressor
	lifecycle      FuncLifecycle
	sessionTimeout uint
	clientTimeout  uint
}

type Option func(*Config)

func WithToolExecutor(toolExecutors ...ToolExecutor) Option {
	return func(o *Config) {
		o.AddTools(toolExecutors...)
	}
}

func WithChatOption(opt ...chat.Option) Option {
	return func(o *Config) {
		o.ChatOption(opt...)
	}
}

func (m *Config) ChatOption(opt ...chat.Option) {
	m.lock.Lock()
	defer m.lock.Unlock()
	for _, o := range opt {
		o(m.chatConfig)
	}
}
func (m *Config) AddTools(exec ...ToolExecutor) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.toolExecutors = append(m.toolExecutors, exec...)
}

// AddLifecycle 注册完整的生命周期实现（接口或 FuncLifecycle）。
func (m *Config) AddLifecycle(lifecycle Lifecycle) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.lifecycle.addCreated(lifecycle.OnSessionCreated)
	m.lifecycle.addFirstMessage(lifecycle.OnMessage)
	m.lifecycle.addRoundEnd(lifecycle.OnRoundDone)
	m.lifecycle.addDestroyed(lifecycle.OnSessionDestroyed)
}

// OnSessionCreated 追加会话创建回调。
func (m *Config) OnSessionCreated(fn ...OnSessionCreatedFunc) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.lifecycle.addCreated(fn...)
}

// OnMessage 追加首条消息回调。
func (m *Config) OnMessage(fn ...OnMessageFunc) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.lifecycle.addFirstMessage(fn...)
}

// OnRoundDone 追加轮次结束回调。
func (m *Config) OnRoundDone(fn ...OnRoundDoneFunc) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.lifecycle.addRoundEnd(fn...)
}

// OnSessionDestroyed 追加会话销毁回调。
func (m *Config) OnSessionDestroyed(fn ...OnSessionDestroyedFunc) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.lifecycle.addDestroyed(fn...)
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

// MessageStore 设置聊天记录持久化实现。
// 设置后，新建会话会自动加载历史，每轮对话结束后自动保存。
func (m *Config) MessageStore(store MessageStore) {
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
	chatConfig := chat.Combine(m.chatConfig)
	return &Config{
		lock:           new(sync.RWMutex),
		chat:           m.chat,
		toolExecutors:  append([]ToolExecutor{}, m.toolExecutors...),
		chatConfig:     chatConfig,
		historyStore:   m.historyStore,
		compressor:     m.compressor,
		lifecycle:      m.lifecycle,
		sessionTimeout: m.sessionTimeout,
		clientTimeout:  m.clientTimeout,
	}
}
func (m *Config) CreateServer(ctx context.Context) *Server {
	server := &Server{
		sessions: NewSessions(),
		config:   m.Copy(),
	}
	util.Go(func() {
		server.run(ctx)
	})
	return server
}

func NewConfig() *Config {
	return &Config{
		lock:           new(sync.RWMutex),
		toolExecutors:  make([]ToolExecutor, 0),
		chatConfig:     chat.DefaultConfig(),
		historyStore:   nil,
		compressor:     nil,
		chat:           chat.NewChat(),
		sessionTimeout: defaultSessionTimeout,
		clientTimeout:  defaultClientTimeout,
	}

}
