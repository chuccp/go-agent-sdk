package chat

import "github.com/chuccp/go-agent-sdk/util"

// ==================== Response (流式) ====================

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

// -------- 事件类型常量 --------

const (
	EventTypeMessageStart      = "message_start"
	EventTypeContentBlockStart = "content_block_start"
	EventTypeContentBlockDelta = "content_block_delta"
	EventTypeContentBlockStop  = "content_block_stop"
	EventTypeMessageDelta      = "message_delta"
	EventTypeMessageStop       = "message_stop"
	EventTypeError             = "error"
)

// -------- 具体事件类型 --------

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
	Type        string `json:"type"`         // "text_delta" | "input_json_delta"
	Text        string `json:"text"`         // text_delta 时的文本增量
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

// -------- StreamWriter --------

// StreamWriter 是流式事件的生产方接口（类似 http.ResponseWriter）。
// ChatService 实现方通过此接口写入事件，无需关心底层队列细节。
type StreamWriter interface {
	Write(event Event) error
	WriteError(err error)
	Close()
}

// -------- Response --------

// Response 是 ChatWithStream 返回的流式响应体。
// 调用方通过循环调用 ReadEvent() 消费事件，nil 表示流结束。
type Response struct {
	events *util.Queue[Event]
	closed bool
}

// NewResponse 创建一个流式 Response。生产方通过 Write 写入事件，完成后调用 Close。
func NewResponse() *Response {
	return &Response{events: util.NewQueue[Event]()}
}

func (r *Response) Write(event Event) error {
	return r.events.Offer(event)
}

func (r *Response) WriteError(err error) {
	r.events.Offer(&ErrorEvent{Err: err})
	r.events.Close()
}

// Close 关闭事件队列，ReadEvent 将在消费完剩余事件后返回 nil。
func (r *Response) Close() {
	r.events.Close()
}

func (r *Response) ReadEvent() Event {
	if r.closed || r.events == nil {
		return nil
	}
	evt, ok := r.events.Dequeue()
	if !ok {
		r.closed = true
		return nil
	}
	return evt
}

// -------- 便捷聚合方法 --------

// Collect 消费所有事件并聚合成文本和工具调用结果。
// 这是一个便捷方法，适用于不需要逐事件处理的简单场景。
func (r *Response) Collect() (text string, toolCalls []ContentBlock) {
	for evt := r.ReadEvent(); evt != nil; evt = r.ReadEvent() {
		switch e := evt.(type) {
		case *ContentBlockDeltaEvent:
			if e.Delta.Type == "text_delta" {
				text += e.Delta.Text
			}
		case *ContentBlockStopEvent:
			// tool_use block 在 start/stop 之间通过 delta 累积，
			// 外部可配合 ContentBlockStartEvent 自行维护状态。
		}
	}
	return
}
