package chat

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/value"
)

type StopReason string

const (
	StopReasonEndTurn    StopReason = "end_turn"      // 自然结束
	StopReasonMaxTokens  StopReason = "max_tokens"    // 达到 max_tokens 上限
	StopReasonToolUse    StopReason = "tool_use"      // 需要调用工具
	StopReasonStopSeq    StopReason = "stop_sequence" // 命中停止序列
	StopReasonToolResult StopReason = "tool_result"   // 工具轮次的默认停止原因：已产出 tool_result，继续携带结果调用 LLM
	// StopReasonUserWait 工具请求暂停：结束本轮（不再携带 tool_result 回调 LLM），
	// 等待用户下一条普通消息（如 ask_user_question 提问）。仅工具路径设置。
	StopReasonUserWait StopReason = "user_wait"
)

// Usage 记录本次请求的 token 消耗。
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheInputTokens int `json:"cache_input_tokens"`
}

type BlockReceiver interface {
	SendBlock(block Block) uint64
}

type assemblerBlock struct {
	stream     *value.Stream
	block      UseDeltaBlock
	active     bool
	blockStart uint64
}

func (a *assemblerBlock) start(blockStart uint64, block UseDeltaBlock) {
	a.block = block
	a.active = true
	a.blockStart = blockStart
	a.stream.Reset()
}
func (a *assemblerBlock) flush() (UseDeltaBlock, bool) {
	if a.active {
		a.block.ParseStream(a.stream)
		a.stream.Reset()
		a.active = false
		a.blockStart = 0
		return a.block, true
	}
	return nil, false
}

func (a *assemblerBlock) delta(content string) {
	if a.active {
		a.stream.WriteString(content)
	}
}

// BlockWriter 是 LLM provider 写流式响应的口子，由 *BlockStream 实现：只含写入面，
// 读回结果（ReadBlocks / ReadBlockGroup / GetStopReason / Usage）不在里面。
//
// 抽成接口是为了让 provider 不绑在具体实现上——调用方能在中间包一层做改写或旁路，
// provider 代码不用动。写包装器时建议内嵌 *BlockStream（或 *ToolResultBlockStream），
// 省得把这十几个方法逐个实现一遍。
type BlockWriter interface {
	MessageStart(usage *Usage)
	MessageDelta(usage *Usage)

	BlockStart(block UseDeltaBlock)
	BlockTextStart()
	BlockErrorTextStart()
	BlockThinkingStart()
	BlockToolUseStart(id string, name string)
	BlockServerToolUseStart(id string, name string)
	Block(block Block)

	FullText(content string)
	ErrorText(err error)
	BlockDelta(content string)
	BlockStop()

	StopReason(stopReason StopReason)
}

var _ BlockWriter = (*BlockStream)(nil)

type BlockStream struct {
	stopReason     StopReason
	receiver       BlockReceiver
	blocks         []Block
	mu             sync.Mutex
	assemblerBlock *assemblerBlock
	firstStart     uint64
	maxEndStart    uint64 // 追踪所有 sendBlock 的最大 endStart，ReadBlockGroup 用它算 Offset
	usage          *Usage
}

func NewBlockStream(receiver BlockReceiver) *BlockStream {
	return &BlockStream{
		usage: &Usage{
			InputTokens:      0,
			OutputTokens:     0,
			CacheInputTokens: 0,
		},
		receiver: receiver,
		blocks:   make([]Block, 0),
		assemblerBlock: &assemblerBlock{
			stream: value.NewStream(),
			block:  nil,
			active: false,
		},
	}
}
func (s *BlockStream) BlockStart(block UseDeltaBlock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(block)
}

func (s *BlockStream) Usage() *Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

func (s *BlockStream) BlockTextStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewTextBlock())
}
func (s *BlockStream) BlockErrorTextStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewErrorTextBlock())
}
func (s *BlockStream) BlockThinkingStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewThinkingBlock())

}
func (s *BlockStream) BlockToolUseStart(id string, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewToolUseBlock(id, name))

}
func (s *BlockStream) BlockServerToolUseStart(id string, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewServerToolUseBlock(id, name))
}
func (s *BlockStream) Block(block Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(block)
}
func (s *BlockStream) ErrorText(error error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(NewErrorFullTextBlock(error.Error()))
}
func (s *BlockStream) FullText(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(NewFullTextBlock(content))
}
func (s *BlockStream) BlockDelta(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delta(content)
}
func (s *BlockStream) StopReason(stopReason StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopReason = stopReason
}
func (s *BlockStream) startUsage(usage *Usage) {
	s.usage.OutputTokens = usage.OutputTokens
	s.usage.CacheInputTokens = usage.CacheInputTokens
	s.usage.InputTokens = usage.InputTokens
}
func (s *BlockStream) deltaUsage(usage *Usage) {
	s.usage.OutputTokens = s.usage.OutputTokens + usage.OutputTokens
}
func (s *BlockStream) MessageStart(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startUsage(usage)
	messageStart := NewMessageStartBlock(usage)
	s.flushAndAdd(messageStart)
}
func (s *BlockStream) MessageDelta(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deltaUsage(usage)
	messageDelta := NewMessageDeltaBlock(usage)
	s.flushAndAdd(messageDelta)
}
func (s *BlockStream) flushAndAdd(block Block) {
	s.sendBlock(block)
	s.flush()
	s.blocks = append(s.blocks, block)
}
func (s *BlockStream) sendBlock(block Block) uint64 {
	if s.receiver != nil {
		start := s.receiver.SendBlock(block)
		// 记录 block 在事件流中的序号，供 relay 按 block 粒度去重
		if s.firstStart == 0 {
			s.firstStart = start
		}
		if start > s.maxEndStart {
			s.maxEndStart = start
		}
		return start
	}
	return 0
}
func (s *BlockStream) flushAndStart(block UseDeltaBlock) {
	s.flush()
	start := s.sendBlock(NewStartBlock(block))
	s.assemblerBlock.start(start, block)

}
func (s *BlockStream) MaxEndStart() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxEndStart
}
func (s *BlockStream) flush() {
	block, fa := s.assemblerBlock.flush()
	if fa && block != nil {
		// 跳过空内容块（如无增量的 thinking/tool_use）
		if s.isEmptyBlock(block) {
			return
		}
		s.blocks = append(s.blocks, block)
	}
}

// isEmptyBlock 检查组装后的 block 是否为空内容。
func (s *BlockStream) isEmptyBlock(block UseDeltaBlock) bool {
	switch b := block.(type) {
	case *TextBlock:
		return b.Text == ""
	case *ThinkingBlock:
		return b.Thinking == ""
	case *ServerToolUseBlock:
		return len(b.Input) == 0
	default:
		return false
	}
}
func (s *BlockStream) delta(content string) {
	s.sendBlock(NewDeltaBlock(content))
	s.assemblerBlock.delta(content)
}
func (s *BlockStream) ReadBlocks() Blocks {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	return s.blocks
}

func (s *BlockStream) BlockStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	s.sendBlock(NewStopBlock())
}

func (s *BlockStream) ReadBlockGroup() *BlockGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	blocks := s.blocks
	// 用 maxEndStart 而非 endStart：后续的 MessageDelta 等调用会更新 endStart，
	// 但 ReadBlockGroup 在它们之前调用，此时 endStart 只反映最后一个 content delta 的位置。
	// maxEndStart 追踪所有 sendBlock 的最大位置，确保 Offset 覆盖完整的消息范围。
	offset := s.maxEndStart - s.firstStart + 1
	return &BlockGroup{
		Start:     s.firstStart,
		Offset:    offset,
		LastStart: s.maxEndStart,
		Content:   blocks,
	}
}
func (s *BlockStream) GetStopReason() StopReason {
	if s.stopReason == "" {
		return StopReasonEndTurn
	}
	return s.stopReason
}

// CompressionBlockStream 是上下文压缩专用的写入壳：内嵌 *BlockStream，只把「开始写正文」
// 的几个口子换成带 CompressionTextType 的块——provider 照常往里写，落到事件流里的块都带
// text_type=compression，前端据此不与助手正文混在一起。
//
// 用法：
//
//	stream := chat.NewCompressionBlockStream(chat.NewBlockStream(ctx))
//	chatService.ChatWithStream(ctx, req, stream) // provider 只认 BlockWriter，正好
//	group := stream.ReadBlockGroup()             // 读回组装结果，内嵌的方法直接可用
type CompressionBlockStream struct {
	*BlockStream
}

var _ BlockWriter = (*CompressionBlockStream)(nil)

func NewCompressionBlockStream(stream *BlockStream) *CompressionBlockStream {
	return &CompressionBlockStream{BlockStream: stream}
}

// BlockTextStart 用带压缩标记的块起头：前端是从 start 块的内层块上读 text_type 的
// （见 WebSocketAdapter），标记必须落在起头这块上。
func (s *CompressionBlockStream) BlockTextStart() {
	s.BlockStream.BlockStart(NewCompressionTextBlock())
}

// Block 整块写入时也补标记（provider 若绕过 BlockTextStart 直接写整块）。
func (s *CompressionBlockStream) Block(block Block) {
	if tb, ok := block.(*TextBlock); ok {
		block = tagPlainText(tb)
	}
	s.BlockStream.Block(block)
}

func (s *CompressionBlockStream) FullText(content string) {
	s.BlockStream.Block(NewFullTextTypeBlock(content, CompressionTextType))
}

// tagPlainText 给没带类型的文本块打上压缩标记；已有类型（error 等）的原样返回。
func tagPlainText(b *TextBlock) *TextBlock {
	if b.TextType != "" {
		return b
	}
	cp := *b
	cp.TextType = CompressionTextType
	return &cp
}

type ToolResultBlockStream struct {
	blockStream *BlockStream
	ToolUseId   string
}

func NewToolResultBlockStream(blockStream *BlockStream, ToolUseId string) *ToolResultBlockStream {
	return &ToolResultBlockStream{
		blockStream: blockStream,
		ToolUseId:   ToolUseId,
	}
}
func (s *ToolResultBlockStream) BlockTextStart() {
	s.blockStream.BlockStart(NewToolResultTextBlock(s.ToolUseId))
}
func (s *ToolResultBlockStream) BlockTextTypeStart(textType TextType) {
	toolResultText := NewToolResultTextBlock(s.ToolUseId)
	toolResultText.TextType = textType
	s.blockStream.BlockStart(toolResultText)
}
func (s *ToolResultBlockStream) BlockDelta(content string) {
	s.blockStream.BlockDelta(content)
}

func (s *ToolResultBlockStream) BlockStop() {
	s.blockStream.BlockStop()
}

func (s *ToolResultBlockStream) ErrorText(error error) {
	block := NewToolResultTextBlock(s.ToolUseId)
	block.Text = error.Error()
	s.blockStream.Block(block)
}
func (s *ToolResultBlockStream) ErrorTextType(error error, textType TextType) {
	block := NewToolResultTextBlock(s.ToolUseId)
	block.Text = error.Error()
	block.TextType = textType
	s.blockStream.Block(block)
}
func (s *ToolResultBlockStream) FullText(content string) {
	block := NewToolResultTextBlock(s.ToolUseId)
	block.Text = content
	s.blockStream.Block(block)
}
func (s *ToolResultBlockStream) FullTextType(content string, textType TextType) {
	block := NewToolResultTextBlock(s.ToolUseId)
	block.Text = content
	block.TextType = textType
	s.blockStream.Block(block)
}
func (s *ToolResultBlockStream) FullCustomTextType(content string, textType TextType) {
	block := NewCustomTextBlockWithTool(s.ToolUseId, content, textType)
	s.blockStream.Block(block)
}
func (s *ToolResultBlockStream) StopReason(wait StopReason) {
	s.blockStream.StopReason(wait)
}
func (s *ToolResultBlockStream) ReadBlocks() Blocks {
	return s.blockStream.ReadBlocks()
}
func (s *ToolResultBlockStream) Block(block Block) {
	s.blockStream.Block(block)
}
func (s *ToolResultBlockStream) ReadBlockGroup() *BlockGroup {
	return s.blockStream.ReadBlockGroup()
}
