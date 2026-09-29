package agent

import (
	"context"
	"errors"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/jsonx"
)

type Tools []*Tool
type Tool struct {
	exec ToolExecutor
	tu   *chat.ToolUseBlock
}

func (t *Tool) Name() string {
	return t.tu.Name
}
func (t *Tool) Input() *jsonx.Object {
	return t.tu.Input
}

var UnknownToolError = errors.New("unknown tool")

func (t *Tool) Execute(turn *Turn, writer *chat.ToolResultBlockStream) error {
	if t.exec == nil {
		return UnknownToolError
	}
	t.exec.Execute(turn, writer)
	return nil
}
func (t *Tool) ID() string {
	return t.tu.ID
}
func NewTool(exec ToolExecutor, tu *chat.ToolUseBlock) *Tool {
	return &Tool{
		exec: exec,
		tu:   tu}
}

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
	ctx   Context
	args  *jsonx.Object
	tools Tools
}

// AgentContext 返回本次执行所属的会话上下文。
func (t *Turn) AgentContext() Context { return t.ctx }

// Context 返回本次执行的 context.Context（取自 NewTurnWithContext 绑定的会话上下文）。
func (t *Turn) Context() context.Context { return t.ctx.Ctx() }

// Args 返回当前执行的 tool_use 入参。
func (t *Turn) Args() *jsonx.Object { return t.args }
func (t *Turn) Tools() Tools        { return t.tools }

// ToolNum 返回本轮工具列表的长度（不含因轮次已停止而跳过的 tool_use）。
func (t *Turn) ToolNum() int {
	return len(t.tools)
}

// NewTurnWithContext 构造绑定会话上下文的 Turn（测试/集成场景直接驱动工具）。
func NewTurnWithContext(ctx Context, args *jsonx.Object, tools Tools) *Turn {
	return &Turn{
		ctx: ctx, args: args,
		tools: tools}
}

// toolArgsDisplay 生成工具入参的展示文本，与前端历史展示逻辑保持一致：
// 优先使用 command 字段（如命令行工具），否则输出入参 JSON。
func toolArgsDisplay(args *jsonx.Object) string {
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
