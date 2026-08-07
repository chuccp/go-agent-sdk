package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// chatHandler 会话处理接口，由 chatSession 实现
type chatHandler interface {
	SendMessage(message *chat.RevMessage, opt ...chat.Option) error
	History() []*chat.Message
	ReadEvent(position *chat.Position) *chat.ClientEvent
	DeleteClient(client *ChatClient)
	Stop()
}

// ChatClient 面向调用方的客户端句柄
type ChatClient struct {
	handler  chatHandler
	queue    *util.Queue[bool]
	position *chat.Position
}

func (c *ChatClient) SendText(message string, opt ...chat.Option) error {
	return c.handler.SendMessage(&chat.RevMessage{Text: message}, opt...)
}

func (c *ChatClient) SendMessage(message *chat.RevMessage, opt ...chat.Option) error {
	return c.handler.SendMessage(message, opt...)
}

func (c *ChatClient) ReadEvent() *chat.ClientEvent {
	for {
		_, hasValue := c.queue.Dequeue()
		if !hasValue {
			return nil
		}
		if event := c.handler.ReadEvent(c.position); event != nil {
			return event
		}
	}
}

func (c *ChatClient) Stop() {
	c.handler.Stop()
}

func (c *ChatClient) Close() {
	c.handler.DeleteClient(c)
}
