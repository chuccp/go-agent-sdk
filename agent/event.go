package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

const (
	EventTypeChunk           = "chunk"
	EventTypeError           = "error"
	EventTypeDone            = "done"
	EventTypeToolExecution   = "tool_execution"   // 工具正在执行，携带工具名称和输出
	EventTypeMessageSent     = "message_sent"     // 消息已被立即处理，发送者可直接显示在对话列表
	EventTypeMessageQueued   = "message_queued"   // 消息进入等待队列，发送者应标记为待处理
	EventTypeMessageConsumed = "message_consumed" // 队列中的消息已被消费，发送者应将其显示在对话框
)

// ClientEvent 是面向客户端的推送事件。
// 实现 chat.Event 接口，与协议层事件统一处理。
type ClientEvent struct {
	Seq            uint   `json:"seq"`                       // 事件序列号，单调递增，客户端可用于去重和断线续传
	EventSource    string `json:"source"`                    // 事件来源："ai" | "client" | "system"
	EventType      string `json:"type"`
	Content        string `json:"content,omitempty"`
	Done           bool   `json:"done,omitempty"`
	Message        string `json:"message,omitempty"`
	MessageID      uint   `json:"message_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// Type 实现 chat.Event 接口。
func (e *ClientEvent) Type() string { return e.EventType }

// Source 实现 chat.Event 接口。
func (e *ClientEvent) Source() string { return e.EventSource }

// 编译期确认 ClientEvent 实现 chat.Event
var _ chat.Event = (*ClientEvent)(nil)

// NewErrorEvent 创建一个错误事件
func NewErrorEvent(message string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceSystem, EventType: EventTypeError, Message: message}
}

// NewChunkEvent 创建一个流式文本片段事件
func NewChunkEvent(content, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceAI, EventType: EventTypeChunk, Content: content, ConversationID: conversationID}
}

// NewDoneEvent 创建一个流结束事件
func NewDoneEvent(conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceAI, EventType: EventTypeDone, Done: true, ConversationID: conversationID}
}

// NewMessageSentEvent 创建一个消息已被立即处理事件
func NewMessageSentEvent(messageID uint, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceClient, EventType: EventTypeMessageSent, MessageID: messageID, ConversationID: conversationID}
}

// NewMessageQueuedEvent 创建一个消息进入等待队列事件
func NewMessageQueuedEvent(messageID uint, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceClient, EventType: EventTypeMessageQueued, MessageID: messageID, ConversationID: conversationID}
}

// NewMessageConsumedEvent 创建一个队列消息已被消费事件
func NewMessageConsumedEvent(messageID uint, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceClient, EventType: EventTypeMessageConsumed, MessageID: messageID, ConversationID: conversationID}
}

// NewToolExecutionEvent 创建一个工具执行事件，携带工具名称和执行输出。
func NewToolExecutionEvent(toolName, output, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: chat.SourceAI, EventType: EventTypeToolExecution, Content: output, Message: toolName, ConversationID: conversationID}
}

// EventEntry 单个事件条目，含偏移量信息
type EventEntry struct {
	Start  uint
	Offset uint
	Event  *ClientEvent
}

// eventStore 追加式事件日志，支持截断已消费的事件以防止无界增长。
type eventStore struct {
	mu      sync.RWMutex
	entries []*EventEntry
	base    uint // entries[0] 对应的全局偏移
}

func newEventStore() *eventStore {
	return &eventStore{
		entries: make([]*EventEntry, 0, 64),
	}
}

func (l *eventStore) add(event *ClientEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	event.Seq = l.base + uint(len(l.entries))
	l.entries = append(l.entries, &EventEntry{
		Start:  event.Seq,
		Offset: 1,
		Event:  event,
	})
}

// readFrom 从全局偏移 start 读取下一个事件，若无新事件返回 nil
func (l *eventStore) readFrom(start uint) *EventEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if start < l.base {
		start = l.base
	}
	idx := int(start - l.base)
	if idx >= len(l.entries) {
		return nil
	}
	return l.entries[idx]
}

// compact 截断全局偏移 minOffset 之前的已消费事件，释放内存。
func (l *eventStore) compact(minOffset uint) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if minOffset <= l.base {
		return
	}
	idx := int(minOffset - l.base)
	if idx > len(l.entries) {
		idx = len(l.entries)
	}
	if idx == 0 {
		return
	}
	// 释放已截断的引用，帮助 GC
	for i := 0; i < idx; i++ {
		l.entries[i] = nil
	}
	l.entries = l.entries[idx:]
	l.base = minOffset
}

// len 返回当前事件总数（base + 未截断条目数）。
func (l *eventStore) len() uint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.base + uint(len(l.entries))
}
