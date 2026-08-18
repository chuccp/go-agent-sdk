package chat

import "github.com/chuccp/go-agent-sdk/value"

type BlockReceiver interface {
	AddBlock(block Block2)
}

type assemblerBlock struct {
	stream *value.Stream
	block  Block2
}

type LLMStream struct {
	usage      *Usage
	stopReason StopReason
	receiver   BlockReceiver
	blocks     []Block2
}

func NewLLMStream(receiver BlockReceiver) *LLMStream {
	return &LLMStream{
		receiver: receiver,
	}
}

func (s *LLMStream) BlockTextStart() {
	if s.receiver != nil {
		s.receiver.AddBlock(NewStartBlock2(NewTextBlock2()))
	}

}
func (s *LLMStream) BlockErrorTextStart() {
	if s.receiver != nil {
		s.receiver.AddBlock(NewErrorTextBlock2())
	}
}
func (s *LLMStream) BlockThinkingStart() {
	if s.receiver != nil {
		s.receiver.AddBlock(NewErrorTextBlock2())
	}

}
func (s *LLMStream) BlockToolUseStart(id string, name string) {

}
func (s *LLMStream) Block(block Block2) {

}
func (s *LLMStream) Delta(content string) {

}
func (s *LLMStream) StopReason(stopReason StopReason) {

}
func (s *LLMStream) Usage(usage *Usage) {

}
func (s *LLMStream) ErrorText(error error) {

}
func (s *LLMStream) ReadBlocks() Block2s {
	return nil
}
