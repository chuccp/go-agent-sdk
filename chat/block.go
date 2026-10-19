package chat

import (
	"encoding/json"
	"fmt"

	"github.com/chuccp/go-agent-sdk/jsonx"
)

type BlockType string

const (
	TextBlockType         BlockType = "text"
	ThinkingBlockType     BlockType = "thinking"
	ImageBlockType        BlockType = "image"
	ToolUseBlockType      BlockType = "tool_use"
	ToolResultBlockType   BlockType = "tool_result"
	StartBlockType        BlockType = "start"
	DeltaBlockType        BlockType = "delta"
	DoneBlockType         BlockType = "done"
	UserBlockType         BlockType = "User"
	ErrorBlockType        BlockType = "error"
	MessageStartBlockType BlockType = "message_start"
	MessageDeltaBlockType BlockType = "message_delta"

	CustomTextBlockType    BlockType = "custom_text"
	ServerToolUseBlockType BlockType = "server_tool_use"
	// WebSearchResultBlockType 服务端联网搜索的**结果**块（Anthropic 的
	// `web_search_tool_result`）。原来没认它，于是客户端完全看不到"搜到了什么"——
	// 实测客户端收到的流里只有 `server_tool_use`，结果块被当成普通文本丢掉了。
	WebSearchResultBlockType BlockType = "web_search_result"
	StopBlockType          BlockType = "stop"
)

type ErrorBlock struct {
	BaseBlock
	Text string `json:"text"`
}

func (b *ErrorBlock) ForContext() bool {
	return false
}

func NewErrorBlock(text string) *ErrorBlock {
	return &ErrorBlock{
		BaseBlock: BaseBlock{Type: ErrorBlockType},
		Text:      text,
	}
}

type UseDeltaBlock interface {
	Block
	ParseStream(stream *jsonx.Stream)
}

// BaseBlock 是所有 Block 的公共基类：Type 记录块类型（序列化为 `type` 字段，
// 供 Blocks.UnmarshalJSON 分发还原），Start 记录该 block 在事件流中的序号，
// 供 relay 按 block 粒度去重（比按 Event 粒度更精确，避免 message 合并后
// 因 Event.Start 为旧值而被整体跳过）。
type BaseBlock struct {
	Type BlockType `json:"type"`
}

func (b *BaseBlock) GetType() BlockType { return b.Type }

type Block interface {
	ForContext() bool
	GetType() BlockType
}

type BlockGroup struct {
	Start     uint64 `json:"-"`
	Offset    uint64 `json:"-"`
	LastStart uint64 `json:"-"`
	Content   Blocks `json:"content"` // content block 数组
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
		// 按 type 字段硬编码分发。CustomTextBlock 是唯一允许业务扩展文本的载体：
		// 业务把自定义内容放进 Text，用 TextType 表达语义，不新增块类型。
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
		case MessageStartBlockType:
			block = &MessageStartBlock{}
		case MessageDeltaBlockType:
			block = &MessageDeltaBlock{}
		case ErrorBlockType:
			block = &ErrorBlock{}
		case DoneBlockType:
			block = &DoneBlock{}
		case DeltaBlockType:
			block = &DeltaBlock{}

		case UserBlockType:
			block = &UserBlock{}
		case CustomTextBlockType:
			block = &CustomTextBlock{}
		case ServerToolUseBlockType:
			block = &ServerToolUseBlock{}
		case WebSearchResultBlockType:
			block = &WebSearchResultBlock{}
		case StopBlockType:
			block = &StopBlock{}
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
	FlowProgressType TextType = "flow_progress"
	AskUserTextType  TextType = "ask_user" // ask_user_question 工具的问题卡片（CustomTextBlock.TextType）
	InternalTextType TextType = "internal" // 仅 LLM 上下文，不在前端显示（如 ask_user 的提示文本）
	// CompressionTextType 上下文压缩的输出（摘要正文）：前端不应把它当助手正文渲染，
	// 与 flow_progress / ask_user 一样靠 text_type 区分。
	CompressionTextType TextType = "compression"
)

// CustomTextBlock 是唯一允许业务扩展的文本块：不进 LLM 上下文（ForContext=false），
// 自定义内容放进 Text，用 TextType 表达语义（如 ask_user / resource_card / plan_card）。
// 业务不要新增自定义块类型，统一走 CustomTextBlock + TextType。
type CustomTextBlock struct {
	BaseBlock
	Text      string   `json:"text"`
	TextType  TextType `json:"text_type"`
	ToolUseId string   `json:"tool_use_id,omitempty"`
}

func (b *CustomTextBlock) ForContext() bool {
	return false
}
func (b *CustomTextBlock) GetType() BlockType {
	return CustomTextBlockType
}
func NewCustomTextBlock(text string, textType TextType) *CustomTextBlock {
	return &CustomTextBlock{
		BaseBlock: BaseBlock{Type: CustomTextBlockType},
		Text:      text,
		TextType:  textType,
	}
}
func NewCustomTextBlockWithTool(toolUseId string, text string, textType TextType) *CustomTextBlock {
	return &CustomTextBlock{
		BaseBlock: BaseBlock{Type: CustomTextBlockType},
		Text:      text,
		TextType:  textType,
		ToolUseId: toolUseId,
	}
}

// WebSearchResultBlock 服务端联网搜索的结果：**只带标题**（不含链接、不含正文）。
//
// 为什么只留标题（2026-10-16 用户定："可以展示 title。link就不展示了，没意义"）：
// Anthropic 的结果项长这样——
//
//	{"type":"web_search_result","title":"…","url":"https://…","encrypted_content":"7u1Cs9cL…"}
//
// `encrypted_content` 是加密串（只有服务端模型能消化，客户端解不开也没必要解），
// `url` 在电视上既点不了也念不出来，摆出来纯属噪音。**标题是唯一对用户有意义的东西**
// （"它到底查到了些啥"），所以这里只抽 titles + 条数，连轮数上限错误也一并记下来。
type WebSearchResultBlock struct {
	BaseBlock
	ToolUseID string   `json:"tool_use_id,omitempty"`
	Titles    []string `json:"titles,omitempty"`
	// ErrorCode 非空表示这一轮搜索没成（如 max_uses_exceeded = 超出 max_uses 上限）
	ErrorCode string `json:"error_code,omitempty"`
}

func (b *WebSearchResultBlock) ForContext() bool { return false }
func (b *WebSearchResultBlock) GetType() BlockType { return WebSearchResultBlockType }

// ParseStream 这个块不经 delta 流（结果是一次性给全的），实现它只是为了满足
// UseDeltaBlock 接口——BlockStart 那条路要它。
func (b *WebSearchResultBlock) ParseStream(stream *jsonx.Stream) {}

func NewWebSearchResultBlock(toolUseID string, titles []string, errorCode string) *WebSearchResultBlock {
	return &WebSearchResultBlock{
		BaseBlock: BaseBlock{Type: WebSearchResultBlockType},
		ToolUseID: toolUseID,
		Titles:    titles,
		ErrorCode: errorCode,
	}
}

// ServerToolUseBlock 表示 Anthropic 服务端内置工具调用（如 web_search）。
// input_json_delta 由 assembler 缓冲，ParseStream 时解析为结构化 Input。
type ServerToolUseBlock struct {
	BaseBlock
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

func (b *ServerToolUseBlock) ForContext() bool {
	return false
}
func (b *ServerToolUseBlock) GetType() BlockType {
	return ServerToolUseBlockType
}
func (b *ServerToolUseBlock) ParseStream(stream *jsonx.Stream) {
	b.Input = stream.ToJSON()
}
func (b *ServerToolUseBlock) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
		Type  BlockType       `json:"type"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	b.ID = a.ID
	b.Name = a.Name
	b.BaseBlock.Type = a.Type
	b.Input = a.Input
	return nil
}
func NewServerToolUseBlock(id string, name string) *ServerToolUseBlock {
	return &ServerToolUseBlock{
		BaseBlock: BaseBlock{Type: ServerToolUseBlockType},
		ID:        id,
		Name:      name,
	}
}

type TextBlock struct {
	BaseBlock
	Text      string   `json:"text"`
	TextType  TextType `json:"text_type"`
	ToolUseId string   `json:"tool_use_id,omitempty"`
}

func (b *TextBlock) ForContext() bool {
	return true
}
func (b *TextBlock) ParseStream(stream *jsonx.Stream) {
	b.Text = stream.String()
}
func NewTextBlock() *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
	}
}

func NewToolResultTextBlock(toolUseId string) *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
		ToolUseId: toolUseId,
	}
}

// NewCompressionTextBlock 返回带压缩标记的流式文本块起点：与普通 TextBlock 同类型，
// 靠 TextType 区分，前端据此不与助手正文混在一起。
func NewCompressionTextBlock() *TextBlock {
	return NewFullTextTypeBlock("", CompressionTextType)
}

func NewErrorTextBlock() *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
		TextType:  ErrorTextType,
	}
}
func NewErrorFullTextBlock(text string) *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
		TextType:  ErrorTextType,
		Text:      text,
	}
}
func NewToolsErrorFullTextBlock(toolUseId string, text string) *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
		TextType:  ErrorTextType,
		Text:      text,
		ToolUseId: toolUseId,
	}
}
func NewFullTextBlock(text string) *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
		Text:      text,
	}
}
func NewFullTextTypeBlock(text string, textType TextType) *TextBlock {
	return &TextBlock{
		BaseBlock: BaseBlock{Type: TextBlockType},
		Text:      text,
		TextType:  textType,
	}
}

type MessageStartBlock struct {
	BaseBlock
	Usage *Usage
}

func (b *MessageStartBlock) ForContext() bool {
	return false
}
func NewMessageStartBlock(usage *Usage) *MessageStartBlock {
	return &MessageStartBlock{
		BaseBlock: BaseBlock{Type: MessageStartBlockType},
		Usage:     usage,
	}
}

type MessageDeltaBlock struct {
	BaseBlock
	Usage *Usage
}

func (b *MessageDeltaBlock) ForContext() bool {
	return false
}
func NewMessageDeltaBlock(usage *Usage) *MessageDeltaBlock {
	return &MessageDeltaBlock{
		BaseBlock: BaseBlock{Type: MessageDeltaBlockType},
		Usage:     usage,
	}
}

type ThinkingBlock struct {
	BaseBlock
	Thinking string `json:"thinking,omitempty"`
}

func (b *ThinkingBlock) ForContext() bool {
	return false
}
func (b *ThinkingBlock) ParseStream(stream *jsonx.Stream) {
	b.Thinking = stream.String()
}
func NewThinkingBlock() *ThinkingBlock {
	return &ThinkingBlock{
		BaseBlock: BaseBlock{Type: ThinkingBlockType},
	}
}

type ImageSource struct {
	SourceType string `json:"type"`       // "base64"
	MediaType  string `json:"media_type"` // "image/png" | "image/jpeg" | "image/gif" | "image/webp"
	Data       string `json:"data"`       // base64 编码的图片数据
}

type ImageBlock struct {
	BaseBlock
	Source *ImageSource `json:"source,omitempty"`
}

func (b *ImageBlock) ForContext() bool {
	return true
}

type ToolUseBlock struct {
	BaseBlock
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Input *jsonx.Object `json:"input,omitempty"`
}

func (b *ToolUseBlock) ForContext() bool {
	return true
}
func (b *ToolUseBlock) ParseStream(stream *jsonx.Stream) {
	b.Input, _ = jsonx.NewObjectFromJson(stream.ToJSON())
}

// UnmarshalJSON 反序列化时显式重建 Input（*jsonx.Object 无标准库可用的反序列化路径）。
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
	b.BaseBlock.Type = a.Type
	if len(a.Input) > 0 {
		obj, err := jsonx.NewObjectFromJson(a.Input)
		if err != nil {
			return err
		}
		b.Input = obj
	}
	return nil
}
func NewToolUseBlock(id string, name string) *ToolUseBlock {
	return &ToolUseBlock{
		BaseBlock: BaseBlock{Type: ToolUseBlockType},
		ID:        id,
		Name:      name,
	}
}

type ToolResultBlock struct {
	BaseBlock
	ToolUseID string `json:"tool_use_id"`
	Content   Blocks `json:"content,omitempty"` // string 或 []Block
}

func (b *ToolResultBlock) ForContext() bool {
	return true
}
func NewToolResultBlock(id string, content []Block) *ToolResultBlock {
	b := &ToolResultBlock{
		BaseBlock: BaseBlock{Type: ToolResultBlockType},
		ToolUseID: id,
		Content:   content,
	}
	return b
}

type StartBlock struct {
	BaseBlock
	Block UseDeltaBlock `json:"block"`
}

func (b *StartBlock) ForContext() bool {
	return false
}

func NewStartBlock(block UseDeltaBlock) *StartBlock {
	return &StartBlock{
		BaseBlock: BaseBlock{Type: StartBlockType},
		Block:     block,
	}
}

type DeltaBlock struct {
	BaseBlock
	Content string `json:"content"`
}

func (b *DeltaBlock) ForContext() bool {
	return false
}
func NewDeltaBlock(content string) *DeltaBlock {
	return &DeltaBlock{
		BaseBlock: BaseBlock{Type: DeltaBlockType},
		Content:   content,
	}
}

type StopBlock struct {
	BaseBlock
}

func (b *StopBlock) ForContext() bool {
	return false
}
func NewStopBlock() *StopBlock {
	return &StopBlock{
		BaseBlock: BaseBlock{Type: StopBlockType},
	}
}

type DoneBlock struct {
	BaseBlock
	Usage *Usage `json:"usage,omitempty"`
}

func NewDoneBlock() *DoneBlock {
	return &DoneBlock{
		BaseBlock: BaseBlock{Type: DoneBlockType},
	}
}
func NewDoneBlockWithUsage(usage *Usage) *DoneBlock {
	return &DoneBlock{
		BaseBlock: BaseBlock{Type: DoneBlockType},
		Usage:     usage,
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
	BaseBlock
	ID            uint64        `json:"id,omitempty"` // 用户消息稳定 ID：sent/queued/consume 同一条消息共享
	BlockUserType BlockUserType `json:"block_user_type"`
	Content       Blocks        `json:"content,omitempty"` // string 或 []Block
}

func (b *UserBlock) ForContext() bool {
	return true
}
func NewUserTextBlock(id uint64, text string, blockUserType BlockUserType) *UserBlock {
	return NewUserBlock(id, []Block{NewFullTextBlock(text)}, blockUserType)
}
func NewUserBlock(id uint64, blocks Blocks, blockUserType BlockUserType) *UserBlock {
	return &UserBlock{
		BaseBlock:     BaseBlock{Type: UserBlockType},
		ID:            id,
		BlockUserType: blockUserType,
		Content:       blocks,
	}
}
