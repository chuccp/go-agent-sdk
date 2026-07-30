package chat

import (
	"context"
	"errors"
	"sync"

	"github.com/chuccp/go-agent-sdk/util"
)

// ChatService 是 LLM 提供方的流式对话接口。
// 每个 provider（如 OpenAI、Anthropic）实现此接口。
type ChatService interface {
	ChatWithStream(ctx context.Context, chatMessages *Request, response StreamWriter) error
}

// ProviderRegistry 管理多个 LLM provider 的注册与路由。
type ProviderRegistry struct {
	providerMap map[string]ChatService
	rLock       *sync.RWMutex
	provider    string
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providerMap: make(map[string]ChatService),
		rLock:       new(sync.RWMutex),
	}
}

func (r *ProviderRegistry) getProvider(provider string) ChatService {
	r.rLock.RLock()
	defer r.rLock.RUnlock()
	if r.providerMap == nil {
		return nil
	}
	return r.providerMap[provider]
}

func (r *ProviderRegistry) ChatWithStream(ctx context.Context, provider string, chatMessages *Request) (*Response, error) {
	chatService := r.getProvider(provider)
	if chatService == nil {
		return nil, errors.New("no such provider: " + provider)
	}
	response := NewResponse()
	util.Go(func() {
		err := chatService.ChatWithStream(ctx, chatMessages, response)
		if err != nil {
			response.WriteError(err)
		}
	})
	return response, nil
}

func (r *ProviderRegistry) Register(provider string, chatService ChatService, isDefault bool) {
	r.rLock.Lock()
	defer r.rLock.Unlock()
	if r.providerMap == nil {
		r.providerMap = make(map[string]ChatService)
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
