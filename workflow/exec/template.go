package exec

import (
	"encoding/json"
	"strconv"
	"strings"
)

// RenderTemplate 渲染节点模板：将 {{path}} 占位符替换为 vars 中的值。
// path 支持点号嵌套（如 {{item.title}}、{{split.segments}}），
// 顶层键直接命中 vars，嵌套路径逐级下钻（map[string]any / []any 数字下标）。
// 未命中的占位符原样保留（便于排查缺参）。
func RenderTemplate(tpl string, vars map[string]any) string {
	var sb strings.Builder
	for rest := tpl; ; {
		start := strings.Index(rest, "{{")
		if start < 0 {
			sb.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			sb.WriteString(rest)
			break
		}
		sb.WriteString(rest[:start])
		path := strings.TrimSpace(rest[start+2 : start+end])
		if v, ok := ResolvePath(vars, path); ok {
			sb.WriteString(FormatValue(v))
		} else {
			sb.WriteString(rest[start : start+end+2])
		}
		rest = rest[start+end+2:]
	}
	return sb.String()
}

// ResolvePath 按点号路径在嵌套结构中取值。
func ResolvePath(vars map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	if v, ok := vars[path]; ok { // 整键命中优先（键本身可能含点）
		return v, true
	}
	segments := strings.Split(path, ".")
	var cur any = vars
	for _, seg := range segments {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// FormatValue 把任意值格式化为模板文本：字符串原样，其余 JSON 序列化。
func FormatValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		data, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(data)
	}
}
