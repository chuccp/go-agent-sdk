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
			arr[i] = json.RawMessage("null")
		} else {
			arr[i] = v.ToJSON()
		}
	}
	data, _ := json.Marshal(arr)
	return data
}

func (a *Array) MarshalJSON() ([]byte, error) { return a.ToJSON(), nil }

func (a *Array) Add(value ...Value) {
	a.data = append(a.data, value...)
}
func (a *Array) AddAny(value any) {
	v := fromInterface(value)
	a.data = append(a.data, v)
}

func (a *Array) Len() int {
	if a == nil {
		return 0
	}
	return len(a.data)
}

func (a *Array) Get(index int) Value {
	if a == nil || index < 0 || index >= len(a.data) {
		return NullValue
	}
	return a.data[index]
}

func (a *Array) Set(index int, value Value) {
	if a == nil || index < 0 || index >= len(a.data) {
		return
	}
	a.data[index] = value
}

func (a *Array) ForEach(fn func(index int, value Value) bool) {
	if a == nil {
		return
	}
	for i, v := range a.data {
		if !fn(i, v) {
			break
		}
	}
}

// Iter 返回一个迭代器函数，支持 Go 1.23+ 的 for-range 语法。
func (a *Array) Iter(yield func(i int, v Value) bool) {
	if a == nil {
		return
	}
	for i, v := range a.data {
		if !yield(i, v) {
			return
		}
	}
}

func (a *Array) StringValues() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.data))
	for _, v := range a.data {
		if v != nil && v.IsText() {
			out = append(out, v.String())
		}
	}
	return out
}

func NewArray(v ...Value) *Array {
	return &Array{
		data: v,
	}
}

func NewArraySize(n int) *Array {
	return &Array{data: make([]Value, n)}
}
