// Package value 是 go-web-frame/value 的转发层。
//
// 本包原先持有一份独立实现，与 go-web-frame/value 是同一份代码的两个副本。
// 两边各自演进后分叉成了「简版」（本包，67 个方法）和「完整版」
// （go-web-frame/value，108 个方法，另有 DecodeJSON / ParseJSON / Lookup
// 路径查找 / Any / Stream 这些能力）。现在统一到完整版：本包只做类型别名与
// 函数转发，不再持有自己的实现。
//
// 为什么用类型别名（type alias）而不是包装类型：别名让 *value.Object 在本模块
// 和 go-web-frame 里是**同一个类型**，两边可以互相传递；包装类型会在两套不兼容
// 的 Object 之间竖起一道转换墙。使用方往往要把 SDK 回调里拿到的 value.Object
// 直接交给按 go-web-frame 完整版 API 写的代码处理，别名省掉的就是这层转换。
//
// 与旧实现相比有两处语义放宽（都是放宽、不是收紧），迁移时留意：
//   - Object.AddAll：两边都是 Object 的键会**递归合并**，旧实现是直接覆盖。
//     影响面很小——SDK 内只有 workflow 的变量合并用到（3 处），变量值基本都是标量。
//   - Object.GetInt / GetNumber：会把 "502" 这类数字字符串转成数字，旧实现对
//     非 Number 类型一律返回默认值。对模型传参这类场景反而更稳。
//
// 完整版的额外入口现在也能从这里拿到，调用方不必为了它们直接 import go-web-frame。
package value

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
