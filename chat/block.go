package chat

import (
	"github.com/chuccp/go-agent-sdk/value"
)

// ContentType 是 content block 的类型标识
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeThinking   ContentType = "thinking"
	ContentTypeImage      ContentType = "image"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

// Block 是所有 content block 的统一接口。
// 每种 block 类型只携带自身相关字段，通过 Type() 标识类型。
type Block interface {
	Type() ContentType
}

// ==================== 具体 Block 类型 ====================

// TextBlock 纯文本内容
type TextBlock struct {
	Text string `json:"text"`
}

func (b *TextBlock) Type() ContentType { return ContentTypeText }

// ThinkingBlock 思考链内容
type ThinkingBlock struct {
	Thinking string `json:"thinking"`
}

func (b *ThinkingBlock) Type() ContentType { return ContentTypeThinking }

// ImageBlock 图片内容（base64 内联）
type ImageBlock struct {
	Source *ImageSource `json:"source"`
}

func (b *ImageBlock) Type() ContentType { return ContentTypeImage }

// ImageSource 描述图片内容
type ImageSource struct {
	SourceType string `json:"type"`       // "base64"
	MediaType  string `json:"media_type"` // "image/png" | "image/jpeg" | "image/gif" | "image/webp"
	Data       string `json:"data"`       // base64 编码的图片数据
}

// ToolUseBlock 工具调用
type ToolUseBlock struct {
	ID    string
	Name  string
	Input *value.Object
}

func (b *ToolUseBlock) Type() ContentType { return ContentTypeToolUse }

// ToolResultBlock 工具执行结果
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"` // string 或 []Block
}

func (b *ToolResultBlock) Type() ContentType { return ContentTypeToolResult }

// Blocks 是 []Block 的别名，支持 JSON 序列化/反序列化。
type Blocks []Block

func NewTextBlock(text string) *TextBlock {
	return &TextBlock{Text: text}
}

func NewThinkingBlock(thinking string) *ThinkingBlock {
	return &ThinkingBlock{Thinking: thinking}
}

func NewToolUseBlock(id, name string, input *value.Object) *ToolUseBlock {
	return &ToolUseBlock{ID: id, Name: name, Input: input}
}

func NewToolResultBlock(toolUseID string, content any) *ToolResultBlock {
	return &ToolResultBlock{ToolUseID: toolUseID, Content: content}
}
