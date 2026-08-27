package chat

import "context"

type Chat struct {
	providerRegistry *ProviderRegistry
	provider         string
}

func (c *Chat) ChatWithStream(ctx context.Context, messages *Messages, response *BlockStream) error {

	return nil
}
