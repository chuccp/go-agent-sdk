package agent

import (
	"context"

	"github.com/chuccp/go-agent-sdk/chat"
)

// SessionContext 会话的唯一状态中心：消息队列、运行期状态、事件存储、
// 客户端订阅、工具与配置全部集中于此。工具执行时通过 Turn 获得本上下文。
type SessionContext struct {
	context.Context
	sessionId string
	chat      *chat.Chat
	transfer  *Transfer
	opts      *chat.Config
}

func (c *SessionContext) SubAgentStore() *Store {
	return c.transfer.SubAgentStore()

}

func (c *SessionContext) GetChat() *chat.Chat {
	return c.chat
}

func (c *SessionContext) GetConfig() *chat.Config {
	return c.opts
}

func (c *SessionContext) GetOptions() *chat.Config {
	return c.opts
}

// SessionId 返回会话 ID。
func (c *SessionContext) SessionId() string { return c.sessionId }

func (c *SessionContext) AgentStore() *Store {
	return c.transfer.AgentStore()
}

func (c *SessionContext) AppendMainUserMessage(blocks *chat.BlockGroup) {
	userMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleUser, Content: blocks.Content}
	c.AgentStore().AppendHistory(userMsg)
}
func (c *SessionContext) AppendMainAssistantMessage(blocks *chat.BlockGroup) {
	assistantMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleAssistant, Content: blocks.Content}
	c.AgentStore().AppendHistory(assistantMsg)
}

// GetChatClient 创建一个事件消费客户端：注册读取位置并加入订阅列表。
func (c *SessionContext) GetChatClient(ctx context.Context, start uint64, handler handler) *Client {
	return c.transfer.GetChatClient(ctx, start, handler)
}

func (c *SessionContext) History() []*chat.Message {
	return c.transfer.history()
}
