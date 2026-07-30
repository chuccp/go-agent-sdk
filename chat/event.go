package chat

import "sync"

// ==================== 统一事件接口 ====================

// 事件来源
const (
	SourceAI     = "ai"     // AI 产生的事件（LLM 流式输出、工具执行）
	SourceClient = "client" // 客户端消息状态确认
	SourceSystem = "system" // 系统事件（错误）
)

// Event 是所有事件的统一接口。
// Type() 标识事件类型，Source() 标识事件来源（AI / 客户端 / 系统）。
type Event interface {
	Type() string
	Source() string
}

// ==================== 事件类型常量 ====================

// LLM 协议层事件（流式 SSE）
const (
	EventTypeMessageStart      = "message_start"
	EventTypeContentBlockStart = "content_block_start"
	EventTypeContentBlockDelta = "content_block_delta"
	EventTypeContentBlockStop  = "content_block_stop"
	EventTypeMessageDelta      = "message_delta"
	EventTypeMessageStop       = "message_stop"
	EventTypeError             = "error"
)

// 客户端推送事件
const (
	EventTypeChunk           = "chunk"
	EventTypeThinking        = "thinking"        // AI 思考链增量
	EventTypeDone            = "done"
	EventTypeToolExecution   = "tool_execution"   // 工具正在执行，携带工具名称和输出
	EventTypeMessageSent     = "message_sent"     // 消息已被立即处理，发送者可直接显示在对话列表
	EventTypeMessageQueued   = "message_queued"   // 消息进入等待队列，发送者应标记为待处理
	EventTypeMessageConsumed = "message_consumed" // 队列中的消息已被消费，发送者应将其显示在对话框
)

// ==================== LLM 协议层事件 ====================

// MessageStartEvent 在流开始时触发，携带响应的元数据。
type MessageStartEvent struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Role  string `json:"role"`
	Usage Usage  `json:"usage"`
}

func (e *MessageStartEvent) Type() string   { return EventTypeMessageStart }
func (e *MessageStartEvent) Source() string { return SourceAI }

// ContentBlockStartEvent 在一个新的 content block 开始时触发。
// 对于 tool_use block，可从 ContentBlock 中读取 ID 和 Name。
type ContentBlockStartEvent struct {
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

func (e *ContentBlockStartEvent) Type() string   { return EventTypeContentBlockStart }
func (e *ContentBlockStartEvent) Source() string { return SourceAI }

// ContentBlockDeltaEvent 携带一段增量内容（文本或工具参数 JSON）。
type ContentBlockDeltaEvent struct {
	Index int          `json:"index"`
	Delta ContentDelta `json:"delta"`
}

func (e *ContentBlockDeltaEvent) Type() string   { return EventTypeContentBlockDelta }
func (e *ContentBlockDeltaEvent) Source() string { return SourceAI }

// ContentDelta 是一次增量更新的内容。
type ContentDelta struct {
	Type        string `json:"type"`         // "text_delta" | "thinking_delta" | "input_json_delta"
	Text        string `json:"text"`         // text_delta 时的文本增量
	Thinking    string `json:"thinking"`     // thinking_delta 时的思考链增量
	PartialJSON string `json:"partial_json"` // input_json_delta 时的 JSON 片段
}

// ContentBlockStopEvent 在一个 content block 完成时触发。
type ContentBlockStopEvent struct {
	Index int `json:"index"`
}

func (e *ContentBlockStopEvent) Type() string   { return EventTypeContentBlockStop }
func (e *ContentBlockStopEvent) Source() string { return SourceAI }

// MessageDeltaEvent 携带停止原因和输出 token 用量（在 message_stop 之前触发）。
type MessageDeltaEvent struct {
	StopReason StopReason `json:"stop_reason"`
	Usage      Usage      `json:"usage"`
}

func (e *MessageDeltaEvent) Type() string   { return EventTypeMessageDelta }
func (e *MessageDeltaEvent) Source() string { return SourceAI }

// MessageStopEvent 表示整个流正常结束。
type MessageStopEvent struct{}

func (e *MessageStopEvent) Type() string   { return EventTypeMessageStop }
func (e *MessageStopEvent) Source() string { return SourceAI }

// ErrorEvent 携带流处理过程中发生的错误。
type ErrorEvent struct {
	Err error
}

func (e *ErrorEvent) Type() string   { return EventTypeError }
func (e *ErrorEvent) Source() string { return SourceSystem }
func (e *ErrorEvent) Error() string  { return e.Err.Error() }

// ==================== 客户端推送事件 ====================

// ClientEvent 是面向客户端的推送事件。
// 实现 Event 接口，与协议层事件统一处理。
type ClientEvent struct {
	Seq            uint   `json:"seq"`    // 事件序列号，单调递增，客户端可用于去重和断线续传
	EventSource    string `json:"source"` // 事件来源："ai" | "client" | "system"
	EventType      string `json:"type"`
	Content        string `json:"content,omitempty"`
	Done           bool   `json:"done,omitempty"`
	Message        string `json:"message,omitempty"`
	MessageID      uint   `json:"message_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// Type 实现 Event 接口。
func (e *ClientEvent) Type() string { return e.EventType }

// Source 实现 Event 接口。
func (e *ClientEvent) Source() string { return e.EventSource }

// 编译期确认 ClientEvent 实现 Event
var _ Event = (*ClientEvent)(nil)

// -------- 工厂函数 --------

// NewErrorEvent 创建一个错误事件
func NewErrorEvent(message string) *ClientEvent {
	return &ClientEvent{EventSource: SourceSystem, EventType: EventTypeError, Message: message}
}

// NewChunkEvent 创建一个流式文本片段事件
func NewChunkEvent(content, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeChunk, Content: content, ConversationID: conversationID}
}

// NewThinkingEvent 创建一个 AI 思考链增量事件
func NewThinkingEvent(content, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeThinking, Content: content, ConversationID: conversationID}
}

// NewDoneEvent 创建一个流结束事件
func NewDoneEvent(conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeDone, Done: true, ConversationID: conversationID}
}

// NewMessageSentEvent 创建一个消息已被立即处理事件
func NewMessageSentEvent(messageID uint, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceClient, EventType: EventTypeMessageSent, MessageID: messageID, ConversationID: conversationID}
}

// NewMessageQueuedEvent 创建一个消息进入等待队列事件
func NewMessageQueuedEvent(messageID uint, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceClient, EventType: EventTypeMessageQueued, MessageID: messageID, ConversationID: conversationID}
}

// NewMessageConsumedEvent 创建一个队列消息已被消费事件
func NewMessageConsumedEvent(messageID uint, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceClient, EventType: EventTypeMessageConsumed, MessageID: messageID, ConversationID: conversationID}
}

// NewToolExecutionEvent 创建一个工具执行事件，携带工具名称和执行输出。
func NewToolExecutionEvent(toolName, output, conversationID string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeToolExecution, Content: output, Message: toolName, ConversationID: conversationID}
}

// ==================== 事件存储 ====================

// EventEntry 单个事件条目，含偏移量信息
type EventEntry struct {
	Start  uint
	Offset uint
	Event  *ClientEvent
}

// EventStore 追加式事件日志，支持截断已消费的事件以防止无界增长。
type EventStore struct {
	mu      sync.RWMutex
	entries []*EventEntry
	base    uint // entries[0] 对应的全局偏移
}

func NewEventStore() *EventStore {
	return &EventStore{
		entries: make([]*EventEntry, 0, 64),
	}
}

func (l *EventStore) Add(event *ClientEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	event.Seq = l.base + uint(len(l.entries))
	l.entries = append(l.entries, &EventEntry{
		Start:  event.Seq,
		Offset: 1,
		Event:  event,
	})
}

// ReadFrom 从全局偏移 start 读取下一个事件，若无新事件返回 nil
func (l *EventStore) ReadFrom(start uint) *EventEntry {
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

// Compact 截断全局偏移 minOffset 之前的已消费事件，释放内存。
func (l *EventStore) Compact(minOffset uint) {
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

// Len 返回当前事件总数（base + 未截断条目数）。
func (l *EventStore) Len() uint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.base + uint(len(l.entries))
}
