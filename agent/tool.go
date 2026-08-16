package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// ToolExecutor 工具执行器接口：定义工具的元数据（发给 LLM）和执行逻辑。
// 执行时入参从 turn.Args() 获取（由 executeTools 按命中的 tool_use 设置），
// 会话上下文从 turn.Context() 获取；输出内容块写入统一的 chat.BlockStream；
// 错误不向外返回：仅回传给模型的错误经 writer.WriteErrorText 以文本写入，
// 需要被 GetError 识别的类型化错误用 writer.WriteError（ErrorBlock）。
type ToolExecutor interface {
	Definition() *chat.ToolFunction
	Name() string
	UsagePrompt() string
	Execute(turn *Turn, writer *chat.BlockStream)
}

// Turn 一次工具执行的载体。
type Turn struct {
	ctx  *SessionContext
	args *value.Object
}

// Context 返回本次执行所属的会话上下文。
func (t *Turn) Context() *SessionContext { return t.ctx }

// Args 返回当前执行的 tool_use 入参。
func (t *Turn) Args() *value.Object { return t.args }

// NewTurn 构造一个独立的 Turn（不绑定会话上下文），用于工具单元测试等场景。
func NewTurn(args *value.Object) *Turn {
	return &Turn{args: args}
}

// NewTurnWithContext 构造绑定会话上下文的 Turn（测试/集成场景直接驱动工具）。
func NewTurnWithContext(ctx *SessionContext, args *value.Object) *Turn {
	return &Turn{ctx: ctx, args: args}
}

// toolArgsDisplay 生成工具入参的展示文本，与前端历史展示逻辑保持一致：
// 优先使用 command 字段（如命令行工具），否则输出入参 JSON。
func toolArgsDisplay(args *value.Object) string {
	if args == nil {
		return ""
	}
	if cmd := args.GetString("command"); cmd != "" {
		return cmd
	}
	if args.IsEmpty() {
		return ""
	}
	return args.String()
}
