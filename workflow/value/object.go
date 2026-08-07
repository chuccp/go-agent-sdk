package value

type Object struct {
	*NodeValue
	data map[string]*NodeValue
}

func NewObject() *Object {
	return &Object{
		data: make(map[string]*NodeValue),
	}
}
