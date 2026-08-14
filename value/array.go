package value

import "encoding/json"

type Array struct {
	ValueBase
	data []Value
}

func (a *Array) IsArray() bool { return true }

func (a *Array) AsArray() *Array { return a }

func (a *Array) String() string { return string(a.ToJSON()) }

func (a *Array) ToJSON() json.RawMessage {
	arr := make([]json.RawMessage, len(a.data))
	for i, v := range a.data {
		if v == nil {
			arr[i] = json.RawMessage("nil")
		} else {
			arr[i] = v.ToJSON()
		}
	}
	data, _ := json.Marshal(arr)
	return data
}

func (a *Array) Add(value ...Value) {
	a.data = append(a.data, value...)
}
func (a *Array) AddAny(value any) {
	v := fromInterface(value)
	a.data = append(a.data, v)
}

func NewArray(v ...Value) *Array {
	return &Array{
		data: v,
	}
}
