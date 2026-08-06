package agent

import (
	"log"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// Filter 所有过滤器的基础接口，Init 注入会话上下文。
type Filter interface {
	Init(context *SessionContext)
}

// MessageFilter 消息过滤器；不调用 chain.Next 即消费该消息。
type MessageFilter interface {
	Filter
	HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error
}

// MessageFilterChain 消息过滤器链，Next 推进到下一个过滤器。
type MessageFilterChain interface {
	Next(msg *QueuedMessage) error
	// Context 返回链所属的会话上下文（链为会话私有，可安全定位当前会话）。
	Context() *SessionContext
}

// messageFilterChain 消息过滤器链实现。
// 过滤器按注册顺序执行，核心主体过滤器位于链尾（最内层）。
type messageFilterChain struct {
	mu             sync.Mutex
	ctx            *SessionContext
	messageFilters []MessageFilter
}

func newMessageFilterChain(ctx *SessionContext) *messageFilterChain {
	return &messageFilterChain{
		ctx:            ctx,
		messageFilters: make([]MessageFilter, 0),
	}
}

// Context 实现 MessageFilterChain 接口，返回链所属会话上下文。
func (c *messageFilterChain) Context() *SessionContext { return c.ctx }

func (c *messageFilterChain) addMessageFilter(filter MessageFilter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageFilters = append(c.messageFilters, filter)
}

// Next 快照当前过滤器列表后从第一个开始执行。
func (c *messageFilterChain) Next(msg *QueuedMessage) error {
	c.mu.Lock()
	filters := make([]MessageFilter, len(c.messageFilters))
	copy(filters, c.messageFilters)
	c.mu.Unlock()
	exec := &messageChainExecution{chain: c, filters: filters, index: -1}
	return exec.Next(msg)
}

// messageChainExecution 携带执行下标的链执行器，作为 MessageFilterChain 传给过滤器。
type messageChainExecution struct {
	chain   *messageFilterChain
	filters []MessageFilter
	index   int
}

func (e *messageChainExecution) Next(msg *QueuedMessage) error {
	e.index++
	if e.index >= len(e.filters) {
		return nil
	}
	return e.filters[e.index].HandleRevMessage(e, msg)
}

// Context 实现 MessageFilterChain 接口，委托给所属链。
func (e *messageChainExecution) Context() *SessionContext { return e.chain.Context() }

// Turn 一轮 LLM 交互的输入输出，在响应链中传递。
type Turn struct {
	Request     *chat.Request   // 输入
	Blocks      chat.Blocks     // 输出：LLM 响应内容块
	StopReason  chat.StopReason // 输出：停止原因
	Err         error           // 输出：错误
	ToolResults chat.Blocks     // 输出：链上工具过滤器累积的执行结果（tool_result blocks）
}

// ResponseFilter 响应过滤器；可在 Next 前后读写 Turn。
// 锁协议：进入 HandleTurn 时调用方持有 runLock，过滤器必须在返回前恢复持锁状态。
type ResponseFilter interface {
	Filter
	HandleTurn(chain ResponseFilterChain, turn *Turn) error
}

// ResponseFilterChain 响应过滤器链，Next 推进到下一个过滤器。
type ResponseFilterChain interface {
	Next(turn *Turn) error
	// Context 返回链所属的会话上下文（链为会话私有，可安全定位当前会话）。
	Context() *SessionContext
}

// responseFilterChain 响应过滤器链实现。
type responseFilterChain struct {
	mu              sync.Mutex
	ctx             *SessionContext
	responseFilters []ResponseFilter
}

func newResponseFilterChain(ctx *SessionContext) *responseFilterChain {
	return &responseFilterChain{
		ctx:             ctx,
		responseFilters: make([]ResponseFilter, 0),
	}
}

// Context 实现 ResponseFilterChain 接口，返回链所属会话上下文。
func (c *responseFilterChain) Context() *SessionContext { return c.ctx }

func (c *responseFilterChain) addResponseFilter(filter ResponseFilter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responseFilters = append(c.responseFilters, filter)
}

// Next 快照当前过滤器列表后从第一个开始执行。
func (c *responseFilterChain) Next(turn *Turn) error {
	c.mu.Lock()
	filters := make([]ResponseFilter, len(c.responseFilters))
	copy(filters, c.responseFilters)
	c.mu.Unlock()
	exec := &responseChainExecution{chain: c, filters: filters, index: -1}
	return exec.Next(turn)
}

// responseChainExecution 携带执行下标的链执行器。
type responseChainExecution struct {
	chain   *responseFilterChain
	filters []ResponseFilter
	index   int
}

func (e *responseChainExecution) Next(turn *Turn) error {
	e.index++
	if e.index >= len(e.filters) {
		return nil
	}
	return e.filters[e.index].HandleTurn(e, turn)
}

// Context 实现 ResponseFilterChain 接口，委托给所属链。
func (e *responseChainExecution) Context() *SessionContext { return e.chain.Context() }

// coreMessageFilter 核心主体过滤器：同时作为消息链与响应链的最内层。
// 消息链终端：用户消息入队并按需启动会话主循环；
// 响应链终端：执行一轮完整的 LLM 交互（构建请求、流式调用、工具执行、轮次收尾）。
type coreMessageFilter struct {
	ctx *SessionContext
}

func newCoreMessageFilter() *coreMessageFilter {
	return &coreMessageFilter{}
}

// Init 实现 Filter 接口，注入会话上下文。
func (core *coreMessageFilter) Init(ctx *SessionContext) {
	core.ctx = ctx
}

// HandleRevMessage 消息链终端：入队并启动主循环。调用时已持有 runLock。
func (core *coreMessageFilter) HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error {
	ctx := core.ctx
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

// HandleTurn 响应链主体：构建请求并执行一轮 LLM 调用，填充 turn 后
// 调用 chain.Next 推进到链上后续过滤器（工具过滤器命中本轮 tool_use 时执行自身）。
// 锁协议：进入时持有 runLock，LLM 网络调用期间释放，返回前恢复持锁。
func (core *coreMessageFilter) HandleTurn(chain ResponseFilterChain, turn *Turn) error {
	ctx := core.ctx

	// 排干 inbox，构建请求（持有 runLock）
	request := ctx.buildRequest()
	if request == nil {
		ctx.running = false
		return nil
	}
	turn.Request = request

	// ===== 释放锁：LLM 网络调用（耗时操作，不持锁） =====
	ctx.runLock.Unlock()
	resp, callErr := ctx.ChatWithStream(ctx.runCtx, request)
	var streamErr error
	if callErr == nil {
		turn.Blocks, turn.StopReason, streamErr = ctx.streamResponse(resp)
	}

	// ===== 重新持锁 =====
	ctx.runLock.Lock()

	if callErr != nil || streamErr != nil {
		ctx.drainInbox()
		ctx.saveAndReset()
		ctx.running = false
		var errMsg string
		if callErr != nil {
			turn.Err = callErr
			errMsg = callErr.Error()
		} else {
			turn.Err = streamErr
			errMsg = streamErr.Error()
		}
		evt := chat.NewErrorEvent(errMsg)
		evt.Done = true
		ctx.AddEvent(evt)
		return turn.Err
	}

	// 推进链：工具过滤器根据 turn.Blocks 中的 tool_use 命中并执行自身，
	// 结果累积到 turn.ToolResults；轮次收尾由编排器（doLoop）完成。
	return chain.Next(turn)
}
