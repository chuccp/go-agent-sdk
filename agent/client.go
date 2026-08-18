package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// handler 会话处理接口，由 session 实现
type handler interface {
	SendMessage(message *chat.RevMessage, opt ...chat.Option) error
	History() []*chat.Message
	ReadEvent(position *Position) *Event
	DeleteClient(client *Client)
	Stop()
}

// Client 面向调用方的客户端句柄
type Client struct {
	handler  handler
	queue    *util.Queue[bool]
	position *Position
}

func (c *Client) SendText(message string, opt ...chat.Option) error {
	return c.handler.SendMessage(&chat.RevMessage{Text: message}, opt...)
}

func (c *Client) SendMessage(message *chat.RevMessage, opt ...chat.Option) error {
	return c.handler.SendMessage(message, opt...)
}

func (c *Client) ReadEvent() *Event {
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

func (c *Client) Stop() {
	c.handler.Stop()
}

func (c *Client) Close() {
	c.handler.DeleteClient(c)
}
