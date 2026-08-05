package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

// ToolExecutor 工具执行器接口：定义工具的元数据（发给 LLM）和执行逻辑。
type ToolExecutor interface {
	Filter
	Definition() *chat.ToolFunction
	Execute(args map[string]any) (string, error)
}
