package chat

import (
	"context"
	"sync"

	"github.com/chuccp/go-agent-sdk/util"
)

type Chat struct {
	serviceStore     *ServiceStore
	defaultServiceId string
}

func (c *Chat) Register(chatService Service) {
	if util.IsBlank(c.defaultServiceId) {
		c.defaultServiceId = chatService.ID()
	}
	c.serviceStore.Register(chatService)
}
func (c *Chat) GetService(id string) Service {
	return c.serviceStore.GetService(id)
}
func (c *Chat) ChatWithStream(ctx context.Context, chatMessages *Messages, response *BlockStream) error {
	id := chatMessages.Config.GetID()
	if util.IsBlank(id) {
		id = c.defaultServiceId
	}
	return c.GetService(id).ChatWithStream(ctx, chatMessages, response)
}

func NewChat() *Chat {
	return &Chat{
		serviceStore: &ServiceStore{
			serviceMap: make(map[string]Service),
			rLock:      new(sync.RWMutex),
		},
	}
}
