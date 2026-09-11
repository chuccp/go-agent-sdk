package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type readEvents interface {
	readEvents(cl *Client) ([]*Event, error)
	deleteClient(client *Client)
	history() []*chat.Message
}

// Client 面向调用方的客户端句柄
type Client struct {
	ctx   context.Context
	queue *util.Queue[bool]
	start uint64

	preStart uint64
	preTime  int64

	readEvents    readEvents
	cancel        context.CancelFunc
	once          sync.Once
	clientTimeout uint
	isClosed      atomic.Bool
}

func NewClient(pCtx context.Context, start uint64, readEvents readEvents) *Client {
	ctx, cancel := context.WithCancel(pCtx)
	return &Client{
		ctx:        ctx,
		cancel:     cancel,
		queue:      util.NewQueue[bool](),
		start:      start,
		readEvents: readEvents,
		preTime:    0,
	}
}
func (c *Client) isTimeout() bool {
	if c.clientTimeout == 0 {
		return false
	}
	if c.preStart == c.start {
		if c.preTime == 0 {
			c.preTime = util.GetSecondTime()
		} else {
			if util.GetSecondTime()-c.preTime > int64(c.clientTimeout) {
				return true
			}
		}
	} else {
		c.preStart = c.start
		c.preTime = 0
	}
	return false
}

// ReadEvents 阻塞等待直到有新事件到达，然后返回所有可用事件。
func (c *Client) ReadEvents() ([]*Event, error) {
	for {
		select {
		case <-c.ctx.Done():
			c.Close()
			return nil, errors.New("client closed")
		default:
		}
		if c.isClosed.Load() {
			return nil, errors.New("client closed")
		}
		events, err := c.readEvents.readEvents(c)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			return events, nil
		}

		_, err = c.queue.Dequeue()
		if err != nil {
			return nil, err
		}

	}
}
func (c *Client) IsClosed() bool {
	return c.isClosed.Load()
}
func (c *Client) Close() {
	c.once.Do(func() {
		c.cancel()
		c.readEvents.deleteClient(c)
		c.isClosed.Store(true)
		c.queue.Close()
	})
}
