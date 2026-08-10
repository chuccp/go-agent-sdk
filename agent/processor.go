package agent

import (
	"fmt"
	"log"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// messageProcessor 会话编排器：接收用户消息后交给消息过滤器链（message_chain），
// 会话主循环驱动单轮 LLM 交互（executeRound）与工具执行（executeTools）。
// 消息链主体位于 coreMessageFilter（链的最内层），会话状态集中于 SessionContext。
type messageProcessor struct {
	ctx            *SessionContext
	messageFilters []MessageFilter
	toolExecutors  []ToolExecutor
}

func newMessageProcessor(sessionContext *SessionContext) *messageProcessor {
	p := &messageProcessor{
		ctx:            sessionContext,
		messageFilters: make([]MessageFilter, 0),
		toolExecutors:  sessionContext.toolExecutors,
	}
	sessionContext.processorHandler = p
	// 实现了 MessageFilter 的工具（如 ask_user_question）注册到消息链，
	// 用于拦截用户回答；工具执行由 doLoop 直接驱动（executeTools）。
	for _, exec := range sessionContext.toolExecutors {
		if mf, ok := exec.(MessageFilter); ok {
			p.messageFilters = append(p.messageFilters, mf)
		}
	}
	return p
}

// handleMessage 接收一条用户消息：包装为 QueuedMessage 后交给消息过滤器链。
// 链的最内层（coreMessageFilter）负责入队并按需启动主循环；
// 实现了 MessageFilter 的工具（如 ask_user_question）可在链上拦截消费。
func (p *messageProcessor) handleMessage(message *chat.RevMessage, opt ...chat.Option) error {
	qm := &QueuedMessage{
		id:   p.ctx.getSeq(),
		ctx:  p.ctx,
		msg:  message,
		opts: opt,
	}
	messageFilterChain := newMessageFilterChain(qm, newCoreMessageFilter(), p.messageFilters...)
	return messageFilterChain.Next()
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
		// ── 轮次收尾（持 runLock）──
		switch stopReason {
		case chat.StopReasonToolUse:
			// assistant 消息入历史
			ctx.appendAssistantMessage(blocks)
			// tool_result 作为 user 消息入历史；未命中工具的 tool_use 已在 executeTools 补错误结果
			results := p.executeTools(ctx, blocks)
			ctx.events.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: results})
			// 工具消费的用户回答（如 ask_user，问答机制由工具按 sessionId 自管）：
			// 在 tool_result 之后入历史，避免 assistant(tool_use) 与 user(tool_result)
			// 之间插入 user 消息触发 Anthropic 校验错误
			for _, exec := range p.toolExecutors {
				ac, ok := exec.(AnswerConsumer)
				if !ok {
					continue
				}
				if answer := ac.TakeConsumedAnswer(ctx.sessionId); answer != nil {
					ctx.AddEvent(chat.NewMessageConsumedEvent(ctx.getSeq(), ctx.sessionId, answer))
					answerMsg := answer.ToMessage()
					ctx.events.AppendHistory(&answerMsg)
				}
			}
			// 继续循环：携带 tool_result 再次调用 LLM

		default: // end_turn
			// 先发 done 事件再写 assistant 历史：消息的 Offset 即可覆盖 done，
			// 前端根据历史计算的 start 会落在 done 之后，重连时不会重放残留的 done
			ctx.AddEvent(chat.NewDoneEvent(ctx.sessionId))
			ctx.appendAssistantMessage(blocks)
			ctx.saveAndReset()
			// inbox 还有消息则继续循环，否则退出
			if ctx.inbox.IsEmpty() {
				ctx.running = false
			}
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

	stream := NewBlockStream(ctx.sessionId, ctx)

	callErr := ctx.ChatWithStream(ctx.runCtx, request, stream)

	var blocks chat.Blocks
	var stopReason chat.StopReason
	var streamErr error
	if callErr == nil {
		blocks, stopReason, streamErr = ctx.streamResponse(stream)
	}

	// ===== 重新持锁 =====
	ctx.runLock.Lock()

	if callErr != nil || streamErr != nil {
		ctx.drainInbox()
		ctx.saveAndReset()
		ctx.running = false
		var err error
		if callErr != nil {
			err = callErr
		} else {
			err = streamErr
		}
		evt := chat.NewErrorEvent(err.Error())
		evt.Done = true
		ctx.AddEvent(evt)
		return nil, "", err
	}
	return blocks, stopReason, nil
}

// executeTools 逐个执行本轮 tool_use 命中的工具，返回 tool_result blocks。
// 每个工具在独立协程中执行（runTool），输出内容块写入各自的 Response（由其拼接组合）；
// 本方法消费每个工具的输出并组装为 tool_result，未命中任何工具的 tool_use 补错误结果
// （避免下一轮请求缺 tool_result 报错）。
// 锁协议：进入时持有 runLock；工具执行期间由 runTool 自行释放/重取，返回时保持持锁。
func (p *messageProcessor) executeTools(ctx *SessionContext, blocks chat.Blocks) chat.Blocks {
	var results chat.Blocks
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

		stream := NewBlockStream(ctx.sessionId, ctx)
		util.Go(func() {
			p.runTool(ctx, tu, exec, stream)
			stream.Close()
		})

		results = append(results, p.collectToolResult(ctx, tu, stream))
	}
	return results
}

// runTool 执行单个工具：输出内容块流式写入 writer。
// 锁协议：调用方持有 runLock，工具执行（外部 I/O）期间释放，返回前恢复持锁。
func (p *messageProcessor) runTool(ctx *SessionContext, tu *chat.ToolUseBlock, exec ToolExecutor, writer chat.StreamWriter) {
	args, _ := tu.Input.(map[string]any)
	turn := &Turn{ctx: ctx, args: args}

	ctx.runLock.Unlock()
	execErr := exec.Execute(turn, writer)
	ctx.runLock.Lock()

	if execErr != nil {
		// 错误通过 Response.Err() 传递给消费方组装进 tool_result
		writer.WriteError(execErr)
	}
}

// collectToolResult 消费单个工具的输出直到流结束：文本拼接为结果正文，
// 其余 block 原样保留，组装为 tool_result block；同时发出 tool_execution 事件。
func (p *messageProcessor) collectToolResult(ctx *SessionContext, tu *chat.ToolUseBlock, stream *BlockStream) *chat.ToolResultBlock {
	var text strings.Builder
	var content chat.Blocks
	for b := stream.ReadBlock(); b != nil; b = stream.ReadBlock() {
		if tb, ok := b.(*chat.TextBlock); ok {
			text.WriteString(tb.Text)
			continue
		}
		content = append(content, b)
	}

	resultText := text.String()
	if err := stream.Err(); err != nil {
		if resultText != "" {
			resultText += "\n"
		}
		resultText += fmt.Sprintf("错误: %v", err)
	}
	if resultText == "" {
		resultText = "(无输出)"
	}

	args, _ := tu.Input.(map[string]any)
	ctx.AddEvent(chat.NewToolExecutionEvent(tu.Name, toolArgsDisplay(args), resultText, ctx.sessionId))

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
