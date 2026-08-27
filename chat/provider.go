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

// Service 是 LLM 提供方的流式对话接口。
// 每个 provider（如 OpenAI、Anthropic）实现此接口。
type Service interface {
	ChatWithStream(ctx context.Context, chatMessages *Request, response *BlockStream) error
	Options(config *Config)
}

// ProviderRegistry 管理多个 LLM provider 的注册与路由。
type ProviderRegistry struct {
	providerMap map[string]Service
	rLock       *sync.RWMutex
	provider    string
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providerMap: make(map[string]Service),
		rLock:       new(sync.RWMutex),
	}
}

func (r *ProviderRegistry) GetProvider(provider string) Service {
	r.rLock.RLock()
	defer r.rLock.RUnlock()
	if r.providerMap == nil {
		return nil
	}
	// 空 provider 名回退到默认 provider（与历史 registry.DefaultProvider() 语义一致）
	if provider == "" {
		provider = r.provider
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

func (r *ProviderRegistry) Register(provider string, chatService Service, isDefault bool) {
	r.rLock.Lock()
	defer r.rLock.Unlock()
	if r.providerMap == nil {
		r.providerMap = make(map[string]Service)
	}
	r.providerMap[provider] = chatService
	if len(r.provider) == 0 {
		r.provider = provider
	}
	if isDefault {
		r.provider = provider
	}
}

func (r *ProviderRegistry) DefaultProvider() string {
	return r.provider
}
