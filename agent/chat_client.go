package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

const (
	EventTypeChunk           = "chunk"
	EventTypeError           = "error"
	EventTypeDone            = "done"
	EventTypeMessageSent     = "message_sent"     // 消息已被立即处理，发送者可直接显示在对话列表
	EventTypeMessageQueued   = "message_queued"   // 消息进入等待队列，发送者应标记为待处理
	EventTypeMessageConsumed = "message_consumed" // 队列中的消息已被消费，发送者应将其显示在对话框
)

type Event struct {
	Type           string `json:"type"`
	Content        string `json:"content,omitempty"`
	Done           bool   `json:"done,omitempty"`
	Message        string `json:"message,omitempty"`
	MessageID      uint   `json:"message_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// NewErrorEvent 创建一个错误事件
func NewErrorEvent(message string) *Event {
	return &Event{Type: EventTypeError, Message: message}
}

// NewChunkEvent 创建一个流式文本片段事件
func NewChunkEvent(content, conversationID string) *Event {
	return &Event{Type: EventTypeChunk, Content: content, ConversationID: conversationID}
}

// NewDoneEvent 创建一个流结束事件
func NewDoneEvent(conversationID string) *Event {
	return &Event{Type: EventTypeDone, Done: true, ConversationID: conversationID}
}

// NewMessageSentEvent 创建一个消息已被立即处理事件
func NewMessageSentEvent(messageID uint, conversationID string) *Event {
	return &Event{Type: EventTypeMessageSent, MessageID: messageID, ConversationID: conversationID}
}

// NewMessageQueuedEvent 创建一个消息进入等待队列事件
func NewMessageQueuedEvent(messageID uint, conversationID string) *Event {
	return &Event{Type: EventTypeMessageQueued, MessageID: messageID, ConversationID: conversationID}
}

// NewMessageConsumedEvent 创建一个队列消息已被消费事件
func NewMessageConsumedEvent(messageID uint, conversationID string) *Event {
	return &Event{Type: EventTypeMessageConsumed, MessageID: messageID, ConversationID: conversationID}
}

// ChatHandler 会话处理接口，由 chatSession 实现
type ChatHandler interface {
	SendMessage(message *chat.Message) error
	ReadEvent(start uint) *EventEntry
	DeleteClient(client *ChatClient)
	Stop()
}

// ChatClient 面向调用方的客户端句柄
type ChatClient struct {
	handler ChatHandler
	offset  uint
	queue   *util.Queue[bool]
}

func (c *ChatClient) SendText(message string) error {
	msg := chat.Text(message)
	return c.handler.SendMessage(&msg)
}

func (c *ChatClient) ReadEvent() *Event {
	_, hasValue := c.queue.Dequeue()
	if !hasValue {
		return nil
	}
	entry := c.handler.ReadEvent(c.offset)
	if entry == nil {
		return nil
	}
	c.offset = c.offset + entry.Offset
	return entry.Event
}
func (c *ChatClient) Stop() {
	c.handler.Stop()
}

func (c *ChatClient) Close() {
	c.handler.DeleteClient(c)
}

// EventEntry 单个事件条目，含偏移量信息
type EventEntry struct {
	Start  uint
	Offset uint
	Event  *Event
}

// eventStore 追加式事件日志
type eventStore struct {
	mu      sync.RWMutex
	entries []*EventEntry
}

func newEventStore() *eventStore {
	return &eventStore{
		entries: make([]*EventEntry, 0, 64),
	}
}

func (l *eventStore) add(event *Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, &EventEntry{
		Start:  uint(len(l.entries)),
		Offset: 1,
		Event:  event,
	})
}

// readFrom 从 start 偏移量读取下一个事件，若无新事件返回 nil
func (l *eventStore) readFrom(start uint) *EventEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if int(start) >= len(l.entries) {
		return nil
	}
	return l.entries[start]
}
