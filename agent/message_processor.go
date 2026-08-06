package agent

import (
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
		responseFilterChain: newResponseFilterChain(),
	}
	sessionContext.processorHandler = p

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

	// 核心主体始终位于两条链的最内层（最后注册）
	core := newCoreMessageFilter()
	core.Init(sessionContext)
	p.messageFilterChain.addMessageFilter(core)
	p.responseFilterChain.addResponseFilter(core)
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

// doLoop 会话主循环：驱动响应过滤器链，直到会话停止（running=false）、
// 被取消（runCtx.Done）或链返回错误（核心过滤器已完成清理与 error 事件）。
// 锁协议：循环全程持有 runLock；响应链内部（核心过滤器）在 LLM 调用
// 与工具执行期间自行释放/重取，返回时恢复持锁状态。
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
	}
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
