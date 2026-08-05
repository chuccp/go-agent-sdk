package agent

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type SessionContext struct {
	processorHandler
	chatHandler
	inbox         *util.SliceQueue[*QueuedMessage] // 用户输入消息队列（runMutex 保护）
	running       bool
	seq           uint64
	sessionId     string
	events        *chat.Store
	registry      *chat.ProviderRegistry
	chatClients   *util.SliceArray[*ChatClient]
	toolExecutors map[string]ToolExecutor
	system        string
	opts          *Options
	historyStore  chat.HistoryStore
	clientMutex   *sync.Mutex // 保护 chatClients
}

func (c *SessionContext) AddEvent(event *chat.ClientEvent) {
	if event.EventType == chat.EventTypeDone {
		log.Printf("[processor] addEvent DONE, sessionId=%s", c.sessionId)
	}
	c.events.Add(event)
	c.Flush()
}
func (c *SessionContext) Flush() {}

// ConsumeMessage 将一条用户消息追加到历史记录，并发出消费事件。
func (c *SessionContext) ConsumeMessage(qm *QueuedMessage) {
	c.AddEvent(chat.NewMessageConsumedEvent(qm.id, c.sessionId, qm.msg))
	msg := qm.msg.ToMessage()
	c.events.AppendHistory(&msg)
}

func (c *SessionContext) GetChatClient(start uint) *ChatClient {
	position := c.events.GetPosition(start)
	chatClient := &ChatClient{
		queue:    util.NewQueue[bool](),
		handler:  c,
		position: position,
	}
	c.clientMutex.Lock()
	c.chatClients.Append(chatClient)
	c.clientMutex.Unlock()
	return chatClient

}

func (c *SessionContext) SendMessage(message *chat.RevMessage, opt ...Option) error {
	return nil
}
func (c *SessionContext) History() []*chat.Message {
	return nil
}
func (c *SessionContext) ReadEvent(position *chat.Position) *chat.ClientEvent {
	return nil
}
func (c *SessionContext) DeleteClient(client *ChatClient) {

}
func (c *SessionContext) Stop() {

}

func (c *SessionContext) getSeq() uint64 {
	return atomic.AddUint64(&c.seq, 1)
}
