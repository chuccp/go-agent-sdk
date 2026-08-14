package value

import "encoding/json"

type Object struct {
	ValueBase
	data map[string]Value
}

func (o *Object) PutAny(key string, value any) {
	o.data[key] = fromInterface(value)
}

func (o *Object) IsObject() bool { return true }

func (o *Object) AsObject() *Object { return o }

func (o *Object) String() string { return string(o.ToJSON()) }

func (o *Object) ToJSON() json.RawMessage {
	m := make(map[string]json.RawMessage, len(o.data))
	for k, v := range o.data {
		if v == nil {
			m[k] = json.RawMessage("null")
		} else {
			m[k] = v.ToJSON()
		}
	}
	data, _ := json.Marshal(m)
	return data
}

// PutJson 解析 JSON 并填充到对象中。
func (o *Object) PutJson(dataJson []byte) error {
	var m map[string]any
	if err := json.Unmarshal(dataJson, &m); err != nil {
		return err
	}
	o.data = make(map[string]Value, len(m))
	for k, v := range m {
		o.data[k] = fromInterface(v)
	}
	return nil
}

func NewObject() *Object {
	return &Object{
		data: make(map[string]Value),
	}
}

// fromInterface 将 JSON 反序列化得到的原生值转换为对应的 Value 类型。
func fromInterface(v any) Value {
	switch val := v.(type) {
	case Value:
		return val
	case nil:
		return NullValue
	case bool:
		return NewBool(val)
	case map[string]any:
		obj := NewObject()
		for k, item := range val {
			obj.data[k] = fromInterface(item)
		}
		return obj
	case []any:
		arr := make([]Value, len(val))
		for index, item := range val {
			arr[index] = fromInterface(item)
		}
		return NewArray(arr...)
	case float64:
		return NewNumber(val)
	case float32:
		return NewNumber(float64(val))
	case int:
		return NewNumber(float64(val))
	case int8:
		return NewNumber(float64(val))
	case int16:
		return NewNumber(float64(val))
	case int32:
		return NewNumber(float64(val))
	case int64:
		return NewNumber(float64(val))
	case uint:
		return NewNumber(float64(val))
	case uint8:
		return NewNumber(float64(val))
	case uint16:
		return NewNumber(float64(val))
	case uint32:
		return NewNumber(float64(val))
	case uint64:
		return NewNumber(float64(val))
	case string:
		return &Text{text: val}
	default:
		return NullValue
	}
}
