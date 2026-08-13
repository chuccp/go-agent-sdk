package value

import "strings"

type Value interface {
}

type Stream struct {
	Value
	text *strings.Builder
}

func NewStream() *Stream {
	return &Stream{
		text: new(strings.Builder),
	}
}

// WriteString 向流中追加文本内容。
func (s *Stream) WriteString(p string) (int, error) {
	return s.text.WriteString(p)
}

// Text 返回流中已累积的文本内容。
func (s *Stream) Text() string {
	return s.text.String()
}

// Len 返回流中已累积的文本长度。
func (s *Stream) Len() int {
	return s.text.Len()
}

// Reset 清空流中已累积的内容。
func (s *Stream) Reset() {
	s.text.Reset()
}
