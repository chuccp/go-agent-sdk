package agent

import (
	"encoding/json"

	"github.com/chuccp/go-agent-sdk/chat"
)

// ToolExecutor 工具执行器接口：定义工具的元数据（发给 LLM）和执行逻辑。
// 执行时入参从 turn.Args() 获取（由 executeTools 按命中的 tool_use 设置），
// 会话上下文从 turn.Context() 获取；输出内容块流式写入 writer（由 Response 拼接组合）。
type ToolExecutor interface {
	Definition() *chat.ToolFunction
	Name() string
	Execute(turn *Turn, writer chat.StreamWriter) error
}

// Turn 一次工具执行的载体。
type Turn struct {
	ctx  *SessionContext
	args map[string]any // 当前执行的 tool_use 入参
}

// Context 返回本次执行所属的会话上下文。
func (t *Turn) Context() *SessionContext { return t.ctx }

// Args 返回当前执行的 tool_use 入参。
func (t *Turn) Args() map[string]any { return t.args }

// AnswerConsumer 由执行期间会消费用户消息的工具实现（如 ask_user_question）。
// 问答等待机制由工具按 sessionId 自行管理；doLoop 在 tool_result 入历史后
// 调用 TakeConsumedAnswer 取出被消费的回答再入历史（顺序要求：回答必须
// 位于 tool_result 之后，否则触发 Anthropic 校验错误）。
type AnswerConsumer interface {
	TakeConsumedAnswer(sessionId string) *chat.RevMessage
}

// toolArgsDisplay 生成工具入参的展示文本，与前端历史展示逻辑保持一致：
// 优先使用 command 字段（如命令行工具），否则输出入参 JSON。
func toolArgsDisplay(args map[string]any) string {
	if cmd, ok := args["command"].(string); ok && cmd != "" {
		return cmd
	}
	if len(args) == 0 {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(data)
}
