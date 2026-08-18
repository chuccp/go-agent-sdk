package chat

// ==================== 协议元数据 ====================

// ==================== 统一事件接口 ====================
type SourceType string

// 事件来源
const (
	SourceAI     SourceType = "ai"     // AI 产生的事件（LLM 流式输出、工具执行）
	SourceClient SourceType = "client" // 客户端消息状态确认
	SourceSystem SourceType = "system" // 系统事件（错误）
)

// Event 是所有事件的统一接口。
// Type() 标识事件类型，Source() 标识事件来源（AI / 客户端 / 系统）。
type Event interface {
	Type() EventType
	Source() SourceType
}

// ==================== 事件类型常量 ====================

type EventType string

// 系统事件
const (
	EventTypeError EventType = "error"
)

// 客户端推送事件
const (
	EventTypeChunk         EventType = "chunk"
	EventTypeThinking      EventType = "thinking" // AI 思考链增量
	EventTypeDone          EventType = "done"
	EventTypeToolExecution EventType = "tool_execution" // 工具正在执行，携带工具名称和输出

	EventTypeMessageSent     EventType = "message_sent"     // 消息已被立即处理，发送者可直接显示在对话列表
	EventTypeMessageQueued   EventType = "message_queued"   // 消息进入等待队列，发送者应标记为待处理
	EventTypeMessageConsumed EventType = "message_consumed" // 队列中的消息已被消费，发送者应将其显示在对话框
)

// ==================== 客户端推送事件 ====================

// ClientEvent 是面向客户端的推送事件。
// 实现 Event 接口，与协议层事件统一处理。
type ClientEvent struct {
	Seq         uint       `json:"seq"`    // 事件序列号，单调递增，客户端可用于去重和断线续传
	EventSource SourceType `json:"source"` // 事件来源："ai" | "client" | "system"
	EventType   EventType  `json:"type"`
	Content     string     `json:"content,omitempty"`
	Done        bool       `json:"done,omitempty"`
	Message     string     `json:"message,omitempty"`
	Args        string     `json:"args,omitempty"` // 工具入参的展示文本（tool_execution 事件携带）
	MessageId   uint64     `json:"message_id,omitempty"`
	//SessionId   string `json:"session_id"`
}

// Type 实现 Event 接口。
func (e *ClientEvent) Type() EventType { return e.EventType }

// Source 实现 Event 接口。
func (e *ClientEvent) Source() SourceType { return e.EventSource }

// 编译期确认 ClientEvent 实现 Event
var _ Event = (*ClientEvent)(nil)

// -------- 工厂函数 --------

// NewErrorEvent 创建一个错误事件
func NewErrorEvent(message string) *ClientEvent {
	return &ClientEvent{EventSource: SourceSystem, EventType: EventTypeError, Message: message}
}

// NewChunkEvent 创建一个流式文本片段事件
func NewChunkEvent(content string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeChunk, Content: content}
}

// NewThinkingEvent 创建一个 AI 思考链增量事件
func NewThinkingEvent(content string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeThinking, Content: content}
}

// NewDoneEvent 创建一个流结束事件
func NewDoneEvent() *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeDone, Done: true}
}

// NewMessageSentEvent 创建一个消息已被立即处理事件
func NewMessageSentEvent(messageID uint64, msg *RevMessage) *ClientEvent {
	return &ClientEvent{
		EventSource: SourceClient,
		EventType:   EventTypeMessageSent,
		MessageId:   messageID,
		//SessionId:   sessionId,
		Content: msg.Text,
	}
}

// NewMessageQueuedEvent 创建一个消息进入等待队列事件
func NewMessageQueuedEvent(messageID uint64, msg *RevMessage) *ClientEvent {
	return &ClientEvent{
		EventSource: SourceClient,
		EventType:   EventTypeMessageQueued,
		MessageId:   messageID,
		//SessionId:   sessionId,
		Content: msg.Text,
	}
}

// NewMessageConsumedEvent 创建一个队列消息已被消费事件
func NewMessageConsumedEvent(messageID uint64, msg *RevMessage) *ClientEvent {
	return &ClientEvent{
		EventSource: SourceClient,
		EventType:   EventTypeMessageConsumed,
		MessageId:   messageID,
		//SessionId:   sessionId,
		Content: msg.Text,
	}
}

// NewToolExecutionEvent 创建一个工具执行事件，携带工具名称、入参展示文本和执行输出。
func NewToolExecutionEvent(toolName, args, output string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeToolExecution, Content: output, Message: toolName, Args: args}
}
