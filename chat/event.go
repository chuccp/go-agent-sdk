package chat

// ==================== 协议元数据 ====================

// StopReason 是模型停止生成的原因
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"      // 自然结束
	StopReasonMaxTokens StopReason = "max_tokens"    // 达到 max_tokens 上限
	StopReasonToolUse   StopReason = "tool_use"      // 需要调用工具
	StopReasonStopSeq   StopReason = "stop_sequence" // 命中停止序列
)

// Usage 记录本次请求的 token 消耗。
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

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
	EventTypeThinking        = "thinking" // AI 思考链增量
	EventTypeDone            = "done"
	EventTypeToolExecution   = "tool_execution"   // 工具正在执行，携带工具名称和输出
	EventTypeAskUser         = "ask_user"         // LLM 向用户提问，需要用户交互后继续
	EventTypeMessageSent     = "message_sent"     // 消息已被立即处理，发送者可直接显示在对话列表
	EventTypeMessageQueued   = "message_queued"   // 消息进入等待队列，发送者应标记为待处理
	EventTypeMessageConsumed = "message_consumed" // 队列中的消息已被消费，发送者应将其显示在对话框
	EventTypeFlowProgress    = "flow_progress"    // flow 步骤进度，content 为 JSON（flowId/stepId/phase/output）
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
// 对于 tool_use block，可从 Block 中读取 ID 和 Name。
type ContentBlockStartEvent struct {
	Index        int   `json:"index"`
	ContentBlock Block `json:"content_block"`
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
	Seq         uint   `json:"seq"`    // 事件序列号，单调递增，客户端可用于去重和断线续传
	EventSource string `json:"source"` // 事件来源："ai" | "client" | "system"
	EventType   string `json:"type"`
	Content     string `json:"content,omitempty"`
	Done        bool   `json:"done,omitempty"`
	Message     string `json:"message,omitempty"`
	Args        string `json:"args,omitempty"` // 工具入参的展示文本（tool_execution 事件携带）
	MessageId   uint64 `json:"message_id,omitempty"`
	//SessionId   string `json:"session_id"`
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
func NewChunkEvent(content, sessionId string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeChunk, Content: content}
}

// NewThinkingEvent 创建一个 AI 思考链增量事件
func NewThinkingEvent(content, sessionId string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeThinking, Content: content}
}

// NewDoneEvent 创建一个流结束事件
func NewDoneEvent(sessionId string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeDone, Done: true}
}

// NewMessageSentEvent 创建一个消息已被立即处理事件
func NewMessageSentEvent(messageID uint64, sessionId string, msg *RevMessage) *ClientEvent {
	return &ClientEvent{
		EventSource: SourceClient,
		EventType:   EventTypeMessageSent,
		MessageId:   messageID,
		//SessionId:   sessionId,
		Content: msg.Text,
	}
}

// NewMessageQueuedEvent 创建一个消息进入等待队列事件
func NewMessageQueuedEvent(messageID uint64, sessionId string, msg *RevMessage) *ClientEvent {
	return &ClientEvent{
		EventSource: SourceClient,
		EventType:   EventTypeMessageQueued,
		MessageId:   messageID,
		//SessionId:   sessionId,
		Content: msg.Text,
	}
}

// NewMessageConsumedEvent 创建一个队列消息已被消费事件
func NewMessageConsumedEvent(messageID uint64, sessionId string, msg *RevMessage) *ClientEvent {
	return &ClientEvent{
		EventSource: SourceClient,
		EventType:   EventTypeMessageConsumed,
		MessageId:   messageID,
		//SessionId:   sessionId,
		Content: msg.Text,
	}
}

// NewToolExecutionEvent 创建一个工具执行事件，携带工具名称、入参展示文本和执行输出。
func NewToolExecutionEvent(toolName, args, output, sessionId string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeToolExecution, Content: output, Message: toolName, Args: args}
}

// NewAskUserEvent 创建一个用户提问事件，content 为问题列表的 JSON。
func NewAskUserEvent(content, sessionId string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeAskUser, Content: content}
}

// NewFlowProgressEvent 创建一个 flow 步骤进度事件，content 为 JSON：
// {"flowId", "stepId", "phase": "start"|"item"|"done"|"error", "output"}。
func NewFlowProgressEvent(content, sessionId string) *ClientEvent {
	return &ClientEvent{EventSource: SourceAI, EventType: EventTypeFlowProgress, Content: content}
}
