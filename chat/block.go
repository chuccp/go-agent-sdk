package chat

import (
	"encoding/json"
	"fmt"

	"github.com/chuccp/go-agent-sdk/value"
)

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
	Text string    `json:"text"`
	Type BlockType `json:"type"`
}

func (b *ErrorBlock) ForContext() bool {
	return false
}

func NewErrorBlock(text string) *ErrorBlock {
	return &ErrorBlock{
		Text: text,
		Type: ErrorBlockType,
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

// UnmarshalJSON 按每个元素的 type 字段分发，还原为对应的具体 Block 类型。
// Block 是接口，标准库无法自动推断具体类型，故在此显式分发，
// 使 Blocks 可跨进程（如历史持久化）无损往返。
func (b *Blocks) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	result := make(Blocks, 0, len(raw))
	for _, item := range raw {
		var t struct {
			Type BlockType `json:"type"`
		}
		if err := json.Unmarshal(item, &t); err != nil {
			return err
		}
		var block Block
		switch t.Type {
		case TextBlockType:
			block = &TextBlock{}
		case ThinkingBlockType:
			block = &ThinkingBlock{}
		case ImageBlockType:
			block = &ImageBlock{}
		case ToolUseBlockType:
			block = &ToolUseBlock{}
		case ToolResultBlockType:
			block = &ToolResultBlock{}
		case UsageBlockType:
			block = &UsageBlock{}
		case ErrorBlockType:
			block = &ErrorBlock{}
		case DoneBlockType:
			block = &DoneBlock{}
		case DeltaBlockType:
			block = &DeltaBlock{}
		case ToolExecutionBlockType:
			block = &ToolExecutionBlock{}
		case UserBlockType:
			block = &UserBlock{}
		default:
			return fmt.Errorf("unknown block type %q", t.Type)
		}
		if err := json.Unmarshal(item, block); err != nil {
			return err
		}
		result = append(result, block)
	}
	*b = result
	return nil
}

type TextType string

const (
	ErrorTextType    TextType = "error"
	CMDTextType      TextType = "cmd"
	ToolResultType   TextType = "tool_result"
	FlowProgressType TextType = "flow_progress"
)

type TextBlock struct {
	Text      string    `json:"text"`
	Type      BlockType `json:"type"`
	TextType  TextType  `json:"text_type"`
	ToolUseId string    `json:"tool_use_id,omitempty"`
}

func (b *TextBlock) ForContext() bool {
	return true
}
func (b *TextBlock) ParesStream(stream *value.Stream) {
	b.Text = stream.String()
}
func NewTextBlock() *TextBlock {
	return &TextBlock{
		Type: TextBlockType,
	}
}

func NewToolResultTextBlock(toolUseId string) *TextBlock {
	return &TextBlock{
		TextType:  ToolResultType,
		ToolUseId: toolUseId,
		Type:      TextBlockType,
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
	Usage *Usage
	Type  BlockType `json:"type"`
}

func (b *UsageBlock) ForContext() bool {
	return false
}
func NewUsageBlock(usage *Usage) *UsageBlock {
	return &UsageBlock{
		Usage: usage,
		Type:  UsageBlockType,
	}
}

type ThinkingBlock struct {
	Thinking string    `json:"thinking,omitempty"`
	Type     BlockType `json:"type"`
}

func (b *ThinkingBlock) ForContext() bool {
	return false
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
	Source *ImageSource `json:"source,omitempty"`
	Type   BlockType    `json:"type"`
}

func (b *ImageBlock) ForContext() bool {
	return true
}

type ToolUseBlock struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Input *value.Object `json:"input,omitempty"`
	Type  BlockType     `json:"type"`
}

func (b *ToolUseBlock) ForContext() bool {
	return true
}
func (b *ToolUseBlock) ParesStream(stream *value.Stream) {
	b.Input, _ = value.NewObjectFromJson(stream.ToJSON())
}

// UnmarshalJSON 反序列化时显式重建 Input（*value.Object 无标准库可用的反序列化路径）。
func (b *ToolUseBlock) UnmarshalJSON(data []byte) error {
	type toolUseAlias struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
		Type  BlockType       `json:"type"`
	}
	var a toolUseAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	b.ID = a.ID
	b.Name = a.Name
	b.Type = a.Type
	if len(a.Input) > 0 {
		obj, err := value.NewObjectFromJson(a.Input)
		if err != nil {
			return err
		}
		b.Input = obj
	}
	return nil
}
func NewToolUseBlock(id string, name string) *ToolUseBlock {
	return &ToolUseBlock{
		ID:   id,
		Name: name,
		Type: ToolUseBlockType,
	}
}

type ToolExecutionBlock struct {
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
	Content   Blocks    `json:"content,omitempty"` // string 或 []Block
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
	Type  BlockType     `json:"type"`
	Block UseDeltaBlock `json:"block"`
}

func (b *StartBlock) ForContext() bool {
	return false
}

func NewStartBlock(block UseDeltaBlock) *StartBlock {
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
	ID            string        `json:"id,omitempty"` // 用户消息稳定 ID：sent/queued/consume 同一条消息共享
	Type          BlockType     `json:"type"`
	BlockUserType BlockUserType `json:"block_user_type"`
	Content       Blocks        `json:"content,omitempty"` // string 或 []Block
}

func (b *UserBlock) ForContext() bool {
	return true
}
func NewUserBlock(id, text string, blockUserType BlockUserType) *UserBlock {
	return &UserBlock{
		ID:            id,
		Type:          UserBlockType,
		BlockUserType: blockUserType,
		Content: []Block{
			NewFullTextBlock(text),
		},
	}
}
