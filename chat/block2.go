package chat

import "github.com/chuccp/go-agent-sdk/value"

type BlockType string

const (
	TextBlockType       BlockType = "text"
	ThinkingBlockType   BlockType = "thinking"
	ImageBlockType      BlockType = "image"
	ToolUseBlockType    BlockType = "tool_use"
	ToolResultBlockType BlockType = "tool_result"
	StartBlockType      BlockType = "start"
	DeltaBlockType      BlockType = "delta"
	UsageBlockType      BlockType = "usage"
)

type BlockStartType string

type Block2 interface {
	ForContext() bool
}

type Block2s []Block2

type TextBlock2 struct {
	Block2
	Text    string    `json:"text"`
	IsError bool      `json:"is_error,omitempty"`
	Type    BlockType `json:"type"`
}

func (b *TextBlock2) ForContext() bool {
	return true
}
func NewTextBlock2() *TextBlock2 {
	return &TextBlock2{
		Type: TextBlockType,
	}
}
func NewErrorTextBlock2() *TextBlock2 {
	return &TextBlock2{
		Type:    TextBlockType,
		IsError: true,
	}
}

type UsageBlock2 struct {
	Block2
}

func (b *UsageBlock2) ForContext() bool {
	return false
}

type ThinkingBlock2 struct {
	Block2
	Thinking string    `json:"thinking,omitempty"`
	Type     BlockType `json:"type"`
}

func (b *ThinkingBlock2) ForContext() bool {
	return false
}

type ImageBlock2 struct {
	Block2
	Source *ImageSource `json:"source,omitempty"`
	Type   BlockType    `json:"type"`
}

func (b *ImageBlock2) ForContext() bool {
	return true
}

type ToolUseBlock2 struct {
	Block2
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Input *value.Object `json:"input,omitempty"`
	Type  BlockType     `json:"type"`
}

func (b *ToolUseBlock2) ForContext() bool {
	return true
}

type ToolResultBlock2 struct {
	ToolUseID string    `json:"tool_use_id"`
	Content   []Block2  `json:"content,omitempty"` // string 或 []Block
	Type      BlockType `json:"type"`
}

func (b *ToolResultBlock2) ForContext() bool {
	return true
}

type StartBlock2 struct {
	Block2
	Type  BlockType `json:"type"`
	Block Block2    `json:"block"`
}

func (b *StartBlock2) ForContext() bool {
	return false
}

func NewStartBlock2(block Block2) *StartBlock2 {
	return &StartBlock2{
		Type:  StartBlockType,
		Block: block,
	}
}

type DeltaBlock2 struct {
	Block2
	Type    BlockType `json:"type"`
	Content string    `json:"content"`
}

func (b *DeltaBlock2) ForContext() bool {
	return false
}
