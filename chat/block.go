package chat

import (
	"encoding/json"

	"github.com/chuccp/go-agent-sdk/value"
)

// textBlockMarshal 用于 JSON 序列化时排除嵌入的 UseDeltaBlock 接口字段。
type textBlockMarshal struct {
	Type     BlockType `json:"type"`
	Text     string    `json:"text"`
	TextType TextType  `json:"text_type,omitempty"`
}

type BlockType string

const (
	TextBlockType          BlockType = "text"
	ThinkingBlockType      BlockType = "thinking"
	ImageBlockType         BlockType = "image"
	ToolUseBlockType       BlockType = "tool_use"
	ToolResultBlockType    BlockType = "tool_result"
	StartBlockType         BlockType = "start"
	DeltaBlockType         BlockType = "delta"
	DoneBlockType          BlockType = "done"
	UsageBlockType         BlockType = "usage"
	UserBlockType          BlockType = "User"
	ErrorBlockType         BlockType = "error"
	ToolExecutionBlockType BlockType = "tool_execution"
)

type ErrorBlock struct {
	text string
}

func (b *ErrorBlock) ForContext() bool {
	return false
}
func (b *ErrorBlock) Text() string { return b.text }
func (b *ErrorBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type    BlockType `json:"type"`
		Message string    `json:"message"`
	}{Type: ErrorBlockType, Message: b.text})
}
func NewErrorBlock(text string) *ErrorBlock {
	return &ErrorBlock{
		text: text,
	}
}

type UseDeltaBlock interface {
	Block
	ParesStream(stream *value.Stream)
}

type Block interface {
	ForContext() bool
}

type Blocks []Block

type TextType string

const (
	ErrorTextType    TextType = "error"
	CMDTextType      TextType = "cmd"
	FlowProgressType TextType = "flow_progress"
)

type TextBlock struct {
	UseDeltaBlock
	Text     string    `json:"text"`
	Type     BlockType `json:"type"`
	TextType TextType  `json:"text_type"`
}

func (b *TextBlock) ForContext() bool {
	return true
}
func (b *TextBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(textBlockMarshal{Type: b.Type, Text: b.Text, TextType: b.TextType})
}
func (b *TextBlock) ParesStream(stream *value.Stream) {
	b.Text = stream.String()
}
func NewTextBlock() *TextBlock {
	return &TextBlock{
		Type: TextBlockType,
	}
}
func NewErrorTextBlock() *TextBlock {
	return &TextBlock{
		Type:     TextBlockType,
		TextType: ErrorTextType,
	}
}
func NewErrorFullTextBlock(text string) *TextBlock {
	return &TextBlock{
		Type:     TextBlockType,
		TextType: ErrorTextType,
		Text:     text,
	}
}
func NewFullTextBlock(text string) *TextBlock {
	return &TextBlock{
		Type: TextBlockType,
		Text: text,
	}
}
func NewFullTextTypeBlock(text string, textType TextType) *TextBlock {
	return &TextBlock{
		Type:     TextBlockType,
		Text:     text,
		TextType: textType,
	}
}

type UsageBlock struct {
	Block
	usage *Usage
}

func (b *UsageBlock) ForContext() bool {
	return false
}
func NewUsageBlock(usage *Usage) *UsageBlock {
	return &UsageBlock{
		usage: usage,
	}
}

type ThinkingBlock struct {
	UseDeltaBlock
	Thinking string    `json:"thinking,omitempty"`
	Type     BlockType `json:"type"`
}

func (b *ThinkingBlock) ForContext() bool {
	return false
}
func (b *ThinkingBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Thinking string    `json:"thinking,omitempty"`
		Type     BlockType `json:"type"`
	}{Thinking: b.Thinking, Type: b.Type})
}
func (b *ThinkingBlock) ParesStream(stream *value.Stream) {
	b.Thinking = stream.String()
}
func NewThinkingBlock() *ThinkingBlock {
	return &ThinkingBlock{
		Type: ThinkingBlockType,
	}
}

type ImageSource struct {
	SourceType string `json:"type"`       // "base64"
	MediaType  string `json:"media_type"` // "image/png" | "image/jpeg" | "image/gif" | "image/webp"
	Data       string `json:"data"`       // base64 编码的图片数据
}

type ImageBlock struct {
	Block
	Source *ImageSource `json:"source,omitempty"`
	Type   BlockType    `json:"type"`
}

func (b *ImageBlock) ForContext() bool {
	return true
}
func (b *ImageBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Source *ImageSource `json:"source,omitempty"`
		Type   BlockType    `json:"type"`
	}{Source: b.Source, Type: b.Type})
}

type ToolUseBlock struct {
	UseDeltaBlock
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Input *value.Object `json:"input,omitempty"`
	Type  BlockType     `json:"type"`
}

func (b *ToolUseBlock) ForContext() bool {
	return true
}
func (b *ToolUseBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID    string        `json:"id"`
		Name  string        `json:"name"`
		Input *value.Object `json:"input,omitempty"`
		Type  BlockType     `json:"type"`
	}{ID: b.ID, Name: b.Name, Input: b.Input, Type: b.Type})
}
func (b *ToolUseBlock) ParesStream(stream *value.Stream) {
	b.Input, _ = value.NewObjectFromJson(stream.ToJSON())
}
func NewToolUseBlock(id string, name string) *ToolUseBlock {
	return &ToolUseBlock{
		ID:   id,
		Name: name,
		Type: ToolUseBlockType,
	}
}

type ToolExecutionBlock struct {
	Block
	ToolName string    `json:"tool_name"`
	Args     string    `json:"args"`
	Output   string    `json:"output"`
	Type     BlockType `json:"type"`
}

func (b *ToolExecutionBlock) ForContext() bool {
	return false
}
func NewToolExecutionBlock(toolName string, args string, Output string) *ToolExecutionBlock {
	return &ToolExecutionBlock{
		ToolName: toolName,
		Args:     args,
		Output:   Output,
		Type:     ToolExecutionBlockType,
	}
}

type ToolResultBlock struct {
	ToolUseID string    `json:"tool_use_id"`
	Content   []Block   `json:"content,omitempty"` // string 或 []Block
	Type      BlockType `json:"type"`
}

func (b *ToolResultBlock) ForContext() bool {
	return true
}
func NewToolResultBlock(id string, content []Block) *ToolResultBlock {
	return &ToolResultBlock{
		ToolUseID: id,
		Content:   content,
		Type:      ToolResultBlockType,
	}
}

type StartBlock struct {
	Type  BlockType `json:"type"`
	Block Block     `json:"block"`
}

func (b *StartBlock) ForContext() bool {
	return false
}

func NewStartBlock(block Block) *StartBlock {
	return &StartBlock{
		Type:  StartBlockType,
		Block: block,
	}
}

type DeltaBlock struct {
	Type    BlockType `json:"type"`
	Content string    `json:"content"`
}

func (b *DeltaBlock) ForContext() bool {
	return false
}
func NewDeltaBlock(content string) *DeltaBlock {
	return &DeltaBlock{
		Type:    DeltaBlockType,
		Content: content,
	}
}

type DoneBlock struct {
	Type BlockType `json:"type"`
}

func NewDoneBlock() *DoneBlock {
	return &DoneBlock{
		Type: DoneBlockType,
	}
}
func (b *DoneBlock) ForContext() bool {
	return false
}

type BlockUserType string

const (
	Queued  BlockUserType = "queued"
	Sent    BlockUserType = "sent"
	Consume BlockUserType = "consume"
)

type UserBlock struct {
	Type          BlockType     `json:"type"`
	Queued        bool          `json:"queued"`
	BlockUserType BlockUserType `json:"block_user_type"`
	Content       []Block       `json:"content,omitempty"` // string 或 []Block
}

func (b *UserBlock) ForContext() bool {
	return true
}
func NewUserBlock(id, text string, blockUserType BlockUserType) *UserBlock {
	return &UserBlock{
		Type:          UserBlockType,
		BlockUserType: blockUserType,
		Content: []Block{
			NewFullTextBlock(text),
		},
	}
}
