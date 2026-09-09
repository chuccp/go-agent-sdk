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
func (c *Chat) GetService(config *Config) Service {
	id := c.defaultServiceId
	if config != nil {
		cid := config.GetID()
		if util.IsNotBlank(cid) {
			id = cid
		}
	}
	return c.serviceStore.GetService(id)
}
func (c *Chat) ChatWithStream(ctx context.Context, chatMessages *Messages, response *BlockStream) error {
	return c.GetService(chatMessages.Config).ChatWithStream(ctx, chatMessages, response)
}

func NewChat() *Chat {
	return &Chat{
		serviceStore: &ServiceStore{
			serviceMap: make(map[string]Service),
			rLock:      new(sync.RWMutex),
		},
	}
}
