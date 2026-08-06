package agent

import (
	"log"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// QueuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
type QueuedMessage struct {
	ctx  *SessionContext
	id   uint64
	msg  *chat.RevMessage
	opts []Option // 本次消息附带的per-turn选项覆盖
}

// Msg 返回包装的原始用户消息。
func (qm *QueuedMessage) Msg() *chat.RevMessage { return qm.msg }

// Context 返回消息所属的会话上下文（消息链上的过滤器通过它访问会话能力）。
func (qm *QueuedMessage) Context() *SessionContext { return qm.ctx }

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
