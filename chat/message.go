package chat

// 下列类型按 Anthropic Messages API (https://docs.anthropic.com/en/api/messages) 标准定义，
// 用于构造发给模型的请求。字段/JSON 键与官方 schema 一一对应，便于序列化后直接 POST。

// ==================== Tool ====================

// ToolFunction 是发给模型的工具定义。模型据此生成 tool_use content block。
type ToolFunction struct {
	Name          string           `json:"name"`                     // 工具名称（唯一标识）
	Description   string           `json:"description"`              // 工具功能描述（模型据此决定是否调用）
	InputSchema   map[string]any   `json:"input_schema"`             // 输入参数的 JSON Schema
	InputExamples []map[string]any `json:"input_examples,omitempty"` // 调用示例（可选）
}

// ==================== Content block ====================

// ContentType 是 content block 的类型标识
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeImage      ContentType = "image"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

// ContentBlock 是 message.content 数组中的一个元素。
// 每种类型只使用对应的字段，其余为零值（不序列化）。
type ContentBlock struct {
	Type     ContentType `json:"type"`                  // "text" | "image" | "tool_use" | "tool_result"
	Text     string      `json:"text,omitempty"`        // text 类型：文本内容
	Input    any         `json:"input,omitempty"`       // tool_use 类型：工具入参（解析后的对象）
	ID       string      `json:"id,omitempty"`          // tool_use 类型：调用 ID；tool_result 类型：对应的 tool_use ID
	Name     string      `json:"name,omitempty"`        // tool_use 类型：工具名称
	Thinking string      `json:"thinking"`              // DeepSeek 兼容：assistant content block 必须带此字段
	ToolUseID string     `json:"tool_use_id,omitempty"` // tool_result 类型：对应的 tool_use block 的 ID
	Content  any         `json:"content,omitempty"`     // tool_result 类型：结果内容（string 或 []ContentBlock）

	// image 类型字段
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource 描述图片内容（当前仅支持 base64 内联）。
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png" | "image/jpeg" | "image/gif" | "image/webp"
	Data      string `json:"data"`       // base64 编码的图片数据
}

// ==================== Message ====================

// Role 是消息发送方
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message 是 messages 数组中的一条消息。
// Content 为 block 数组（与 API 一致）；为兼容纯文本场景，Text 字段在内部可转换为单 text block。
type Message struct {
	Role    Role           `json:"role"`    // "user" | "assistant"
	Content []ContentBlock `json:"content"` // content block 数组
}

// Text 便捷构造：生成一条纯文本 user 消息。
func Text(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []ContentBlock{{Type: ContentTypeText, Text: text}},
	}
}

// ==================== Request ====================

// Messages 是发给 Claude Messages API 的完整请求体。
type Messages struct {
	Model     string    `json:"model"`      // 模型 ID，如 "claude-opus-4-8"
	MaxTokens int       `json:"max_tokens"` // 最大生成 token 数
	Messages  []Message `json:"messages"`   // 对话历史（user/assistant 交替）

	// 可选字段
	System        string         `json:"system,omitempty"`         // 系统提示（独立于 messages）
	Tools         []ToolFunction `json:"tools,omitempty"`          // 可用工具列表
	Stream        bool           `json:"stream,omitempty"`         // 是否流式返回
	Temperature   *float64       `json:"temperature,omitempty"`    // 采样温度 (0,1]
	TopP          *float64       `json:"top_p,omitempty"`          // nucleus 采样
	TopK          *int           `json:"top_k,omitempty"`          // top-k 采样
	StopSequences []string       `json:"stop_sequences,omitempty"` // 停止序列
	Metadata      map[string]any `json:"metadata,omitempty"`       // 自定义元数据（不透传给模型）
}

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

// Event 是流式响应中单个事件的接口。每个事件通过 Type() 标识其类型。
type Event interface {
	Type() string
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

func (e *MessageStartEvent) Type() string { return EventTypeMessageStart }

// ContentBlockStartEvent 在一个新的 content block 开始时触发。
// 对于 tool_use block，可从 ContentBlock 中读取 ID 和 Name。
type ContentBlockStartEvent struct {
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

func (e *ContentBlockStartEvent) Type() string { return EventTypeContentBlockStart }

// ContentBlockDeltaEvent 携带一段增量内容（文本或工具参数 JSON）。
type ContentBlockDeltaEvent struct {
	Index int          `json:"index"`
	Delta ContentDelta `json:"delta"`
}

func (e *ContentBlockDeltaEvent) Type() string { return EventTypeContentBlockDelta }

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

func (e *ContentBlockStopEvent) Type() string { return EventTypeContentBlockStop }

// MessageDeltaEvent 携带停止原因和输出 token 用量（在 message_stop 之前触发）。
type MessageDeltaEvent struct {
	StopReason StopReason `json:"stop_reason"`
	Usage      Usage      `json:"usage"`
}

func (e *MessageDeltaEvent) Type() string { return EventTypeMessageDelta }

// MessageStopEvent 表示整个流正常结束。
type MessageStopEvent struct{}

func (e *MessageStopEvent) Type() string { return EventTypeMessageStop }

// ErrorEvent 携带流处理过程中发生的错误。
type ErrorEvent struct {
	Err error
}

func (e *ErrorEvent) Type() string { return EventTypeError }
func (e *ErrorEvent) Error() string { return e.Err.Error() }

// -------- Response --------

// Response 是 ChatWithStream 返回的流式响应体。
// 调用方通过循环调用 ReadEvent() 消费事件，nil 表示流结束。
type Response struct {
	events <-chan Event
	closed bool
}

// NewResponse 创建一个流式 Response。传入的 channel 由调用方负责关闭。
func NewResponse(events <-chan Event) *Response {
	return &Response{events: events}
}

// ReadEvent 从流中读取下一个事件。若流已结束或已关闭则返回 nil。
// 调用方应在循环中使用，直到收到 nil：
//
//	for evt := resp.ReadEvent(); evt != nil; evt = resp.ReadEvent() {
//	    switch e := evt.(type) {
//	    case *ContentBlockDeltaEvent:
//	        fmt.Print(e.Delta.Text)
//	    case *MessageStopEvent:
//	        // 流结束
//	    }
//	}
func (r *Response) ReadEvent() Event {
	if r.closed || r.events == nil {
		return nil
	}
	evt, ok := <-r.events
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
