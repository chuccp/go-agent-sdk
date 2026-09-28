// Package jsonx 是 go-web-frame/value 的转发层：只做类型别名与函数转发，
// 不持有自己的实现。
//
// 用类型别名（type alias）而不是包装类型：别名让 *jsonx.Object 在本模块和
// go-web-frame 里是**同一个类型**，两边可以互相传递；包装类型会在两套不兼容的
// Object 之间竖起一道转换墙。使用方往往要把 SDK 回调里拿到的 jsonx.Object
// 直接交给按 go-web-frame 完整版 API 写的代码处理，别名省掉的就是这层转换。
//
// 完整版的入口在这里都能拿到，包括 DecodeJSON / ParseJSON、Lookup 路径查找、
// Any / Stream，调用方不必为了它们直接 import go-web-frame。
//
// 两处偏宽松的语义，使用时留意：
//   - Object.AddAll：两边都是 Object 的键会**递归合并**，而不是直接覆盖。
//   - Object.GetInt / GetNumber：会把 "502" 这类数字字符串转成数字，对模型
//     传参这类场景更省事。
package jsonx

import wf "github.com/chuccp/go-web-frame/value"

// 类型一律别名到完整版：同名类型在本模块与 go-web-frame 中是同一个类型。
type (
	Value     = wf.Value
	ValueBase = wf.ValueBase

	Object = wf.Object
	Array  = wf.Array
	Text   = wf.Text
	Bool   = wf.Bool
	Number = wf.Number
	Null   = wf.Null
	Stream = wf.Stream
	Any    = wf.Any

	DecoderConfig       = wf.DecoderConfig
	DecoderConfigOption = wf.DecoderConfigOption
	LookupOption        = wf.LookupOption
)

// 构造函数与变量：签名与旧实现完全一致，直接转发。
var (
	NullValue = wf.NullValue

	NewObject         = wf.NewObject
	NewObjectFromMap  = wf.NewObjectFromMap
	NewObjectFromJson = wf.NewObjectFromJson
	NewArray          = wf.NewArray
	NewArraySize      = wf.NewArraySize
	NewText           = wf.NewText
	NewBool           = wf.NewBool
	NewNumber         = wf.NewNumber
	NewInt            = wf.NewInt
	NewStream         = wf.NewStream
)

// 完整版独有的入口，一并转发。
var (
	// DecodeJSON / ParseJSON 直接从流或 RawMessage 解析出 Value。
	DecodeJSON = wf.DecodeJSON
	ParseJSON  = wf.ParseJSON

	// Lookup / LookupFirst / LookupAll 走路径表达式，如 "data.data.trainDataList"。
	Lookup      = wf.Lookup
	LookupFirst = wf.LookupFirst
	LookupAll   = wf.LookupAll

	ToNumberE = wf.ToNumberE

	// 路径匹配策略：默认 MatchFlexible（忽略大小写 + snake_case/CamelCase 互转）。
	MatchExact           = wf.MatchExact
	MatchCaseInsensitive = wf.MatchCaseInsensitive
	MatchFlexible        = wf.MatchFlexible

	// 反序列化选项。
	WithTagName          = wf.WithTagName
	WithWeaklyTypedInput = wf.WithWeaklyTypedInput
	WithMatchFieldName   = wf.WithMatchFieldName
)
