package agent

import (
	"fmt"
	"log"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// MessageFilter 消息过滤器；不调用 chain.Next 即消费该消息。
type MessageFilter interface {
	HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error
}

// MessageFilterChain 消息过滤器链，Next 推进到下一个过滤器。
type MessageFilterChain interface {
	Next() error
}

// messageFilterChain 消息过滤器链实现。
// 过滤器按注册顺序执行，核心主体过滤器位于链尾（最内层）。
type messageFilterChain struct {
	index                int
	defaultMessageFilter MessageFilter
	messageFilters       []MessageFilter
	msg                  *QueuedMessage
}

func newMessageFilterChain(msg *QueuedMessage, defaultMessageFilter MessageFilter, messageFilters ...MessageFilter) *messageFilterChain {
	return &messageFilterChain{
		messageFilters:       messageFilters,
		defaultMessageFilter: defaultMessageFilter,
		msg:                  msg,
		index:                -1,
	}
}

// Next 推进到下一个过滤器；消息由链自身持有，过滤器无需再传递。
func (c *messageFilterChain) Next() error {
	if c.index < len(c.messageFilters)-1 {
		c.index++
		return c.messageFilters[c.index].HandleRevMessage(c, c.msg)
	}
	return c.defaultMessageFilter.HandleRevMessage(c, c.msg)
}

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

// coreMessageFilter 核心主体过滤器：消息链的最内层。
// 用户消息入队并按需启动会话主循环（LLM 轮次由 messageProcessor.executeRound 驱动）。
type coreMessageFilter struct {
}

func newCoreMessageFilter() *coreMessageFilter {
	return &coreMessageFilter{}
}

// HandleRevMessage 消息链终端：入队并启动主循环。调用时已持有 runLock。
func (core *coreMessageFilter) HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error {
	ctx := msg.ctx
	ctx.runLock.Lock()
	defer ctx.runLock.Unlock()
	err := ctx.inbox.Write(msg)
	if err != nil {
		log.Printf("[chatSession] inbox write failed: %v", err)
		return err
	}
	if !ctx.running {
		ctx.runCtx, ctx.cancel = newRunContext()
		ctx.running = true
		ctx.AddEvent(chat.NewMessageSentEvent(msg.id, ctx.sessionId, msg.msg))
		util.GoWithRecover(func() {
			ctx.doLoop()
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := chat.NewErrorEvent("internal error")
			evt.Done = true
			ctx.AddEvent(evt)
		})
	} else {
		ctx.AddEvent(chat.NewMessageQueuedEvent(msg.id, ctx.sessionId, msg.msg))
	}
	return nil
}
