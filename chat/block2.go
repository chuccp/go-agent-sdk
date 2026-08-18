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

type UseDeltaBlock interface {
	Block2
	ParesStream(stream *value.Stream)
}

type Block2 interface {
	ForContext() bool
}

type Block2s []Block2

type TextBlock2 struct {
	UseDeltaBlock
	Text    string    `json:"text"`
	IsError bool      `json:"is_error,omitempty"`
	Type    BlockType `json:"type"`
}

func (b *TextBlock2) ForContext() bool {
	return true
}
func (b *TextBlock2) ParesStream(stream *value.Stream) {
	b.Text = stream.String()
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
func NewErrorFullTextBlock2(text string) *TextBlock2 {
	return &TextBlock2{
		Type:    TextBlockType,
		IsError: true,
		Text:    text,
	}
}

type UsageBlock2 struct {
	Block2
	usage *Usage
}

func (b *UsageBlock2) ForContext() bool {
	return false
}
func NewUsageBlock2(usage *Usage) *UsageBlock2 {
	return &UsageBlock2{
		usage: usage,
	}
}

type ThinkingBlock2 struct {
	UseDeltaBlock
	Thinking string    `json:"thinking,omitempty"`
	Type     BlockType `json:"type"`
}

func (b *ThinkingBlock2) ForContext() bool {
	return false
}
func (b *ThinkingBlock2) ParesStream(stream *value.Stream) {
	b.Thinking = stream.String()
}
func NewThinkingBlock2() *ThinkingBlock2 {
	return &ThinkingBlock2{
		Type: ThinkingBlockType,
	}
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
	UseDeltaBlock
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Input *value.Object `json:"input,omitempty"`
	Type  BlockType     `json:"type"`
}

func (b *ToolUseBlock2) ForContext() bool {
	return true
}
func (b *ToolUseBlock2) ParesStream(stream *value.Stream) {
	b.Input, _ = value.NewObjectFromJson(stream.ToJSON())
}
func NewToolUseBlock2(id string, name string) *ToolUseBlock2 {
	return &ToolUseBlock2{
		ID:   id,
		Name: name,
		Type: ToolUseBlockType,
	}
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
func NewDeltaBlock2(content string) *DeltaBlock2 {
	return &DeltaBlock2{
		Type:    DeltaBlockType,
		Content: content,
	}
}
