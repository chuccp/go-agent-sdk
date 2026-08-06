package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

// ToolExecutor 工具执行器接口：定义工具的元数据（发给 LLM）和执行逻辑。
// 执行时入参从 turn.Args() 获取（由工具链按命中的 tool_use 设置），
// 会话上下文从 turn.Context() 获取。
type ToolExecutor interface {
	Definition() *chat.ToolFunction
	Name() string
	Execute(chain ToolsChain, turn *Turn) (string, error)
}
