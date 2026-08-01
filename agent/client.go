package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// ChatHandler 会话处理接口，由 chatSession 实现
type ChatHandler interface {
	SendMessage(message *chat.Message, opt ...Option) error
	History() []*chat.Message
	ReadEvent(start uint) *chat.EventEntry
	DeleteClient(client *ChatClient)
	Stop()
}

// ChatClient 面向调用方的客户端句柄
type ChatClient struct {
	handler ChatHandler
	queue   *util.Queue[bool]
	offset  uint
	start   uint
}

func (c *ChatClient) SendText(message string, opt ...Option) error {
	msg := chat.Text(message)
	return c.handler.SendMessage(&msg, opt...)
}

func (c *ChatClient) ReadEvent() *chat.ClientEvent {
	_, hasValue := c.queue.Dequeue()
	if !hasValue {
		return nil
	}
	entry := c.handler.ReadEvent(c.offset)
	if entry == nil {
		return nil
	}
	c.offset += entry.Offset
	return entry.Event
}

func (c *ChatClient) Stop() {
	c.handler.Stop()
}

func (c *ChatClient) Close() {
	c.handler.DeleteClient(c)
}
