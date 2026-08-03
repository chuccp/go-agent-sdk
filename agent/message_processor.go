package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// sessionContext 定义 messageProcessor 所需的会话能力接口，
// 使 messageProcessor 不直接依赖具体的 chatSession 结构体。
type sessionContext interface {
	ID() string
	// Flush 通知所有订阅客户端有新事件可读。
	Flush()
	// ChatWithStream 使用默认 provider 发起流式对话请求。
	ChatWithStream(ctx context.Context, messages *chat.Request) (*chat.Response, error)
	// Options 返回当前会话选项。
	Options() *Options
	// System 返回系统提示词。
	System() string
	// ToolExecutors 返回已注册的工具执行器集合。
	ToolExecutors() map[string]ToolExecutor
}

// queuedMessage 是 agent 层的消息包装，携带追踪 ID（不侵入 chat 协议层）。
type queuedMessage struct {
	id   uint64
	msg  *chat.RevMessage
	opts []Option // 本次消息附带的per-turn选项覆盖
}

// messageProcessor 负责会话的消息处理：接收用户消息、运行会话主循环、
// 构建 LLM 请求、解析流式响应、执行工具以及持久化历史。
// 由 chatSession 持有并调用，运行期状态（inbox / running / cancel）由 runMutex 保护。
type messageProcessor struct {
	ctx       sessionContext
	events    *chat.Store
	runMutex  sync.Mutex                      // 保护 inbox / running / cancel
	inbox     util.SliceQueue[*queuedMessage] // 用户输入消息队列（runMutex 保护）
	running   bool
	cancel    context.CancelFunc
	runCtx    context.Context // run 循环使用的上下文
	seq       uint64
	sessionId string
}

func newMessageProcessor(ctx sessionContext, historyStore chat.HistoryStore) *messageProcessor {
	return &messageProcessor{
		ctx:       ctx,
		sessionId: ctx.ID(),
		events:    chat.NewStore(ctx.ID(), historyStore),
	}
}

// handleMessage 接收一条用户消息：入队后若主循环未运行则启动，否则仅排队等待。
func (p *messageProcessor) handleMessage(message *chat.RevMessage, opt ...Option) {
	qm := &queuedMessage{
		id:   p.getSeq(),
		msg:  message,
		opts: opt,
	}
	p.runMutex.Lock()
	defer p.runMutex.Unlock()
	err := p.inbox.Write(qm)
	if err != nil {
		log.Printf("[chatSession] inbox write failed: %v", err)
		return
	}
	if !p.running {
		p.runCtx, p.cancel = context.WithCancel(context.Background())
		p.running = true
		p.addEvent(chat.NewMessageSentEvent(qm.id, p.sessionId, message))
		util.GoWithRecover(func() {
			p.run()
		}, func(r any) {
			log.Printf("[chatSession] run panic recovered: %v", r)
			evt := chat.NewErrorEvent("internal error")
			evt.Done = true
			p.addEvent(evt)
		})
	} else {
		p.addEvent(chat.NewMessageQueuedEvent(qm.id, p.sessionId, message))
	}
}

// Stop 取消当前正在运行的会话主循环。
func (p *messageProcessor) Stop() {
	p.runMutex.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.runMutex.Unlock()
}

func (p *messageProcessor) getSeq() uint64 {
	return atomic.AddUint64(&p.seq, 1)
}

// History 返回当前会话的完整历史。
func (p *messageProcessor) History() []*chat.Message {
	return p.events.History()
}

// LoadHistory 从持久化存储加载历史记录。
func (p *messageProcessor) LoadHistory() error {
	return p.events.LoadHistory()
}

// ReadEvent 从指定事件位置开始读取事件条目。
func (p *messageProcessor) ReadEvent(position *chat.Position) *chat.EventEntry {
	return p.events.ReadFrom(position)
}

// RemoveEventPosition 注销客户端的读取位置。
func (p *messageProcessor) RemoveEventPosition(position *chat.Position) {
	p.events.RemovePosition(position)
}

// run 会话主循环。持有 runMutex 运行，仅在 LLM 网络调用期间释放（允许 handleMessage 写入 inbox）。
func (p *messageProcessor) run() {
	p.runMutex.Lock()
	for {
		// 检查取消
		select {
		case <-p.runCtx.Done():
			p.drainInbox()
			p.saveAndReset()
			p.running = false
			p.cancel = nil
			p.runMutex.Unlock()
			return
		default:
		}

		// 排干 inbox，构建请求
		messages := p.buildRequest()
		if messages == nil {
			p.running = false
			p.cancel = nil
			p.runMutex.Unlock()
			return
		}

		// ===== 释放锁：LLM 网络调用（耗时操作，不持锁） =====
		p.runMutex.Unlock()

		resp, callErr := p.ctx.ChatWithStream(p.runCtx, messages)

		var blocks chat.Blocks
		var stopReason chat.StopReason
		var streamErr error
		if callErr == nil {
			blocks, stopReason, streamErr = p.streamResponse(resp)
		}

		// ===== 重新持锁 =====
		p.runMutex.Lock()

		if callErr != nil || streamErr != nil {
			p.drainInbox()
			p.saveAndReset()
			p.running = false
			p.cancel = nil
			p.runMutex.Unlock()
			// 统一发送错误事件
			var errMsg string
			if callErr != nil {
				errMsg = callErr.Error()
			} else {
				errMsg = streamErr.Error()
			}
			evt := chat.NewErrorEvent(errMsg)
			evt.Done = true
			p.addEvent(evt)
			return
		}

		// assistant 消息入历史
		p.appendAssistantMessage(blocks)

		switch stopReason {
		case chat.StopReasonToolUse:
			// 工具执行属于外部 I/O，释放锁（与 LLM 调用同理）
			p.runMutex.Unlock()
			p.executeTools(blocks)
			p.runMutex.Lock()

		default: // end_turn
			log.Printf("[processor] end_turn, adding done event, inbox empty=%v", p.inbox.IsEmpty())
			p.addEvent(chat.NewDoneEvent(p.sessionId))
			p.saveAndReset()
			// inbox 还有消息则继续循环，否则退出
			if p.inbox.IsEmpty() {
				p.running = false
				p.cancel = nil
				p.runMutex.Unlock()
				return
			}
		}
	}
}

// buildRequest 从 inbox 中取出所有待处理消息，追加到历史记录，构建 LLM 请求。
// 调用方必须持有 runMutex。
func (p *messageProcessor) buildRequest() *chat.Request {
	var turnOpts []Option
	for {
		qm, err := p.inbox.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("[chatSession] inbox read failed: %v", err)
			continue
		}
		p.consumeMessage(qm)
		if len(qm.opts) > 0 {
			turnOpts = qm.opts
		}
	}
	p.inbox.Reset()

	// 注入历史上下文
	history := p.events.History()
	if len(history) == 0 {
		return nil
	}

	effective := p.ctx.Options()
	if len(turnOpts) > 0 && effective != nil {
		merged := *effective
		for _, o := range turnOpts {
			o(&merged)
		}
		effective = &merged
	} else if len(turnOpts) > 0 {
		// effective 为 nil 但存在 per-turn 选项时，从默认零值合并
		merged := Options{}
		for _, o := range turnOpts {
			o(&merged)
		}
		effective = &merged
	}

	messages := &chat.Request{
		System:   p.ctx.System(),
		Messages: make([]chat.Message, 0, len(history)),
	}
	for _, m := range history {
		msg := *m
		msg.Content = withoutThinking(m.Content)
		// 剥离后内容为空的消息不发送（避免空 content 报错）
		if len(msg.Content) == 0 {
			continue
		}
		messages.Messages = append(messages.Messages, msg)
	}

	if effective != nil {
		messages.Model = effective.Model
		messages.MaxTokens = effective.MaxTokens
		messages.Temperature = effective.Temperature
		messages.TopP = effective.TopP
		messages.TopK = effective.TopK
		messages.StopSequences = effective.StopSequences
		messages.Stream = effective.Stream
		messages.Thinking = effective.Thinking.toThinkingConfig()
	} else {
		messages.Stream = true
	}

	toolExecutors := p.ctx.ToolExecutors()
	if len(toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(toolExecutors))
		for _, exec := range toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}

// consumeMessage 将一条用户消息追加到历史记录，并发出消费事件。
func (p *messageProcessor) consumeMessage(qm *queuedMessage) {
	p.addEvent(chat.NewMessageConsumedEvent(qm.id, p.sessionId, qm.msg))
	msg := qm.msg.ToMessage()
	p.events.AppendHistory(&msg)
}

func (p *messageProcessor) addEvent(event *chat.ClientEvent) {
	if event.EventType == chat.EventTypeDone {
		log.Printf("[processor] addEvent DONE, sessionId=%s", p.sessionId)
	}
	p.events.Add(event)
	p.ctx.Flush()
}

// drainInbox 排干 inbox 中所有剩余消息，将它们写入历史（不丢失）。
// 调用方必须持有 runMutex。
func (p *messageProcessor) drainInbox() {
	for {
		qm, err := p.inbox.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("[chatSession] inbox read failed: %v", err)
			continue
		}
		p.consumeMessage(qm)
	}
	p.inbox.Reset()
}

// streamResponse 消费 SSE 流，返回所有 content block 和 stop_reason。
// 同时在消费过程中通过 addEvent 向外广播文本增量。
func (p *messageProcessor) streamResponse(resp *chat.Response) (blocks chat.Blocks, stopReason chat.StopReason, err error) {
	var collector blockCollector

	for evt := resp.ReadEvent(); evt != nil; evt = resp.ReadEvent() {
		switch evt.Type() {
		case chat.EventTypeContentBlockStart:
			e := evt.(*chat.ContentBlockStartEvent)
			var id, name string
			if tu, ok := e.ContentBlock.(*chat.ToolUseBlock); ok {
				id = tu.ID
				name = tu.Name
			}
			collector.start(e.ContentBlock.Type(), id, name)

		case chat.EventTypeContentBlockDelta:
			e := evt.(*chat.ContentBlockDeltaEvent)
			switch e.Delta.Type {
			case chat.DeltaTypeText:
				collector.appendText(e.Delta.Text)
				p.addEvent(chat.NewChunkEvent(e.Delta.Text, p.sessionId))
			case chat.DeltaTypeThinking:
				collector.appendThinking(e.Delta.Thinking)
				p.addEvent(chat.NewThinkingEvent(e.Delta.Thinking, p.sessionId))
			case chat.DeltaTypeInputJSON:
				collector.appendJSON(e.Delta.PartialJSON)
			}

		case chat.EventTypeContentBlockStop:
			collector.flush()

		case chat.EventTypeMessageDelta:
			e := evt.(*chat.MessageDeltaEvent)
			stopReason = e.StopReason

		case chat.EventTypeError:
			e := evt.(*chat.ErrorEvent)
			return collector.take(), stopReason, e.Err

		case chat.EventTypeMessageStop:
			return collector.take(), stopReason, nil
		}
	}

	// 流异常中断（ReadEvent 返回 nil 但未收到 MessageStop）
	return collector.take(), stopReason, nil
}

// executeTools 执行 tool_use blocks 中的工具，将 tool_result 作为 user 消息追加到历史。
func (p *messageProcessor) executeTools(blocks chat.Blocks) {
	toolExecutors := p.ctx.ToolExecutors()
	toolResults := make(chat.Blocks, 0, len(blocks))
	for _, block := range blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		exec, exists := toolExecutors[tu.Name]
		if !exists {
			toolResults = append(toolResults, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewTextBlock(fmt.Sprintf("未知工具: %s", tu.Name))},
			))
			continue
		}

		args, _ := tu.Input.(map[string]any)
		output, execErr := exec.Execute(args)

		p.addEvent(chat.NewToolExecutionEvent(tu.Name, output, p.sessionId))

		resultText := output
		if execErr != nil {
			resultText = fmt.Sprintf("错误: %v", execErr)
		}
		toolResults = append(toolResults, chat.NewToolResultBlock(
			tu.ID,
			chat.Blocks{chat.NewTextBlock(resultText)},
		))
	}
	// tool_result 作为 user 消息入历史
	msg := &chat.Message{Role: chat.RoleUser, Content: toolResults}
	p.events.AppendHistory(msg)
}

// appendAssistantMessage 将 LLM 返回的 content blocks 作为 assistant 消息写入历史。
func (p *messageProcessor) appendAssistantMessage(blocks chat.Blocks) {
	assistantMsg := &chat.Message{Role: chat.RoleAssistant, Content: blocks}
	p.events.AppendHistory(assistantMsg)
}

// saveAndReset 持久化自上次保存以来新增的消息，并清理 client 已读取的事件条目。
func (p *messageProcessor) saveAndReset() {
	if err := p.events.SaveHistory(); err != nil {
		log.Printf("[chatSession] save history failed: %v", err)
	}
	p.events.Reset()
}

func (p *messageProcessor) GetPosition(start uint) *chat.Position {
	return p.events.GetPosition(start)
}

// withoutThinking 从 blocks 中剩离 thinking block。
// 发送给 LLM 的历史不需要思考链：Anthropic 要求历史中的 thinking block 必须携带
// signature 字段原样传回，否则报 400；且空 thinking 会因 omitempty 序列化为
// {"type":"thinking"} 缺少 thinking 字段。思考链仍保留在 history/DB 中供展示，
// 仅不回传给模型。
func withoutThinking(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
		if _, ok := b.(*chat.ThinkingBlock); ok {
			continue
		}
		result = append(result, b)
	}
	return result
}

// blockBuilder 在流式接收过程中累积构建一个 content block。
type blockBuilder struct {
	blockType chat.ContentType
	text      strings.Builder
	thinking  strings.Builder
	rawJSON   strings.Builder
	id        string
	name      string
}

// finalize 完成当前 block 的构建，返回具体的 Block 实现。
func (b *blockBuilder) finalize() chat.Block {
	switch b.blockType {
	case chat.ContentTypeText:
		return chat.NewTextBlock(b.text.String())
	case chat.ContentTypeThinking:
		return chat.NewThinkingBlock(b.thinking.String())
	case chat.ContentTypeToolUse:
		var input any
		if b.rawJSON.Len() > 0 {
			if err := json.Unmarshal([]byte(b.rawJSON.String()), &input); err != nil {
				log.Printf("tool_use JSON 解析失败: %v, raw=%s", err, b.rawJSON.String())
			}
		}
		return chat.NewToolUseBlock(b.id, b.name, input)
	default:
		return chat.NewTextBlock(b.text.String())
	}
}

// blockCollector 管理流式 content block 的累积。
type blockCollector struct {
	current *blockBuilder
	blocks  chat.Blocks
}

// start 开始构建一个新的 content block（自动 flush 上一个）。
func (c *blockCollector) start(blockType chat.ContentType, id, name string) {
	c.flush()
	c.current = &blockBuilder{blockType: blockType, id: id, name: name}
}

// flush 完成当前 block 并将其加入列表。
func (c *blockCollector) flush() {
	if c.current != nil {
		b := c.current.finalize()
		c.current = nil
		// 跳过空的 thinking block（避免产生 {"type":"thinking"} 脏数据）
		if tb, ok := b.(*chat.ThinkingBlock); ok && tb.Thinking == "" {
			return
		}
		c.blocks = append(c.blocks, b)
	}
}

// appendText 向当前 block 追加文本增量。
func (c *blockCollector) appendText(text string) {
	if c.current != nil {
		c.current.text.WriteString(text)
	}
}

// appendThinking 向当前 block 追加思考链增量。
func (c *blockCollector) appendThinking(thinking string) {
	if c.current != nil {
		c.current.thinking.WriteString(thinking)
	}
}

// appendJSON 向当前 block 追加 input_json_delta 片段。
func (c *blockCollector) appendJSON(fragment string) {
	if c.current != nil {
		c.current.rawJSON.WriteString(fragment)
	}
}

// take 返回所有已累积的 block（先 flush 当前未完成的）。
func (c *blockCollector) take() chat.Blocks {
	c.flush()
	return c.blocks
}
