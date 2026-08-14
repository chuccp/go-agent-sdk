package chat

import (
	"bytes"
	"encoding/json"
	"fmt"

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

// ==================== JSON 序列化 ====================

// blockEnvelope 用于 JSON 序列化时携带 type 字段。
// 各 block 类型仅携带自身相关字段，通过 Type 标识类型，与 Anthropic Messages API 对齐。
type blockEnvelope struct {
	Type      ContentType  `json:"type"`
	Text      string       `json:"text,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Source    *ImageSource `json:"source,omitempty"`
	ID        string       `json:"id,omitempty"`
	Name      string       `json:"name,omitempty"`
	Input     any          `json:"input,omitempty"`
	ToolUseID string       `json:"tool_use_id,omitempty"`
	Content   any          `json:"content,omitempty"`
}

// MarshalBlock 将 Block 序列化为 JSON（携带 type 字段）。
func MarshalBlock(b Block) ([]byte, error) {
	var env blockEnvelope
	switch v := b.(type) {
	case *TextBlock:
		env = blockEnvelope{Type: ContentTypeText, Text: v.Text}
	case *ThinkingBlock:
		env = blockEnvelope{Type: ContentTypeThinking, Thinking: v.Thinking}
	case *ImageBlock:
		env = blockEnvelope{Type: ContentTypeImage, Source: v.Source}
	case *ToolUseBlock:
		env = blockEnvelope{Type: ContentTypeToolUse, ID: v.ID, Name: v.Name, Input: v.Input}
	case *ToolResultBlock:
		env = blockEnvelope{Type: ContentTypeToolResult, ToolUseID: v.ToolUseID, Content: v.Content}
	default:
		return nil, fmt.Errorf("unknown block type: %T", b)
	}
	return json.Marshal(env)
}

// Blocks 是 []Block 的别名，支持 JSON 序列化/反序列化。
type Blocks []Block

// MarshalJSON 将 Blocks 序列化为 JSON，每个 block 携带 type 字段。
// nil 序列化为 null，空切片序列化为 []。
func (bs Blocks) MarshalJSON() ([]byte, error) {
	if bs == nil {
		return []byte("null"), nil
	}
	raw := make([]json.RawMessage, 0, len(bs))
	for _, b := range bs {
		data, err := MarshalBlock(b)
		if err != nil {
			return nil, err
		}
		raw = append(raw, data)
	}
	return json.Marshal(raw)
}

// ==================== JSON 反序列化 ====================

// blockEnvelopeIn 用于 JSON 反序列化：先取 type 分发到具体 block，
// 其中 Input（JSON 对象）/Content（字符串或 block 数组）为多态字段，按 RawMessage 保留后再解析。
type blockEnvelopeIn struct {
	Type      ContentType     `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Source    *ImageSource    `json:"source"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// UnmarshalBlock 从 JSON 反序列化为具体的 Block 类型。
func UnmarshalBlock(data []byte) (Block, error) {
	var env blockEnvelopeIn
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	switch env.Type {
	case ContentTypeText:
		return &TextBlock{Text: env.Text}, nil
	case ContentTypeThinking:
		return &ThinkingBlock{Thinking: env.Thinking}, nil
	case ContentTypeImage:
		return &ImageBlock{Source: env.Source}, nil
	case ContentTypeToolUse:
		input := value.NewObject()
		if len(env.Input) > 0 {
			if err := input.PutJson(env.Input); err != nil {
				return nil, fmt.Errorf("tool_use input 解析失败: %w", err)
			}
		}
		return &ToolUseBlock{ID: env.ID, Name: env.Name, Input: input}, nil
	case ContentTypeToolResult:
		content, err := decodeToolResultContent(env.Content)
		if err != nil {
			return nil, err
		}
		return &ToolResultBlock{ToolUseID: env.ToolUseID, Content: content}, nil
	default:
		return nil, fmt.Errorf("unknown content block type: %s", env.Type)
	}
}

// decodeToolResultContent 解析 tool_result 的 content 字段：
// 字符串按原样返回，JSON 数组按 Blocks 反序列化，null/缺省返回 nil。
func decodeToolResultContent(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return s, nil
	}
	var blocks Blocks
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// UnmarshalJSON 将 JSON 数组反序列化为 Blocks；null 反序列化为 nil。
func (bs *Blocks) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*bs = nil
		return nil
	}
	var rawList []json.RawMessage
	if err := json.Unmarshal(data, &rawList); err != nil {
		return err
	}
	result := make(Blocks, 0, len(rawList))
	for _, raw := range rawList {
		b, err := UnmarshalBlock(raw)
		if err != nil {
			return err
		}
		result = append(result, b)
	}
	*bs = result
	return nil
}

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
