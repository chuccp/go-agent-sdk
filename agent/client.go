package agent

import (
	"context"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// handler 会话处理接口，由 Session 实现
type handler interface {
	WriteBlocks(block ...chat.Block)
	Stop()
}

type readEvents interface {
	readEvents(cl *Client) []*Event
	deleteClient(client *Client)
	history() []*chat.Message
}

// 超时时间 1 很长时间没有动作，2. start
// Client 面向调用方的客户端句柄
type Client struct {
	ctx        context.Context
	handler    handler
	queue      *util.Queue[bool]
	start      uint64
	readEvents readEvents
	cancel     context.CancelFunc
	once       sync.Once
}

func NewClient(pCtx context.Context, handler handler, queue *util.Queue[bool], start uint64, readEvents readEvents) *Client {
	ctx, cancel := context.WithCancel(pCtx)
	return &Client{
		ctx:        ctx,
		cancel:     cancel,
		handler:    handler,
		queue:      queue,
		start:      start,
		readEvents: readEvents,
	}
}

func (c *Client) WriteText(message string) {
	c.handler.WriteBlocks(chat.NewFullTextBlock(message))
}

func (c *Client) WriteMessage(block ...chat.Block) {
	c.handler.WriteBlocks(block...)
}

// ReadEvents 阻塞等待直到有新事件到达，然后返回所有可用事件。
// 返回 nil 表示队列已关闭。
func (c *Client) ReadEvents() ([]*Event, error) {
	for {
		select {
		case <-c.ctx.Done():
			c.Close()
			return nil, c.ctx.Err()
		default:
		}
		_, hasValue := c.queue.Dequeue()
		if !hasValue {
			return nil, nil
		}
		events := c.readEvents.readEvents(c)
		if len(events) > 0 {
			return events, nil
		}
	}
}

func (c *Client) Stop() {
	c.handler.Stop()
}

func (c *Client) Close() {
	c.once.Do(func() {
		c.cancel()
		c.readEvents.deleteClient(c)
	})
}
