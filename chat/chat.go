package chat

import "context"

type Chat struct {
	providerRegistry *ProviderRegistry
	provider         string
}

func (c *Chat) ChatWithStream(ctx context.Context, chatMessages *Request, response *BlockStream) error {

	return nil
}
