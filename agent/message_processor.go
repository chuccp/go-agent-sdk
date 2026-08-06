package agent

import (
	"fmt"
	"log"

	"github.com/chuccp/go-agent-sdk/chat"
)

// QueuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
type QueuedMessage struct {
	id   uint64
	msg  *chat.RevMessage
	opts []Option // 本次消息附带的per-turn选项覆盖
}

// Msg 返回包装的原始用户消息。
func (qm *QueuedMessage) Msg() *chat.RevMessage { return qm.msg }

// messageProcessor 会话编排器：接收用户消息后交给消息过滤器链，
// 会话主循环驱动响应过滤器链。主体逻辑位于 coreMessageFilter（两条链的最内层），
// 会话状态集中于 SessionContext。
type messageProcessor struct {
	ctx                 *SessionContext
	messageFilterChain  *messageFilterChain
	responseFilterChain *responseFilterChain
}

func newMessageProcessor(sessionContext *SessionContext) *messageProcessor {
	p := &messageProcessor{
		ctx:                 sessionContext,
		messageFilterChain:  newMessageFilterChain(sessionContext),
		responseFilterChain: newResponseFilterChain(sessionContext),
	}
	sessionContext.processorHandler = p

	// 响应链：核心主体在链首（负责 LLM 调用），工具过滤器随后，
	// core 调用 chain.Next 后逐个命中本轮 tool_use 并执行自身。
	core := newCoreMessageFilter()
	core.Init(sessionContext)
	p.responseFilterChain.addResponseFilter(core)

	// 注册工具过滤器：所有工具都是响应过滤器（ToolExecutor 继承 ResponseFilter）；
	// 实现了 MessageFilter 的工具（如 ask_user_question）同时注册到消息链，
	// 用于拦截用户回答。
	for _, exec := range sessionContext.toolExecutors {
		exec.Init(sessionContext)
		p.responseFilterChain.addResponseFilter(exec)
		if mf, ok := exec.(MessageFilter); ok {
			p.messageFilterChain.addMessageFilter(mf)
		}
	}

	// 消息链：核心主体位于最内层（入队并启动主循环）
	p.messageFilterChain.addMessageFilter(core)
	return p
}

// handleMessage 接收一条用户消息：包装为 QueuedMessage 后交给消息过滤器链。
// 链的最内层（coreMessageFilter）负责入队并按需启动主循环；
// 动态过滤器（如 ask_user 的一次性过滤器）可在链首拦截消费。
func (p *messageProcessor) handleMessage(message *chat.RevMessage, opt ...Option) error {
	qm := &QueuedMessage{
		id:   p.ctx.getSeq(),
		msg:  message,
		opts: opt,
	}
	return p.messageFilterChain.Next(qm)
}

// doLoop 会话主循环：驱动响应过滤器链（core LLM 调用 → 工具过滤器执行），
// 链返回后根据 turn 完成轮次收尾，直到会话停止（running=false）、
// 被取消（runCtx.Done）或链返回错误（核心过滤器已完成清理与 error 事件）。
// 锁协议：循环全程持有 runLock；链内部在 LLM 调用与工具执行期间自行释放/重取。
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
		turn := &Turn{}
		err := p.responseFilterChain.Next(turn)
		if err != nil {
			log.Printf("[chatSession] turn ended with error: %v", err)
			return
		}
		if turn.Request == nil {
			// inbox 为空，核心过滤器已将 running 置 false
			continue
		}
		// ── 轮次收尾（持 runLock）──
		switch turn.StopReason {
		case chat.StopReasonToolUse:
			// assistant 消息入历史
			ctx.appendAssistantMessage(turn.Blocks)
			// tool_result 作为 user 消息入历史；为无工具命中的 tool_use 补错误结果
			results := finalizeToolResults(turn)
			ctx.events.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: results})
			// 工具消费的用户回答（如 ask_user）：在 tool_result 之后入历史，
			// 避免 assistant(tool_use) 与 user(tool_result) 之间插入 user 消息
			// 触发 Anthropic 校验错误
			if ctx.consumedAnswer != nil {
				answer := ctx.consumedAnswer
				ctx.consumedAnswer = nil
				ctx.AddEvent(chat.NewMessageConsumedEvent(ctx.getSeq(), ctx.sessionId, answer))
				answerMsg := answer.ToMessage()
				ctx.events.AppendHistory(&answerMsg)
			}
			// 继续循环：携带 tool_result 再次调用 LLM

		default: // end_turn
			// 先发 done 事件再写 assistant 历史：消息的 Offset 即可覆盖 done，
			// 前端根据历史计算的 start 会落在 done 之后，重连时不会重放残留的 done
			ctx.AddEvent(chat.NewDoneEvent(ctx.sessionId))
			ctx.appendAssistantMessage(turn.Blocks)
			ctx.saveAndReset()
			// inbox 还有消息则继续循环，否则退出
			if ctx.inbox.IsEmpty() {
				ctx.running = false
			}
		}
	}
}

// finalizeToolResults 汇总链上工具过滤器累积的执行结果，
// 为未命中任何工具的 tool_use 补充错误结果（避免下一轮请求缺 tool_result 报错）。
func finalizeToolResults(turn *Turn) chat.Blocks {
	results := make(chat.Blocks, 0, len(turn.ToolResults))
	results = append(results, turn.ToolResults...)
	for _, block := range turn.Blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		matched := false
		for _, r := range turn.ToolResults {
			if tr, ok := r.(*chat.ToolResultBlock); ok && tr.ToolUseID == tu.ID {
				matched = true
				break
			}
		}
		if !matched {
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewTextBlock(fmt.Sprintf("未知工具: %s", tu.Name))},
			))
		}
	}
	return results
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
