package node

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

type ChatNode struct {
	Node
	model          string
	Name           string
	Id             string
	description    string
	systemTemplate string
	userTemplate   string
	options        []chat.Option
}

func (c *ChatNode) GetName() string {
	return c.Name
}
func (c *ChatNode) GetId() string {
	return c.Id
}
func (c *ChatNode) Exec(context WorkflowContext) error {
	return nil
}

type ChatNodeBuilder struct {
	chatNode *ChatNode
}

func NewChatNodeBuilder(id string) *ChatNodeBuilder {
	return &ChatNodeBuilder{
		chatNode: &ChatNode{
			Id:      id,
			options: make([]chat.Option, 0),
		},
	}
}
func (c *ChatNodeBuilder) Description(description string) *ChatNodeBuilder {
	c.chatNode.description = description
	return c
}
func (c *ChatNodeBuilder) SystemTemplate(systemTemplate string) *ChatNodeBuilder {
	c.chatNode.systemTemplate = systemTemplate
	return c
}
func (c *ChatNodeBuilder) UserTemplate(userTemplate string) *ChatNodeBuilder {
	c.chatNode.systemTemplate = userTemplate
	return c
}
func (c *ChatNodeBuilder) Name(name string) *ChatNodeBuilder {
	c.chatNode.Name = name
	return c
}
func (c *ChatNodeBuilder) Model(model string) *ChatNodeBuilder {
	c.chatNode.model = model
	return c
}

func (c *ChatNodeBuilder) Options(options ...chat.Option) *ChatNodeBuilder {
	c.chatNode.options = append(c.chatNode.options, options...)
	return c
}
func (c *ChatNodeBuilder) Build() *ChatNode {
	return c.chatNode
}
