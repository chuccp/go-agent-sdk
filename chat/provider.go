package chat

import (
	"context"
	"sync"
)

type Config struct {
	opts *Options
}

func (m *Config) Option(opt ...Option) {
	for _, o := range opt {
		o(m.opts)
	}
}

// Provider 是 LLM 提供方的流式对话接口。
// 每个 provider（如 OpenAI、Anthropic）实现此接口。
type Provider interface {
	ChatWithStream(ctx context.Context, chatMessages *Request, response *BlockStream) error
	Options(config *Config)
}

// ProviderRegistry 管理多个 LLM provider 的注册与路由。
type ProviderRegistry struct {
	providerMap map[string]Provider
	rLock       *sync.RWMutex
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providerMap: make(map[string]Provider),
		rLock:       new(sync.RWMutex),
	}
}

func (r *ProviderRegistry) GetProvider(provider string) Provider {
	r.rLock.RLock()
	defer r.rLock.RUnlock()
	if r.providerMap == nil {
		return nil
	}
	return r.providerMap[provider]
}

//func (r *ProviderRegistry) ChatWithStream(ctx context.Context, provider string, chatMessages *Request, stream *BlockStream) error {
//	chatService := r.getProvider(provider)
//	if chatService == nil {
//		return errors.New("no such provider: " + provider)
//	}
//	return chatService.ChatWithStream(ctx, chatMessages, stream)
//}

func (r *ProviderRegistry) Register(provider string, chatService Provider, isDefault bool) {
	r.rLock.Lock()
	defer r.rLock.Unlock()
	if r.providerMap == nil {
		r.providerMap = make(map[string]Provider)
	}
	r.providerMap[provider] = chatService
}

func (r *ProviderRegistry) DefaultProvider() string {
	return ""
}
