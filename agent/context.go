package agent

import (
	"log"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type SessionContext struct {
	inbox         *util.SliceQueue[*QueuedMessage] // 用户输入消息队列（runMutex 保护）
	running       bool
	seq           uint64
	sessionId     string
	events        *chat.Store
	doLoop        func()
	registry      *chat.ProviderRegistry
	chatClients   *util.SliceArray[*ChatClient]
	toolExecutors map[string]ToolExecutor
	system        string
	opts          *Options
}

func (c *SessionContext) AddEvent(event *chat.ClientEvent) {
	if event.EventType == chat.EventTypeDone {
		log.Printf("[processor] addEvent DONE, sessionId=%s", c.sessionId)
	}
	c.events.Add(event)
	c.Flush()
}

// ConsumeMessage 将一条用户消息追加到历史记录，并发出消费事件。
func (c *SessionContext) ConsumeMessage(qm *QueuedMessage) {
	c.AddEvent(chat.NewMessageConsumedEvent(qm.id, c.sessionId, qm.msg))
	msg := qm.msg.ToMessage()
	c.events.AppendHistory(&msg)
}

func (c *SessionContext) GetPosition(start uint) *chat.Position {

	return nil
}

func (c *SessionContext) Append(client *ChatClient) {

}

func newSessionContext(sessionId string, sessionContext sessionContext, historyStore chat.HistoryStore) *SessionContext {
	events := chat.NewStore(sessionId, historyStore)
	return &SessionContext{
		sessionContext: sessionContext,
		sessionId:      sessionId,
		inbox:          new(util.SliceQueue[*QueuedMessage]),
		running:        false,
		seq:            0,
		events:         events,
		historyStore:   historyStore,
	}
}
