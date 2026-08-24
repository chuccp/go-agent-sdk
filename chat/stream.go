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
		a.block.SetStart(a.blockStart)
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
func (s *BlockStream) Delta(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delta(content)
}
func (s *BlockStream) StopReason(stopReason StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopReason = stopReason
}
func (s *BlockStream) deltaUsage(usage *Usage) {
	if usage.InputTokens > s.usage.InputTokens {
		s.usage.InputTokens = usage.InputTokens
	}
	if usage.InputTokens == 0 {
		usage.InputTokens = s.usage.InputTokens
	}
	if usage.CacheInputTokens > s.usage.CacheInputTokens {
		s.usage.CacheInputTokens = usage.CacheInputTokens
	}
	if usage.CacheInputTokens == 0 {
		usage.CacheInputTokens = s.usage.CacheInputTokens
	}
	if usage.OutputTokens > s.usage.OutputTokens {
		s.usage.OutputTokens = usage.OutputTokens
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = s.usage.OutputTokens
	}
}
func (s *BlockStream) MessageStart(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deltaUsage(usage)
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
		block.SetStart(start)
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
	start := s.sendBlock(NewStartBlock(block))
	s.flush()
	s.assemblerBlock.start(start, block)

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
		Start:   s.firstStart,
		Offset:  offset,
		Content: blocks,
	}
}
func (s *BlockStream) GetStopReason() StopReason {
	if s.stopReason == "" {
		return StopReasonEndTurn
	}
	return s.stopReason
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
func (s *ToolResultBlockStream) Delta(content string) {
	s.blockStream.Delta(content)
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
