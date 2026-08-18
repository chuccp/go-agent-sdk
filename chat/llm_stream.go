package chat

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/value"
)

type BlockReceiver interface {
	AddBlock(block Block2)
}

type assemblerBlock struct {
	stream *value.Stream
	block  Block2
	active bool
}

func (a *assemblerBlock) start(block Block2) {
	a.block = block
	a.active = true
	a.stream.Reset()
}
func (a *assemblerBlock) flush() (Block2, bool) {
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

type LLMStream struct {
	stopReason     StopReason
	receiver       BlockReceiver
	blocks         []Block2
	mu             sync.Mutex
	assemblerBlock *assemblerBlock
}

func NewLLMStream(receiver BlockReceiver) *LLMStream {
	return &LLMStream{
		receiver: receiver,
		assemblerBlock: &assemblerBlock{
			stream: value.NewStream(),
			block:  nil,
			active: false,
		},
	}
}
func (s *LLMStream) BlockStart(block Block2) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(block)
}
func (s *LLMStream) BlockTextStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewTextBlock2())
}
func (s *LLMStream) BlockErrorTextStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewErrorTextBlock2())
}
func (s *LLMStream) BlockThinkingStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewThinkingBlock2())

}
func (s *LLMStream) BlockToolUseStart(id string, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndStart(NewToolUseBlock2(id, name))

}
func (s *LLMStream) Block(block Block2) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(block)
}
func (s *LLMStream) Delta(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delta(content)
}
func (s *LLMStream) StopReason(stopReason StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopReason = stopReason
}
func (s *LLMStream) Usage(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(NewUsageBlock2(usage))
}
func (s *LLMStream) flushAndAdd(block Block2) {
	if s.receiver != nil {
		s.receiver.AddBlock(block)
	}
	s.assemblerBlock.flush()
}
func (s *LLMStream) flushAndStart(block Block2) {
	if s.receiver != nil {
		s.receiver.AddBlock(NewStartBlock2(block))
	}
	s.assemblerBlock.flush()
	s.assemblerBlock.start(block)
}
func (s *LLMStream) flush() {
	block, fa := s.assemblerBlock.flush()
	if fa {
		s.blocks = append(s.blocks, block)
	}
}
func (s *LLMStream) delta(content string) {
	if s.receiver != nil {
		s.receiver.AddBlock(NewDeltaBlock2(content))
	}
	s.assemblerBlock.delta(content)
}
func (s *LLMStream) ErrorText(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushAndAdd(NewErrorFullTextBlock2(content))
}
func (s *LLMStream) ReadBlocks() Block2s {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	return s.blocks
}
