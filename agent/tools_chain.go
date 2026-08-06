package agent

import (
	"encoding/json"
	"fmt"

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

// Turn 一轮 LLM 交互的载体，在工具链中传递。
type Turn struct {
	ctx  *SessionContext
	args map[string]any // 当前执行的 tool_use 入参（由工具链逐个设置）
}

// Context 返回本轮所属的会话上下文。
func (t *Turn) Context() *SessionContext { return t.ctx }

// Args 返回当前执行的 tool_use 入参。
func (t *Turn) Args() map[string]any { return t.args }

// ToolsChain 工具执行链，Next 推进执行下一个工具执行器。
type ToolsChain interface {
	Next() error
}

// toolsChain 工具执行链实现：按注册顺序遍历工具执行器，
// 逐个将本轮 tool_use blocks 按名称匹配，命中则设置 turn.args 并执行，
// 执行结果累积为 tool_result blocks。
type toolsChain struct {
	index         int
	toolExecutors []ToolExecutor
	turn          *Turn
	blocks        chat.Blocks // 本轮 LLM 响应内容块（含 tool_use）
	results       chat.Blocks // 执行累积的 tool_result blocks
}

func newToolsChain(turn *Turn, blocks chat.Blocks, toolExecutors ...ToolExecutor) *toolsChain {
	return &toolsChain{
		turn:          turn,
		blocks:        blocks,
		index:         -1,
		toolExecutors: toolExecutors,
	}
}

// Results 返回链上累积的 tool_result blocks。
func (c *toolsChain) Results() chat.Blocks { return c.results }

// Next 推进到下一个工具执行器：本轮 tool_use 命中它时逐个执行
// （每次执行前设置 turn.args），结果累积到链上，随后自动推进后续执行器。
// 锁协议：调用方持有 runLock，工具执行（外部 I/O）期间释放，返回前恢复持锁。
func (c *toolsChain) Next() error {
	if c.index >= len(c.toolExecutors)-1 {
		return nil
	}
	c.index++
	exec := c.toolExecutors[c.index]
	ctx := c.turn.ctx
	for _, block := range c.blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok || tu.Name != exec.Name() {
			continue
		}
		args, _ := tu.Input.(map[string]any)
		c.turn.args = args

		// 工具执行属于外部 I/O，释放锁（与 LLM 调用同理）
		ctx.runLock.Unlock()
		output, execErr := exec.Execute(c, c.turn)
		ctx.runLock.Lock()

		ctx.AddEvent(chat.NewToolExecutionEvent(tu.Name, toolArgsDisplay(args), output, ctx.sessionId))

		resultText := output
		if execErr != nil {
			resultText = fmt.Sprintf("错误: %v", execErr)
		}
		c.results = append(c.results, chat.NewToolResultBlock(
			tu.ID,
			chat.Blocks{chat.NewTextBlock(resultText)},
		))
	}
	return c.Next()
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
