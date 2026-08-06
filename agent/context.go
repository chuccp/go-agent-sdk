package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// processorHandler 定义 SessionContext 反向调用 messageProcessor 的能力。
type processorHandler interface {
	handleMessage(message *chat.RevMessage, opt ...Option) error
	doLoop()
	Stop()
}

// SessionContext 会话的唯一状态中心：消息队列、运行期状态、事件存储、
// 客户端订阅、工具与配置全部集中于此。过滤器与工具通过 Filter.Init 获得本上下文。
type SessionContext struct {
	processorHandler
	inbox          *util.SliceQueue[*QueuedMessage] // 用户输入消息队列（runLock 保护）
	running        bool                             // 主循环是否运行中（runLock 保护）
	runCtx         context.Context                  // 主循环上下文（runLock 保护）
	cancel         context.CancelFunc               // runCtx 的取消函数（runLock 保护）
	runLock        sync.Mutex                       // 保护 inbox / running / runCtx / cancel
	seq            uint64
	sessionId      string
	events         *chat.Store
	registry       *chat.ProviderRegistry
	chatClients    *util.SliceArray[*ChatClient]
	toolExecutors  map[string]ToolExecutor
	system         string
	opts           *Options
	historyStore   chat.HistoryStore
	clientMutex    *sync.Mutex      // 保护 chatClients
	consumedAnswer *chat.RevMessage // 本轮工具消费的用户回答（executeTools 结束后入历史）
	answerMu       sync.Mutex       // 保护 answerCh
	answerCh       chan *chat.RevMessage // 正在等待的用户回答投递通道（如 ask_user_question）
}

// newRunContext 创建主循环上下文。调用方必须持有 runLock。
func newRunContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// ID 返回会话 ID。
func (c *SessionContext) ID() string { return c.sessionId }

func (c *SessionContext) getSeq() uint64 {
	return atomic.AddUint64(&c.seq, 1)
}

// AddEvent 追加事件到存储并通知所有客户端。
func (c *SessionContext) AddEvent(event *chat.ClientEvent) {
	if event.EventType == chat.EventTypeDone {
		log.Printf("[processor] addEvent DONE, sessionId=%s", c.sessionId)
	}
	c.events.Add(event)
	c.Flush()
}

// Flush 通知所有客户端有新事件可读。
func (c *SessionContext) Flush() {
	c.clientMutex.Lock()
	clients := c.chatClients.Slice()
	c.clientMutex.Unlock()
	for _, sub := range clients {
		err := sub.queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat session: %v", err)
		}
	}
}

// ── chatHandler 接口实现（ChatClient 的 handler 指向 SessionContext）──

func (c *SessionContext) SendMessage(message *chat.RevMessage, opt ...Option) error {
	return c.processorHandler.handleMessage(message, opt...)
}

func (c *SessionContext) History() []*chat.Message {
	return c.events.History()
}

func (c *SessionContext) ReadEvent(position *chat.Position) *chat.ClientEvent {
	return c.events.ReadFrom(position)
}

func (c *SessionContext) DeleteClient(client *ChatClient) {
	c.clientMutex.Lock()
	c.chatClients.Remove(client)
	client.queue.Close()
	c.clientMutex.Unlock()
	c.events.RemovePosition(client.position)
}

func (c *SessionContext) Stop() {
	c.processorHandler.Stop()
}

// GetChatClient 创建一个事件消费客户端：注册读取位置并加入订阅列表。
func (c *SessionContext) GetChatClient(start uint) *ChatClient {
	position := c.events.GetPosition(start)
	chatClient := &ChatClient{
		queue:    util.NewQueue[bool](),
		handler:  c,
		position: position,
	}
	c.clientMutex.Lock()
	c.chatClients.Append(chatClient)
	c.clientMutex.Unlock()
	return chatClient
}

// ── 会话主体能力（核心过滤器调用）──

// ChatWithStream 使用默认 provider 发起流式对话请求。
func (c *SessionContext) ChatWithStream(ctx context.Context, messages *chat.Request) (*chat.Response, error) {
	provider := c.registry.DefaultProvider()
	return c.registry.ChatWithStream(ctx, provider, messages)
}

// ConsumeMessage 将一条用户消息追加到历史记录，并发出消费事件。
// 返回该消息附带的 per-turn 选项。
func (c *SessionContext) ConsumeMessage(qm *QueuedMessage) []Option {
	c.AddEvent(chat.NewMessageConsumedEvent(qm.id, c.sessionId, qm.msg))
	msg := qm.msg.ToMessage()
	c.events.AppendHistory(&msg)
	return qm.opts
}

// buildRequest 从 inbox 中取出所有待处理消息，追加到历史记录，构建 LLM 请求。
// 调用方必须持有 runLock。
func (c *SessionContext) buildRequest() *chat.Request {
	var turnOpts []Option
	for {
		qm, err := c.inbox.Read()
		if err != nil {
			break
		}
		if opts := c.ConsumeMessage(qm); len(opts) > 0 {
			turnOpts = opts
		}
	}
	c.inbox.Reset()

	// 注入历史上下文
	history := c.events.History()
	if len(history) == 0 {
		return nil
	}

	effective := c.opts
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
		System:   c.system,
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

	if len(c.toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(c.toolExecutors))
		for _, exec := range c.toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}

// drainInbox 排干 inbox 中所有剩余消息，将它们写入历史（不丢失）。
// 调用方必须持有 runLock。
func (c *SessionContext) drainInbox() {
	for {
		qm, err := c.inbox.Read()
		if err != nil {
			break
		}
		c.ConsumeMessage(qm)
	}
	c.inbox.Reset()
}

// appendAssistantMessage 将 LLM 返回的 content blocks 作为 assistant 消息写入历史。
func (c *SessionContext) appendAssistantMessage(blocks chat.Blocks) {
	assistantMsg := &chat.Message{Role: chat.RoleAssistant, Content: blocks}
	c.events.AppendHistory(assistantMsg)
}

// saveAndReset 持久化自上次保存以来新增的消息，并清理 client 已读取的事件条目。
func (c *SessionContext) saveAndReset() {
	if err := c.events.SaveHistory(); err != nil {
		log.Printf("[chatSession] save history failed: %v", err)
	}
	c.events.Reset()
}

// executeTools 执行 tool_use blocks 中的工具，将 tool_result 作为 user 消息追加到历史。
// 若本轮有工具（如 ask_user_question）消费了用户回答，回答在 tool_result 之后
// 作为 user 消息入历史并补发 message_consumed 事件。
func (c *SessionContext) executeTools(blocks chat.Blocks) {
	c.consumedAnswer = nil
	toolResults := make(chat.Blocks, 0, len(blocks))
	for _, block := range blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		exec, exists := c.toolExecutors[tu.Name]
		if !exists {
			toolResults = append(toolResults, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewTextBlock(fmt.Sprintf("未知工具: %s", tu.Name))},
			))
			continue
		}

		args, _ := tu.Input.(map[string]any)
		// 执行前即时注入会话上下文：工具实例跨会话共享，执行时刻注入可保证
		// ctx 始终指向当前会话（同一会话内工具串行执行，无竞争）
		exec.Init(c)
		output, execErr := exec.Execute(args)

		c.AddEvent(chat.NewToolExecutionEvent(tu.Name, output, c.sessionId))

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
	c.events.AppendHistory(msg)

	// 工具消费的用户回答：在 tool_result 之后入历史（避免 assistant(tool_use)
	// 与 user(tool_result) 之间插入 user 消息触发 Anthropic 校验错误）
	if c.consumedAnswer != nil {
		answer := c.consumedAnswer
		c.consumedAnswer = nil
		c.AddEvent(chat.NewMessageConsumedEvent(c.getSeq(), c.sessionId, answer))
		answerMsg := answer.ToMessage()
		c.events.AppendHistory(&answerMsg)
	}
}

// WaitForUserMessage 阻塞等待用户的下一条消息。
// 拦截依赖实现了 MessageFilter 的工具（如 ask_user_question）在消息链中
// 调用 DeliverAnswer 投递；拦截的消息不进入 inbox（不触发 LLM 调用）。
// 若主循环上下文被取消（如用户点 Stop），返回 error。
func (c *SessionContext) WaitForUserMessage() (*chat.RevMessage, error) {
	ch := make(chan *chat.RevMessage, 1)
	c.answerMu.Lock()
	c.answerCh = ch
	c.answerMu.Unlock()
	defer func() {
		c.answerMu.Lock()
		c.answerCh = nil
		c.answerMu.Unlock()
	}()
	var done <-chan struct{}
	if c.runCtx != nil {
		done = c.runCtx.Done()
	}
	select {
	case msg := <-ch:
		c.consumedAnswer = msg
		return msg, nil
	case <-done:
		return nil, c.runCtx.Err()
	}
}

// DeliverAnswer 若当前正在等待用户回答（如 ask_user_question 工具阻塞中），
// 投递并消费该消息，返回 true；否则返回 false。
// 由实现了 MessageFilter 的工具在消息链中调用。
func (c *SessionContext) DeliverAnswer(msg *chat.RevMessage) bool {
	c.answerMu.Lock()
	ch := c.answerCh
	c.answerMu.Unlock()
	if ch == nil {
		return false
	}
	ch <- msg
	return true
}

// streamResponse 消费 SSE 流，返回所有 content block 和 stop_reason。
// 同时在消费过程中通过 AddEvent 向外广播文本增量。
func (c *SessionContext) streamResponse(resp *chat.Response) (blocks chat.Blocks, stopReason chat.StopReason, err error) {
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
				c.AddEvent(chat.NewChunkEvent(e.Delta.Text, c.sessionId))
			case chat.DeltaTypeThinking:
				collector.appendThinking(e.Delta.Thinking)
				c.AddEvent(chat.NewThinkingEvent(e.Delta.Thinking, c.sessionId))
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

// withoutThinking 从 blocks 中剥离 thinking block。
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
