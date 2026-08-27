package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

// SessionContext 会话的唯一状态中心：消息队列、运行期状态、事件存储、
// 客户端订阅、工具与配置全部集中于此。工具执行时通过 Turn 获得本上下文。
type SessionContext struct {
	LoopContext
	sessionId     string
	registry      *chat.ProviderRegistry
	toolExecutors []ToolExecutor
	transfer      *Transfer
	opts          *chat.Options
}

func (c *SessionContext) GetToolExecutor() []ToolExecutor {
	return c.toolExecutors
}

func (c *SessionContext) GetOptions() *chat.Options {
	return c.opts
}

func (c *SessionContext) DefaultProvider() string {
	return c.registry.DefaultProvider()
}

// SessionId 返回会话 ID。
func (c *SessionContext) SessionId() string { return c.sessionId }

func (c *SessionContext) GetStore() *Store {
	return c.transfer.GetStore()
}

// SendBlock 追加事件到存储并通知所有客户端。
func (c *SessionContext) SendBlock(no uint64, block chat.Block) uint64 {
	return c.transfer.SendBlock(no, block)
}

func (c *SessionContext) GetService(provider string) chat.Provider {
	return c.registry.GetProvider(provider)
}

func (c *SessionContext) AppendMainUserMessage(blocks *chat.BlockGroup) {
	userMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleUser, Content: blocks.Content}
	c.GetStore().AppendHistory(userMsg)
}
func (c *SessionContext) AppendMainAssistantMessage(blocks *chat.BlockGroup) {
	assistantMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleAssistant, Content: blocks.Content}
	c.GetStore().AppendHistory(assistantMsg)
}

// GetChatClient 创建一个事件消费客户端：注册读取位置并加入订阅列表。
func (c *SessionContext) GetChatClient(start uint64, handler handler) *Client {
	return c.transfer.GetChatClient(start, handler)
}

func (c *SessionContext) LoadHistory() error {
	return c.transfer.LoadHistory()
}

func (c *SessionContext) History() []*chat.Message {
	return c.transfer.history()
}
