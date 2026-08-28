package chat

import (
	"context"
	"sync"
)

// Service 是 LLM 提供方的流式对话接口。
// 每个 provider（如 OpenAI、Anthropic）实现此接口。
type Service interface {
	ChatWithStream(ctx context.Context, chatMessages *Messages, response *BlockStream) error
	ID() string
}

// ServiceStore 管理多个 LLM provider 的注册与路由。
type ServiceStore struct {
	serviceMap map[string]Service
	rLock      *sync.RWMutex
}

func NewServiceStore() *ServiceStore {
	return &ServiceStore{
		serviceMap: make(map[string]Service),
		rLock:      new(sync.RWMutex),
	}
}

func (r *ServiceStore) GetService(id string) Service {
	r.rLock.RLock()
	defer r.rLock.RUnlock()
	if r.serviceMap == nil {
		return nil
	}
	return r.serviceMap[id]
}

func (r *ServiceStore) Register(chatService Service) {
	r.rLock.Lock()
	defer r.rLock.Unlock()
	if r.serviceMap == nil {
		r.serviceMap = make(map[string]Service)
	}
	r.serviceMap[chatService.ID()] = chatService
}
