package value

import "strings"

type Text struct {
	Value
	text *strings.Builder
}

func NewText() *Text {
	return &Text{
		text: new(strings.Builder),
	}
}
