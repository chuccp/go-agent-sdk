package agent

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// SessionContext 会话的唯一状态中心：消息队列、运行期状态、事件存储、
// 客户端订阅、工具与配置全部集中于此。工具执行时通过 Turn 获得本上下文。
type SessionContext struct {
	LoopContext
	context.Context
	sessionId     string
	registry      *chat.ProviderRegistry
	chatClients   *util.SliceArray[*Client]
	toolExecutors []ToolExecutor
	store         *Store0
	opts          *chat.Options
	historyStore  HistoryStore
	clientMutex   *sync.Mutex // 保护 chatClients
	seq           uint64
}

// ID 返回会话 ID。
func (c *SessionContext) ID() string { return c.sessionId }

func (c *SessionContext) GetSeq() uint64 {
	return atomic.AddUint64(&c.seq, 1)
}

// SendBlock 追加事件到存储并通知所有客户端。
func (c *SessionContext) SendBlock(no uint64, block chat.Block) {
	c.store.AddBlock(no, block)
	c.flush()
}

func (c *SessionContext) GetService(provider string) chat.Service {
	return c.registry.GetProvider(provider)
}

// Flush 通知所有客户端有新事件可读。
func (c *SessionContext) flush() {
	c.clientMutex.Lock()
	clients := c.chatClients.Slice()
	c.clientMutex.Unlock()
	for _, sub := range clients {
		err := sub.queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat session: %v", err)
		}
	}
}

func (c *SessionContext) Stop() {

}

func (c *SessionContext) ReceiveEvent(position *Position) *Event {
	return c.store.ReadFrom(position)
}

func (c *SessionContext) DeleteClient(client *Client) {
	c.clientMutex.Lock()
	c.chatClients.Remove(client)
	client.queue.Close()
	c.clientMutex.Unlock()
	c.store.RemovePosition(client.position)
}

// GetChatClient 创建一个事件消费客户端：注册读取位置并加入订阅列表。
func (c *SessionContext) GetChatClient(start uint64, handler handler) *Client {
	position := c.store.GetPosition(start)
	chatClient := &Client{
		queue:    util.NewQueue[bool](),
		handler:  handler,
		position: position,
	}
	c.clientMutex.Lock()
	c.chatClients.Append(chatClient)
	c.clientMutex.Unlock()
	return chatClient
}
