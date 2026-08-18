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
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type BlockReceiver interface {
	AddBlock(block Block)
}

type assemblerBlock struct {
	stream *value.Stream
	block  UseDeltaBlock
	active bool
}

func (a *assemblerBlock) start(block UseDeltaBlock) {
	a.block = block
	a.active = true
	a.stream.Reset()
}
func (a *assemblerBlock) flush() (UseDeltaBlock, bool) {
	if a.active {
		a.block.ParesStream(a.stream)
		a.stream.Reset()
		a.active = false
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
}

func NewBlockStream(receiver BlockReceiver) *BlockStream {
	return &BlockStream{
		receiver: receiver,
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
	// 连续同类型 TextBlock 合并（工具路径多次 Block 拼接为一块）
	if tb, ok := block.(*TextBlock); ok && len(s.blocks) > 1 {
		if prev, ok := s.blocks[len(s.blocks)-2].(*TextBlock); ok && prev.TextType == tb.TextType {
			prev.Text += tb.Text
			s.blocks = s.blocks[:len(s.blocks)-1]
		}
	}
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
func (s *BlockStream) Usage(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(NewUsageBlock(usage))
}
func (s *BlockStream) flushAndAdd(block Block) {
	if s.receiver != nil {
		s.receiver.AddBlock(block)
	}
	s.flush()
	s.blocks = append(s.blocks, block)
}
func (s *BlockStream) flushAndStart(block UseDeltaBlock) {
	if s.receiver != nil {
		s.receiver.AddBlock(NewStartBlock(block))
	}
	s.flush()
	s.assemblerBlock.start(block)

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
	if s.receiver != nil {
		s.receiver.AddBlock(NewDeltaBlock(content))
	}
	s.assemblerBlock.delta(content)
}
func (s *BlockStream) ReadBlocks() Blocks {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	return s.blocks
}

func (s *BlockStream) GetStopReason() StopReason {
	if s.stopReason == "" {
		return StopReasonEndTurn
	}
	return s.stopReason
}
