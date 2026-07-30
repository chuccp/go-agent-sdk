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
	ContentTypeThinking   ContentType = "thinking"
	ContentTypeImage      ContentType = "image"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

// ContentBlock 是 message.content 数组中的一个元素。
// 每种类型只使用对应的字段，其余为零值（不序列化）。
type ContentBlock struct {
	Type      ContentType `json:"type"`                  // "text" | "image" | "tool_use" | "tool_result"
	Text      string      `json:"text,omitempty"`        // text 类型：文本内容
	Input     any         `json:"input,omitempty"`       // tool_use 类型：工具入参（解析后的对象）
	ID        string      `json:"id,omitempty"`          // tool_use 类型：调用 ID；tool_result 类型：对应的 tool_use ID
	Name      string      `json:"name,omitempty"`        // tool_use 类型：工具名称
	Thinking  string      `json:"thinking,omitempty"`    // DeepSeek 兼容：assistant content block 可带此字段
	ToolUseID string      `json:"tool_use_id,omitempty"` // tool_result 类型：对应的 tool_use block 的 ID
	Content   any         `json:"content,omitempty"`     // tool_result 类型：结果内容（string 或 []ContentBlock）

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
