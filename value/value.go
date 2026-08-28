package value

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Value interface {
	IsObject() bool
	IsArray() bool
	IsText() bool
	IsBool() bool
	IsNumber() bool
	IsNull() bool

	IsStream() bool

	AsObject() *Object
	AsArray() *Array
	AsText() *Text
	AsBool() *Bool

	AsStream() *Stream

	AsNumber() *Number

	ToJSON() json.RawMessage
	String() string
}

// ValueBase 提供 Value 接口的默认实现，具体类型只需覆写自身对应的 IsXxx / AsXxx 方法。
type ValueBase struct{}

func (ValueBase) IsObject() bool { return false }
func (ValueBase) IsArray() bool  { return false }
func (ValueBase) IsText() bool   { return false }
func (ValueBase) IsBool() bool   { return false }
func (ValueBase) IsNumber() bool { return false }
func (ValueBase) IsNull() bool   { return false }
func (ValueBase) IsStream() bool { return false }

func (ValueBase) AsObject() *Object       { panic("not an object") }
func (ValueBase) AsArray() *Array         { panic("not an array") }
func (ValueBase) AsText() *Text           { panic("not text") }
func (ValueBase) AsBool() *Bool           { panic("not bool") }
func (ValueBase) AsNumber() *Number       { panic("not number") }
func (ValueBase) AsStream() *Stream       { panic("not Stream") }
func (ValueBase) ToJSON() json.RawMessage { return json.RawMessage("null") }
func (ValueBase) String() string          { return "null" }

type Stream struct {
	ValueBase
	text *strings.Builder
}

func (s *Stream) IsStream() bool { return true }

func NewStream() *Stream {
	return &Stream{
		text: new(strings.Builder),
	}
}
func (s *Stream) AsStream() *Stream {
	return s
}

// WriteString 向流中追加文本内容。
func (s *Stream) WriteString(p string) {
	s.text.WriteString(p)
}
func (s *Stream) ToJSON() json.RawMessage {
	return json.RawMessage(s.text.String())
}

// Text 返回流中已累积的文本内容。
func (s *Stream) Text() string {
	return s.text.String()
}

func (s *Stream) String() string {
	return s.text.String()
}

// Len 返回流中已累积的文本长度。
func (s *Stream) Len() int {
	return s.text.Len()
}
func (s *Stream) IsEmpty() bool {
	return s.text.Len() == 0
}

// Reset 清空流中已累积的内容。
func (s *Stream) Reset() {
	s.text.Reset()
}

type Text struct {
	ValueBase
	text string
}

func (t *Text) IsText() bool { return true }

func (t *Text) AsText() *Text { return t }

func (t *Text) String() string { return t.text }

func (t *Text) ToJSON() json.RawMessage {
	data, _ := json.Marshal(t.text)
	return data
}

func (t *Text) MarshalJSON() ([]byte, error) { return t.ToJSON(), nil }

func NewText(text string) *Text {
	return &Text{text: text}
}

type Number struct {
	ValueBase
	i       int64
	f       float64
	isFloat bool
}

func (n *Number) IsNumber() bool { return true }

func (n *Number) AsNumber() *Number { return n }

func (n *Number) IsFloat() bool { return n.isFloat }

// Int64 返回整数值。若为浮点数则截断小数部分。
func (n *Number) Int64() int64 {
	if n.isFloat {
		return int64(n.f)
	}
	return n.i
}

// Float64 返回浮点值。若为整数则转换为 float64。
func (n *Number) Float64() float64 {
	if n.isFloat {
		return n.f
	}
	return float64(n.i)
}

func (n *Number) String() string {
	if n.isFloat {
		return fmt.Sprintf("%v", n.f)
	}
	return fmt.Sprintf("%d", n.i)
}

func (n *Number) ToJSON() json.RawMessage {
	if n.isFloat {
		data, _ := json.Marshal(n.f)
		return data
	}
	data, _ := json.Marshal(n.i)
	return data
}

func (n *Number) MarshalJSON() ([]byte, error) { return n.ToJSON(), nil }

// NewNumber 从 float64 创建浮点数值。
func NewNumber(f float64) *Number {
	return &Number{f: f, isFloat: true}
}

// NewInt 从 int64 创建整数值。
func NewInt(i int64) *Number {
	return &Number{i: i}
}

type Bool struct {
	ValueBase
	b bool
}

func (b *Bool) IsBool() bool { return true }

func (b *Bool) AsBool() *Bool { return b }

func (b *Bool) String() string { return fmt.Sprintf("%v", b.b) }

func (b *Bool) ToJSON() json.RawMessage {
	if b.b {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func (b *Bool) MarshalJSON() ([]byte, error) { return b.ToJSON(), nil }

func NewBool(b bool) *Bool {
	return &Bool{b: b}
}

type Null struct {
	ValueBase
}

func (n *Null) IsNull() bool { return true }

func (n *Null) String() string { return "null" }

func (n *Null) ToJSON() json.RawMessage { return json.RawMessage("null") }

func (n *Null) MarshalJSON() ([]byte, error) { return n.ToJSON(), nil }

// NullValue 空值单例。
var NullValue = &Null{}

// 确保各类型实现 Value 接口。
var _ Value = (*Null)(nil)
var _ Value = (*Text)(nil)
var _ Value = (*Bool)(nil)
var _ Value = (*Number)(nil)
var _ Value = (*Object)(nil)
var _ Value = (*Array)(nil)
var _ Value = (*Stream)(nil)
