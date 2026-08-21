package agent

import (
	"context"

	"github.com/chuccp/go-agent-sdk/chat"
)

// QueuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
//type QueuedMessage struct {
//	ctx  *SessionContext
//	id   uint64
//	msg  *chat.RevMessage
//	opts []chat.Option // 本次消息附带的per-turn选项覆盖
//}

// Msg 返回包装的原始用户消息。
//func (qm *QueuedMessage) Msg() *chat.RevMessage { return qm.msg }
//
//// Context 返回消息所属的会话上下文（通过它访问会话能力）。
//func (qm *QueuedMessage) Context() *SessionContext { return qm.ctx }

// messageProcessor 会话编排器：接收用户消息后入队并驱动会话主循环，
// 主循环执行单轮 LLM 交互（executeRound）与工具执行（executeTools）。
// 会话状态集中于 SessionContext。
type messageProcessor struct {
	ctx           *SessionContext
	toolExecutors []ToolExecutor
	loop          Loop
	handler
}

func newMessageProcessor(sessionContext *SessionContext) *messageProcessor {
	NewLoopBuilder(context.Background(), sessionContext, 0, sessionContext.GetStore())
	p := &messageProcessor{
		ctx:           sessionContext,
		toolExecutors: sessionContext.toolExecutors,
	}
	return p
}

func (p *messageProcessor) WriteBlocks(block ...chat.Block) error {

	return nil
}
func (p *messageProcessor) Stop() {

}

//func (p *messageProcessor) HandleRevMessage(message *chat.RevMessage) error {
//ctx := p.ctx
//ctx.runLock.Lock()
//defer ctx.runLock.Unlock()
//qm := &QueuedMessage{
//	id:   p.ctx.GetSeq(),
//	ctx:  p.ctx,
//	msg:  message,
//	opts: opt,
//}
//ctx.inbox.Write(qm)
//
//if !ctx.running {
//	ctx.running = true
//	ctx.SendBlock(p.no, chat.NewUserTextBlock(qm.id, qm.msg.Text, chat.Sent))
//	util.GoWithRecover(func() {
//		p.doLoop()
//	}, func(r any) {
//		log.Printf("[chatSession] run panic recovered: %v", r)
//		evt := chat.NewErrorBlock("internal error")
//		ctx.SendBlock(p.no, evt)
//	})
//} else {
//	ctx.SendBlock(p.no, chat.NewUserTextBlock(qm.id, qm.msg.Text, chat.Queued))
//}
//return nil
//}

//func (p *messageProcessor) SendBlock(block chat.Block) {
//	p.ctx.SendBlock(p.no, block)
//}

// doLoop 会话主循环：执行单轮 LLM 交互（executeRound：构建请求、流式调用）与
// 工具执行（executeTools），轮次返回后根据 stopReason 完成收尾。
// 停止语义：每轮持有独立的可取消上下文，Stop() 只中止当前轮——
// 被停轮的结果丢弃（不入历史、不报错误），inbox 中的后续消息继续处理，
// 无后续消息则结束循环等待下一条用户消息（与主流 agent 一致）。
// 锁协议：循环全程持有 runLock；executeRound / executeTools 内部在
// LLM 调用与工具执行期间自行释放/重取。
//func (p *messageProcessor) doLoop() {
//	ctx := p.ctx
//	ctx.runLock.Lock()
//	defer ctx.runLock.Unlock()
//	for ctx.running {
//		// 每轮独立的可取消上下文：Stop 只对单轮生效；
//		// 进入新轮前先取消上一轮的上下文（释放 provider 侧监听协程，防泄漏）
//		if ctx.cancel != nil {
//			ctx.cancel()
//		}
//		ctx.runCtx, ctx.cancel = context.WithCancel(context.Background())
//
//		blocks, stopReason, err := p.executeRound()
//
//		if p.roundStopped(ctx) {
//			p.SendBlock(chat.NewDoneBlock())
//			p.finishStoppedRound(ctx)
//			continue
//		}
//		if err != nil {
//			log.Printf("[chatSession] turn ended with error: %v", err)
//			return
//		}
//		if blocks == nil {
//			// inbox 为空，executeRound 已将 running 置 false
//			continue
//		}
//
//		if stopReason == chat.StopReasonToolUse {
//			// assistant 消息入历史
//			ctx.appendAssistantMessage(blocks)
//			// tool_result 作为 user 消息入历史；未命中工具的 tool_use 已在 executeTools 补错误结果
//			results, toolStop := p.executeTools(ctx, blocks)
//			if p.roundStopped(ctx) {
//				// 轮次在工具阶段被停止：已产出的 tool_result 仍须入历史，
//				// 否则历史以无 tool_result 的 tool_use 结尾，下次请求会被 LLM API 拒绝；
//				// done 在历史写入前发出，被其 Offset 覆盖，重连不重放
//				p.SendBlock(chat.NewDoneBlock())
//				ctx.events.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: results})
//				p.finishStoppedRound(ctx)
//				continue
//			}
//			if toolStop == chat.StopReasonUserWait {
//				// 工具请求暂停（如 ask_user_question 等待用户回答）：本轮到此结束。
//				// done 在 tool_result 历史写入前发出，被其 Offset 覆盖，
//				// 前端根据历史计算的 start 落在 done 之后，重连时不重放残留的 done
//				p.SendBlock(chat.NewDoneBlock())
//			}
//			ctx.events.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: results})
//			if toolStop != chat.StopReasonUserWait {
//				// 继续循环：携带 tool_result 再次调用 LLM
//				continue
//			}
//		} else {
//			// 先发 done 事件再写 assistant 历史：消息的 Offset 即可覆盖 done，
//			// 前端根据历史计算的 start 会落在 done 之后，重连时不重放残留的 done
//			p.SendBlock(chat.NewDoneBlock())
//			ctx.appendAssistantMessage(blocks)
//		}
//
//		// 收尾（end_turn / 工具暂停）：保存历史；inbox 还有消息则继续循环，否则退出
//		ctx.saveAndReset()
//		if ctx.inbox.IsEmpty() {
//			ctx.running = false
//		}
//	}
//	// 循环结束：取消最后一轮的上下文（释放 provider 侧监听协程）
//	if ctx.cancel != nil {
//		ctx.cancel()
//	}
//}

//// roundStopped 当前轮是否被 Stop() 取消（runCtx 仅由 Stop 取消）。调用方持有 runLock。
//func (p *messageProcessor) roundStopped(ctx *SessionContext) bool {
//	select {
//	case <-ctx.runCtx.Done():
//		return true
//	default:
//		return false
//	}
//}

// finishStoppedRound 被停轮的统一收尾：保存历史；inbox 有后续消息则继续循环处理，
// 否则结束循环等待下一条用户消息（done 事件由调用方在合适时机发出）。调用方持有 runLock。
//func (p *messageProcessor) finishStoppedRound(ctx *SessionContext) {
//	ctx.saveAndReset()
//	if ctx.inbox.IsEmpty() {
//		ctx.running = false
//	}
//}

// Stop 停止当前轮次（只对单轮生效）：取消本轮的可取消上下文，
// LLM 调用与监听会话停止的工具会尽快中止；后续用户消息不受影响。
//func (p *messageProcessor) Stop() {
//ctx := p.ctx
//ctx.runLock.Lock()
//if ctx.cancel != nil {
//	ctx.cancel()
//}
//ctx.runLock.Unlock()
//}
