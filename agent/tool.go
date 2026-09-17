package agent

import (
	"context"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// ToolExecutor 工具执行器接口：定义工具的元数据（发给 LLM）和执行逻辑。
// 执行时入参从 turn.Args() 获取（由 executeTools 按命中的 tool_use 设置），
// 会话上下文从 turn.AgentContext() 取、纯 context 从 turn.Context() 取；
// 输出内容块写入统一的 chat.BlockStream；
// 错误不向外返回，经 writer.WriteErrorText 以文本写入（随 tool_result 回传给模型）。
type ToolExecutor interface {
	Definition() *chat.ToolFunction
	Name() string
	UsagePrompt() string
	Execute(turn *Turn, writer *chat.ToolResultBlockStream)
}

// Turn 一次工具执行的载体。
type Turn struct {
	ctx  Context
	args *value.Object
}

// AgentContext 返回本次执行所属的会话上下文。
func (t *Turn) AgentContext() Context { return t.ctx }

// Context 返回本次执行的 context.Context。
//
// 没绑会话上下文时（调用方给的是 nil）回落到 context.Background()：工具把 nil 递给
// net/http 这类地方就是一句 nil pointer dereference，整个工具直接崩，
// 与 RunContext.Ctx() 靠 pContext 兜底是同一个理由——喂给工具的这个 ctx 必须是能用的。
func (t *Turn) Context() context.Context {
	if t.ctx == nil {
		return context.Background()
	}
	return t.ctx.Ctx()
}

// Args 返回当前执行的 tool_use 入参。
func (t *Turn) Args() *value.Object { return t.args }

// NewTurnWithContext 构造绑定会话上下文的 Turn（测试/集成场景直接驱动工具）。
func NewTurnWithContext(ctx Context, args *value.Object) *Turn {
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
