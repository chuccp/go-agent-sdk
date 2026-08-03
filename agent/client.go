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

// ReadEvent 阻塞读取下一个事件。
// 仅当连接关闭（queue 被关闭）时返回 nil；
// 若收到通知但当前位置暂时无可读事件，则继续等待下一次通知，不退出读取循环。
func (c *ChatClient) ReadEvent() *chat.ClientEvent {
	for {
		_, hasValue := c.queue.Dequeue()
		if !hasValue {
			return nil
		}
		if entry := c.handler.ReadEvent(c.position); entry != nil {
			return entry.Event
		}
		// 当前位置暂不可读（如事件已被清理），等待下一次 flush 通知
	}
}

func (c *ChatClient) Stop() {
	c.handler.Stop()
}

func (c *ChatClient) Close() {
	c.handler.DeleteClient(c)
}
