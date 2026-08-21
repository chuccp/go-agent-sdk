package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// handler 会话处理接口，由 session 实现
type handler interface {
	WriteBlocks(block ...chat.Block)
	Stop()
}

type readEvents interface {
	readEvents(cl *Client) []*Event
	deleteClient(client *Client)
	history() []*chat.Message
}

// Client 面向调用方的客户端句柄
type Client struct {
	handler    handler
	queue      *util.Queue[bool]
	start      uint64
	readEvents readEvents
}

func (c *Client) WriteText(message string) {
	c.handler.WriteBlocks(chat.NewFullTextBlock(message))
}

func (c *Client) WriteMessage(block ...chat.Block) {
	c.handler.WriteBlocks(block...)
}

// ReadEvent 读取单个事件，无事件时返回 nil（阻塞等待直到有事件或队列关闭）。
func (c *Client) ReadEvent() *Event {
	events := c.ReadEvents()
	if len(events) > 0 {
		return events[0]
	}
	return nil
}

// ReadEvents 阻塞等待直到有新事件到达，然后返回所有可用事件。
// 返回 nil 表示队列已关闭。
func (c *Client) ReadEvents() []*Event {
	_, hasValue := c.queue.Dequeue()
	if !hasValue {
		return nil
	}
	// 通知到达后事件可能尚未写入存储，短暂重试。

	events := c.readEvents.readEvents(c)
	if len(events) > 0 {
		return events
	}

	return nil
}

func (c *Client) Stop() {
	c.handler.Stop()
}

func (c *Client) Close() {
	c.readEvents.deleteClient(c)
}
