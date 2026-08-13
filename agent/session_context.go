package agent

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// SessionContext 会话的唯一状态中心：消息队列、运行期状态、事件存储、
// 客户端订阅、工具与配置全部集中于此。工具执行时通过 Turn 获得本上下文。
type SessionContext struct {
	inbox         *util.SliceQueue[*QueuedMessage] // 用户输入消息队列（runLock 保护）
	running       bool                             // 主循环是否运行中（runLock 保护）
	runCtx        context.Context                  // 主循环上下文（runLock 保护）
	cancel        context.CancelFunc               // runCtx 的取消函数（runLock 保护）
	runLock       sync.Mutex                       // 保护 inbox / running / runCtx / cancel
	seq           uint64
	sessionId     string
	events        *Store
	registry      *chat.ProviderRegistry
	chatClients   *util.SliceArray[*Client]
	toolExecutors []ToolExecutor
	system        string
	opts          *chat.Options
	historyStore  HistoryStore
	clientMutex   *sync.Mutex // 保护 chatClients
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

func (c *SessionContext) History() []*chat.Message {
	return c.events.History()
}
func (c *SessionContext) Stop() {
	c.cancel()
}

func (c *SessionContext) ReadEvent(position *Position) *chat.ClientEvent {
	return c.events.ReadFrom(position)
}

func (c *SessionContext) DeleteClient(client *Client) {
	c.clientMutex.Lock()
	c.chatClients.Remove(client)
	client.queue.Close()
	c.clientMutex.Unlock()
	c.events.RemovePosition(client.position)
}

// GetChatClient 创建一个事件消费客户端：注册读取位置并加入订阅列表。
func (c *SessionContext) GetChatClient(start uint, handler handler) *Client {
	position := c.events.GetPosition(start)
	chatClient := &Client{
		queue:    util.NewQueue[bool](),
		handler:  handler,
		position: position,
	}
	c.clientMutex.Lock()
	c.chatClients.Append(chatClient)
	c.clientMutex.Unlock()
	return chatClient
}

// ── 会话主体能力（主循环调用）──

// ChatWithStream 使用默认 provider 发起流式对话请求，结果写入调用方创建的独享 StreamWriter。
// stream 创建时传入本上下文作为事件接收方（AddEvent），
// 流式增量产生的客户端事件（chunk/thinking）由 StreamWriter 直接推送。
func (c *SessionContext) ChatWithStream(ctx context.Context, messages *chat.Request, stream *chat.StreamWriter) error {
	provider := c.registry.DefaultProvider()
	return c.registry.ChatWithStream(ctx, provider, messages, stream)
}

// ChatComplete 零上下文一次性调用：不带会话历史、不产生会话事件（receiver 为 nil）。
// 供 flow 执行节点等需要与会话隔离的 LLM 调用使用。
// ChatWithStream 同步写入并 Close，错误直接经其返回值返回；此处仅取回全部 Block。
func (c *SessionContext) ChatComplete(ctx context.Context, request *chat.Request) (string, error) {
	stream := chat.NewStreamWriter(nil)
	if err := c.ChatWithStream(ctx, request, stream); err != nil {
		return "", err
	}
	blocks, _ := stream.ReadBlocks()
	var text string
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			text += tb.Text
		}
	}
	return text, nil
}

// Done 返回主循环上下文的取消通道，供长耗时工具（如 exec_node 的 LLM 调用）响应会话停止。
// 主循环未启动时返回 nil。
func (c *SessionContext) Done() <-chan struct{} {
	c.runLock.Lock()
	defer c.runLock.Unlock()
	if c.runCtx == nil {
		return nil
	}
	return c.runCtx.Done()
}

// ConsumeMessage 将一条用户消息追加到历史记录，并发出消费事件。
// 返回该消息附带的 per-turn 选项。
func (c *SessionContext) ConsumeMessage(qm *QueuedMessage) []chat.Option {
	c.AddEvent(chat.NewMessageConsumedEvent(qm.id, qm.msg))
	msg := qm.msg.ToMessage()
	c.events.AppendHistory(&msg)
	return qm.opts
}

// buildRequest 从 inbox 中取出所有待处理消息，追加到历史记录，构建 LLM 请求。
// 调用方必须持有 runLock。
func (c *SessionContext) buildRequest() *chat.Request {
	var turnOpts []chat.Option
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
		merged := chat.Options{}
		for _, o := range turnOpts {
			o(&merged)
		}
		effective = &merged
	}

	messages := &chat.Request{
		System:   c.composeSystem(),
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
		messages.Thinking = effective.Thinking.ToThinkingConfig()
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

// composeSystem 组装本轮请求的 System：基础系统提示词 + 各工具的引导提示词。
// 工具可通过 UsagePrompt() 返回引导提示词——用了哪个工具就带上哪个，
// 宿主应用无需硬编码；未提供引导词时返回空字符串。
// 调用方必须持有 runLock。
func (c *SessionContext) composeSystem() string {
	system := c.system
	var prompts []string
	for _, exec := range c.toolExecutors {
		if p := exec.UsagePrompt(); p != "" {
			prompts = append(prompts, p)
		}
	}
	if len(prompts) > 0 {
		if system != "" {
			system += "\n\n"
		}
		system += strings.Join(prompts, "\n\n")
	}
	return system
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
