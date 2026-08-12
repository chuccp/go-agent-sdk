package value

type Object struct {
	Value
	data map[string]Value
}

func NewObject() *Object {
	return &Object{
		data: make(map[string]Value),
	}
}
