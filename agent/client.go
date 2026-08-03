package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// ChatHandler 会话处理接口，由 chatSession 实现
type ChatHandler interface {
	SendMessage(message *chat.RevMessage, opt ...Option) error
	History() []*chat.Message
	ReadEvent(position *chat.Position) *chat.EventEntry
	DeleteClient(client *ChatClient)
	Stop()
}

// ChatClient 面向调用方的客户端句柄
type ChatClient struct {
	handler  ChatHandler
	queue    *util.Queue[bool]
	position *chat.Position
}

func (c *ChatClient) SendText(message string, opt ...Option) error {
	return c.handler.SendMessage(&chat.RevMessage{Text: message}, opt...)
}

// SendMessage 发送用户输入消息（支持文本 + 附件）。
func (c *ChatClient) SendMessage(message *chat.RevMessage, opt ...Option) error {
	return c.handler.SendMessage(message, opt...)
}

func (c *ChatClient) ReadEvent() *chat.ClientEvent {
	_, hasValue := c.queue.Dequeue()
	if !hasValue {
		return nil
	}
	entry := c.handler.ReadEvent(c.position)
	if entry == nil {
		return nil
	}

	return entry.Event
}

func (c *ChatClient) Stop() {
	c.handler.Stop()
}

func (c *ChatClient) Close() {
	c.handler.DeleteClient(c)
}
