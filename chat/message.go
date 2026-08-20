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

// ==================== Message ====================

// Role 是消息发送方
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message 是 messages 数组中的一条消息。
// Content 为 Block 接口数组，支持多态 JSON 序列化。
// Start/Offset 是 SDK 内部的消息序号记账字段，不出现在发给模型的 JSON 中。
type Message struct {
	Start   uint64 `json:"-"`
	Offset  uint64 `json:"-"`
	Role    Role   `json:"role"`    // "user" | "assistant"
	Content Blocks `json:"content"` // content block 数组
}

// NewTextMessage 便捷构造：生成一条纯文本 user 消息。
func NewTextMessage(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: Blocks{NewFullTextBlock(text)},
	}
}

// Text 便捷构造（兼容旧调用）。
func Text(text string) Message {
	return NewTextMessage(text)
}

// RevMessage 专门接收用户输入，由调用方构造后传入 SDK。
// 与 Message（SDK 内部 / 发给模型的协议结构）职责分离。
type RevMessage struct {
	//TODO FilesIds 暂时不处理
	FilesIds []string `json:"files_ids"`
	Text     string   `json:"text"`
}

// ToMessage 将用户输入转换为内部协议 Message。
func (r *RevMessage) ToMessage() Message {
	return Message{
		Role:    RoleUser,
		Content: Blocks{NewFullTextBlock(r.Text)},
	}
}
