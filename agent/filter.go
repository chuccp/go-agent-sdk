package agent

import (
	"log"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type Filter interface {
	Init(context *SessionContext)
}

// MessageFilter 消息过滤器；不调用 chain.Next 即消费该消息。
type MessageFilter interface {
	Filter
	HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error
}
type MessageFilterChain interface {
	Next(msg *QueuedMessage) error
}

type messageFilterChain struct {
	MessageFilterChain
	messageFilters []MessageFilter
}

func newMessageFilterChain() *messageFilterChain {
	return &messageFilterChain{
		messageFilters: make([]MessageFilter, 0),
	}
}
func (ctx *messageFilterChain) addMessageFilter(messageFilter MessageFilter) {
	ctx.messageFilters = append(ctx.messageFilters, messageFilter)
}
func (ctx *messageFilterChain) Next(msg *QueuedMessage) error {

	return nil
}

type Turn struct {
	Request    *chat.Request   // 输入
	Blocks     chat.Blocks     // 输出
	StopReason chat.StopReason // 输出
	Err        error           // 输出
}

// ResponseFilter 响应过滤器；可在 Next 前后读写 Turn。
type ResponseFilter interface {
	HandleTurn(chain ResponseFilterChain, turn *Turn) error
}
type ResponseFilterChain interface {
	Next(turn *Turn) error
}

type responseFilterChain struct {
	ResponseFilterChain
	responseFilters []ResponseFilter
}

func newResponseFilterChain() *responseFilterChain {
	return &responseFilterChain{
		responseFilters: make([]ResponseFilter, 0),
	}
}
func (ctx *responseFilterChain) addResponseFilter(responseFilter ResponseFilter) {
	ctx.responseFilters = append(ctx.responseFilters, responseFilter)
}

type coreMessageFilter struct {
	MessageFilter
	ResponseFilter
	messageContext *SessionContext
}

func (core *coreMessageFilter) HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error {
	err := core.messageContext.inbox.Write(msg)
	if err != nil {
		log.Printf("[chatSession] inbox write failed: %v", err)
		return err
	}
	if !core.messageContext.running {
		//p.runCtx, p.cancel = context.WithCancel(context.Background())
		core.messageContext.running = true
		core.messageContext.AddEvent(chat.NewMessageSentEvent(msg.id, core.messageContext.sessionId, msg.msg))
		util.GoWithRecover(func() {
			core.messageContext.doLoop()
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := chat.NewErrorEvent("internal error")
			evt.Done = true
			core.messageContext.AddEvent(evt)
		})
	} else {
		core.messageContext.AddEvent(chat.NewMessageQueuedEvent(msg.id, core.messageContext.sessionId, msg.msg))
	}
	return nil
}
func (core *coreMessageFilter) HandleTurn(chain ResponseFilterChain, turn *Turn) error {
	return nil
}
func newCoreMessageFilter() *coreMessageFilter {
	return &coreMessageFilter{}
}
