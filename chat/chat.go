package chat

import (
	"context"
	"errors"
	"sync"

	"github.com/chuccp/go-agent-sdk/util"
)

type LLMOptions struct {
	options map[string]any
}

type IChatService interface {
	ChatWithStream(ctx context.Context, chatMessages *Messages, response *Response) error
}

type UnifiedChatService struct {
	providerMap  map[string]IChatService
	rLock        *sync.RWMutex
	chatServices []IChatService
	provider     string
}

func NewUnifiedChatService() *UnifiedChatService {
	return &UnifiedChatService{
		providerMap:  make(map[string]IChatService),
		rLock:        new(sync.RWMutex),
		chatServices: make([]IChatService, 0),
	}
}

func (service *UnifiedChatService) getProvider(provider string) IChatService {
	service.rLock.RLock()
	defer service.rLock.RUnlock()
	if service.providerMap == nil {
		return nil
	}
	return service.providerMap[provider]
}

func (service *UnifiedChatService) ChatWithStream(ctx context.Context, provider string, chatMessages *Messages) (*Response, error) {
	chatService := service.getProvider(provider)
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
func (service *UnifiedChatService) Register(provider string, chatService IChatService, isDefault bool) {
	service.rLock.Lock()
	defer service.rLock.Unlock()
	if service.providerMap == nil {
		service.providerMap = make(map[string]IChatService)
	}
	service.providerMap[provider] = chatService
	service.chatServices = append(service.chatServices, chatService)
	if len(service.provider) == 0 {
		service.provider = provider
	}
	if isDefault {
		service.provider = provider
	}
}
func (service *UnifiedChatService) DefaultProvider() string {
	return service.provider
}
