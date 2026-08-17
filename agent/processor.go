package agent

import (
	"context"
	"fmt"
	"log"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
	"github.com/chuccp/go-agent-sdk/value"
)

// QueuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
type QueuedMessage struct {
	ctx  *SessionContext
	id   uint64
	msg  *chat.RevMessage
	opts []chat.Option // 本次消息附带的per-turn选项覆盖
}

// Msg 返回包装的原始用户消息。
func (qm *QueuedMessage) Msg() *chat.RevMessage { return qm.msg }

// Context 返回消息所属的会话上下文（通过它访问会话能力）。
func (qm *QueuedMessage) Context() *SessionContext { return qm.ctx }

// messageProcessor 会话编排器：接收用户消息后入队并驱动会话主循环，
// 主循环执行单轮 LLM 交互（executeRound）与工具执行（executeTools）。
// 会话状态集中于 SessionContext。
type messageProcessor struct {
	ctx           *SessionContext
	toolExecutors []ToolExecutor
}

func newMessageProcessor(sessionContext *SessionContext) *messageProcessor {
	p := &messageProcessor{
		ctx:           sessionContext,
		toolExecutors: sessionContext.toolExecutors,
	}
	return p
}

func (p *messageProcessor) HandleRevMessage(message *chat.RevMessage, opt ...chat.Option) error {
	ctx := p.ctx
	ctx.runLock.Lock()
	defer ctx.runLock.Unlock()
	qm := &QueuedMessage{
		id:   p.ctx.getSeq(),
		ctx:  p.ctx,
		msg:  message,
		opts: opt,
	}
	err := ctx.inbox.Write(qm)
	if err != nil {
		log.Printf("[chatSession] inbox write failed: %v", err)
		return err
	}
	if !ctx.running {
		ctx.runCtx, ctx.cancel = context.WithCancel(context.Background())
		ctx.running = true
		ctx.AddEvent(chat.NewMessageSentEvent(qm.id, qm.msg))
		util.GoWithRecover(func() {
			p.doLoop()
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := chat.NewErrorEvent("internal error")
			evt.Done = true
			ctx.AddEvent(evt)
		})
	} else {
		ctx.AddEvent(chat.NewMessageQueuedEvent(qm.id, qm.msg))
	}
	return nil
}

// doLoop 会话主循环：执行单轮 LLM 交互（executeRound：构建请求、流式调用）与
// 工具执行（executeTools），轮次返回后根据 stopReason 完成收尾，
// 直到会话停止（running=false）、被取消（runCtx.Done）或轮次返回错误
// （executeRound 已完成清理与 error 事件）。
// 锁协议：循环全程持有 runLock；executeRound / executeTools 内部在
// LLM 调用与工具执行期间自行释放/重取。
func (p *messageProcessor) doLoop() {
	ctx := p.ctx
	ctx.runLock.Lock()
	defer ctx.runLock.Unlock()
	for ctx.running {
		// 检查取消
		select {
		case <-ctx.runCtx.Done():
			ctx.drainInbox()
			ctx.saveAndReset()
			ctx.running = false
			return
		default:
		}

		blocks, stopReason, err := p.executeRound()
		if err != nil {
			log.Printf("[chatSession] turn ended with error: %v", err)
			return
		}
		if blocks == nil {
			// inbox 为空，executeRound 已将 running 置 false
			continue
		}

		if stopReason == chat.StopReasonToolUse {
			// assistant 消息入历史
			ctx.appendAssistantMessage(blocks)
			// tool_result 作为 user 消息入历史；未命中工具的 tool_use 已在 executeTools 补错误结果
			results, toolStop := p.executeTools(ctx, blocks)
			if toolStop == chat.StopReasonUserWait {
				// 工具请求暂停（如 ask_user_question 等待用户回答）：本轮到此结束。
				// done 在 tool_result 历史写入前发出，被其 Offset 覆盖，
				// 前端根据历史计算的 start 落在 done 之后，重连时不重放残留的 done
				ctx.AddEvent(chat.NewDoneEvent())
			}
			ctx.events.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: results})
			if toolStop != chat.StopReasonUserWait {
				// 继续循环：携带 tool_result 再次调用 LLM
				continue
			}
		} else {
			// 先发 done 事件再写 assistant 历史：消息的 Offset 即可覆盖 done，
			// 前端根据历史计算的 start 会落在 done 之后，重连时不重放残留的 done
			ctx.AddEvent(chat.NewDoneEvent())
			ctx.appendAssistantMessage(blocks)
		}

		// 收尾（end_turn / 工具暂停）：保存历史；inbox 还有消息则继续循环，否则退出
		ctx.saveAndReset()
		if ctx.inbox.IsEmpty() {
			ctx.running = false
		}
	}
}

// executeRound 执行一轮 LLM 交互：排干 inbox 构建请求、流式调用并收集内容块。
// 返回 nil blocks 表示 inbox 为空（已将 running 置 false）。
// 锁协议：进入时持有 runLock，LLM 网络调用期间释放，返回前恢复持锁。
func (p *messageProcessor) executeRound() (chat.Blocks, chat.StopReason, error) {
	ctx := p.ctx
	// 排干 inbox，构建请求（持有 runLock）
	request := ctx.buildRequest()
	if request == nil {
		ctx.running = false
		return nil, "", nil
	}

	// ===== 释放锁：LLM 网络调用（耗时操作，不持锁） =====
	ctx.runLock.Unlock()

	// ChatWithStream 内部创建独享 BlockStream（组装 Block、推送增量事件），
	// 同步完成后一次性返回全部结果
	blocks, stopReason, callErr := ctx.ChatWithStream(request)

	// ===== 重新持锁 =====
	ctx.runLock.Lock()

	if callErr != nil {
		ctx.drainInbox()
		ctx.saveAndReset()
		ctx.running = false

		evt := chat.NewErrorEvent(callErr.Error())
		evt.Done = true
		ctx.AddEvent(evt)
		return nil, "", callErr
	}
	return blocks, stopReason, nil
}

// executeTools 逐个执行本轮 tool_use 命中的工具，返回 tool_result blocks 及工具轮次的停止原因
// （默认 StopReasonToolResult；任一工具置 StopReasonUserWait 则返回 UserWait，通知 doLoop 本轮暂停）。
// 每个工具的输出（含执行错误，工具自行写入）写入各自的 BlockStream，
// 同步执行完成后一次性取回；本方法消费每个工具的输出并组装为 tool_result，
// 未命中任何工具的 tool_use 补错误结果（避免下一轮请求缺 tool_result 报错）。
// 锁协议：进入时持有 runLock；工具执行期间由 runTool 自行释放/重取，返回时保持持锁。
func (p *messageProcessor) executeTools(ctx *SessionContext, blocks chat.Blocks) (chat.Blocks, chat.StopReason) {
	var results chat.Blocks
	// 本轮工具执行的停止原因：默认 ToolResult（继续携带 tool_result 调用 LLM），
	// 若任一工具置 UserWait（如 ask_user_question 等待用户回答），则本轮暂停
	stopReason := chat.StopReasonToolResult
	for _, block := range blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		exec := p.findExecutor(tu.Name)
		if exec == nil {
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewTextBlock(fmt.Sprintf("未知工具: %s", tu.Name))},
			))
			continue
		}
		blocks, toolStop := p.runTool(ctx, tu, exec)
		results = append(results, p.collectToolResult(ctx, tu, blocks))
		if toolStop == chat.StopReasonUserWait {
			stopReason = chat.StopReasonUserWait
		}
	}
	return results, stopReason
}

// runTool 执行单个工具：输出内容块写入统一的 chat.BlockStream；
// 执行错误由工具以文本写入（不中断会话），随输出一起组装为 tool_result。
// 锁协议：调用方持有 runLock，工具执行（外部 I/O）期间释放，返回前恢复持锁。
func (p *messageProcessor) runTool(ctx *SessionContext, tu *chat.ToolUseBlock, exec ToolExecutor) (chat.Blocks, chat.StopReason) {

	turn := &Turn{ctx: ctx, args: tu.Input}

	ctx.runLock.Unlock()
	writer := chat.NewBlockStream(ctx)
	// 工具轮次默认停止原因为 ToolResult（已产出 tool_result，继续调用 LLM）；
	// 需要暂停的工具（如 ask_user_question）在 Execute 内覆盖为 UserWait
	writer.StopReason(chat.StopReasonToolResult)
	exec.Execute(turn, writer)
	ctx.runLock.Lock()
	return writer.ReadBlocks(), writer.GetStopReason()
}

// collectToolResult 取回单个工具的全部输出：文本拼接为结果正文，
// 其余 block 原样保留，组装为 tool_result block；同时发出 tool_execution 事件。
func (p *messageProcessor) collectToolResult(ctx *SessionContext, tu *chat.ToolUseBlock, blocks chat.Blocks) *chat.ToolResultBlock {
	text := value.NewStream()
	var content chat.Blocks
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			text.WriteString(tb.Text)
			continue
		}
		content = append(content, b)
	}
	if text.IsEmpty() {
		text.WriteString("(无输出)")
	}
	resultText := text.String()
	ctx.AddEvent(chat.NewToolExecutionEvent(tu.Name, toolArgsDisplay(tu.Input), resultText))
	content = append(chat.Blocks{chat.NewTextBlock(resultText)}, content...)
	return chat.NewToolResultBlock(tu.ID, content)
}

// findExecutor 按名称查找已注册的工具执行器。
func (p *messageProcessor) findExecutor(name string) ToolExecutor {
	for _, exec := range p.toolExecutors {
		if exec.Name() == name {
			return exec
		}
	}
	return nil
}

// Stop 取消当前正在运行的会话主循环。
func (p *messageProcessor) Stop() {
	ctx := p.ctx
	ctx.runLock.Lock()
	if ctx.cancel != nil {
		ctx.cancel()
	}
	ctx.runLock.Unlock()
}
