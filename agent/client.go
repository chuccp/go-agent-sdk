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

func (c *Client) ReadEvents() []*Event {
	for {
		_, hasValue := c.queue.Dequeue()
		if !hasValue {
			return nil
		}
		allEvents := make([]*Event, 0)
		for {
			events := c.readEvents.readEvents(c)
			if len(events) > 0 {
				allEvents = append(allEvents, events...)
			} else {
				if len(allEvents) == 0 {
					return nil
				}
				return allEvents
			}
		}
	}
}

func (c *Client) Stop() {
	c.handler.Stop()
}

func (c *Client) Close() {
	c.readEvents.deleteClient(c)
}
