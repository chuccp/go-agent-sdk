package chat

type LLMStream struct {
	blockStream *BlockStream
	usage       *Usage
	stopReason  StopReason
}

func (s *LLMStream) BlockTextStart() {

}
func (s *LLMStream) BlockThinkingStart() {

}
func (s *LLMStream) BlockToolUseStart(id string, name string) {

}
func (s *LLMStream) Delta(content string) {

}
func (s *LLMStream) StopReason(stopReason StopReason) {

}
func (s *LLMStream) Usage(usage *Usage) {

}
func (s *LLMStream) ErrorText(usage *Usage) {

}
