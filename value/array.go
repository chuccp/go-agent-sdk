package value

type Array struct {
	ValueBase
	data []Value
}

func (a *Array) IsArray() bool { return true }

func (a *Array) AsArray() *Array { return a }

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
