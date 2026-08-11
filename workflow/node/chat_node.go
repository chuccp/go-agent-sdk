package node

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

// DeliverMode 节点产出去向。
type DeliverMode string

const (
	// DeliverEvent 全文随事件直达前端渲染（生成物默认）；主 LLM 只拿摘要。
	DeliverEvent DeliverMode = "event"
	// DeliverContext 全文返回主 LLM（需要内容继续加工的产出）。
	DeliverContext DeliverMode = "context"
)

type ChatNode struct {
	Node
	model          string
	Name           string
	Id             string
	description    string
	systemTemplate string
	userTemplate   string
	deliver        DeliverMode
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

// SystemTemplate 返回系统提示模板。
func (c *ChatNode) SystemTemplate() string { return c.systemTemplate }

// UserTemplate 返回用户提示模板。
func (c *ChatNode) UserTemplate() string { return c.userTemplate }

// Model 返回节点指定的模型（空表示用默认 provider）。
func (c *ChatNode) Model() string { return c.model }

// Deliver 返回产出去向（默认 event）。
func (c *ChatNode) Deliver() DeliverMode {
	if c.deliver == "" {
		return DeliverEvent
	}
	return c.deliver
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
	c.chatNode.userTemplate = userTemplate
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

// Deliver 设置产出去向（DeliverEvent / DeliverContext）。
func (c *ChatNodeBuilder) Deliver(mode DeliverMode) *ChatNodeBuilder {
	c.chatNode.deliver = mode
	return c
}

func (c *ChatNodeBuilder) Options(options ...chat.Option) *ChatNodeBuilder {
	c.chatNode.options = append(c.chatNode.options, options...)
	return c
}
func (c *ChatNodeBuilder) Build() *ChatNode {
	return c.chatNode
}
