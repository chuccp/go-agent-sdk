package value

import (
	"encoding/json"
	"log"
)

type Object struct {
	ValueBase
	data map[string]Value
}

func (o *Object) PutAny(key string, value any) {
	o.data[key] = fromInterface(value)
}
func (o *Object) Get(key string) Value {
	return o.data[key]
}
func (o *Object) IsEmpty() bool {
	return len(o.data) == 0
}
func (o *Object) GetMustString(key string) string {
	v := o.Get(key)
	if v == nil {
		log.Panic("GetString: " + key + " not found ")
	}

	if v.IsNull() {
		return ""
	}
	return v.String()

}

func (o *Object) GetString(key string) string {
	v := o.Get(key)
	if v == nil {
		return ""
	}
	if v.IsNull() {
		return ""
	}
	return v.String()
}

func (o *Object) HasKey(key string) bool {
	_, ok := o.data[key]
	return ok
}

func (o *Object) GetBool(key string) bool {
	v := o.Get(key)
	if v == nil || !v.IsBool() {
		return false
	}
	return v.AsBool().b
}

func (o *Object) GetNumber(key string) float64 {
	v := o.Get(key)
	if v == nil || !v.IsNumber() {
		return 0
	}
	return v.AsNumber().f
}

func (o *Object) GetInt(key string) int {
	return int(o.GetNumber(key))
}

func (o *Object) GetObject(key string) *Object {
	v := o.Get(key)
	if v == nil || !v.IsObject() {
		return nil
	}
	return v.AsObject()
}

func (o *Object) GetArray(key string) *Array {
	v := o.Get(key)
	if v == nil || !v.IsArray() {
		return nil
	}
	return v.AsArray()
}

// ToMap 将对象转换为原生 map（递归转换嵌套的 Object/Array）。
func (o *Object) ToMap() map[string]any {
	if o == nil {
		return nil
	}
	m := make(map[string]any, len(o.data))
	for k, v := range o.data {
		m[k] = toAny(v)
	}
	return m
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

func (o *Object) MarshalJSON() ([]byte, error) { return o.ToJSON(), nil }

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

// NewObjectFromMap 从原生 map 构建对象。
func NewObjectFromMap(m map[string]any) *Object {
	obj := NewObject()
	for k, v := range m {
		obj.data[k] = fromInterface(v)
	}
	return obj
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
	case []string:
		arr := make([]Value, len(val))
		for index, item := range val {
			arr[index] = &Text{text: item}
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

// toAny 将 Value 还原为原生 Go 值（ToMap 的递归辅助函数）。
func toAny(v Value) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case *Object:
		return val.ToMap()
	case *Array:
		out := make([]any, 0, len(val.data))
		for _, item := range val.data {
			out = append(out, toAny(item))
		}
		return out
	case *Text:
		return val.text
	case *Number:
		return val.f
	case *Bool:
		return val.b
	default:
		return nil
	}
}
