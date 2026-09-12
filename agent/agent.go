package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/log"
	"github.com/chuccp/go-agent-sdk/util"
)

type Context interface {
	context.Context
	SessionId() string
	GetChat() *chat.Chat
	SubAgentStore() *Store
	AgentStore() *Store

	SendBlock(no uint64, block chat.Block) uint64
	SendSignalBlock(no uint64, block chat.Block) uint64
	GetTransferStart() uint64

	AppendMainAssistantMessage(blocks *chat.BlockGroup)
	AppendMainUserMessage(blocks *chat.BlockGroup)
}

type Agent struct {
	inbox         *util.SliceQueue[*chat.UserBlock]
	agentContext  Context
	service       chat.Service
	running       bool
	pContext      context.Context
	pCancel       context.CancelFunc
	runLock       sync.Mutex
	store         *Store
	lContext      context.Context
	lCancel       context.CancelFunc
	ctxLock       sync.Mutex
	mid           atomic.Uint64
	toolExecutors []ToolExecutor
	config        *chat.Config
	systemPrompt  string
	done          func()
	lifecycle     *FuncLifecycle
}

type Builder struct {
	agent *Agent
}

func NewBuilder(agentContext Context) *Builder {
	pContext, plCancel := context.WithCancel(agentContext)
	return &Builder{agent: &Agent{
		agentContext:  agentContext,
		toolExecutors: make([]ToolExecutor, 0),
		inbox:         new(util.SliceQueue[*chat.UserBlock]),
		pContext:      pContext,
		pCancel:       plCancel,
		config:        chat.DefaultConfig(),
	}}
}
func (b *Builder) Store(store *Store) *Builder {
	b.agent.store = store
	return b
}
func (b *Builder) Config(config *chat.Config) *Builder {
	b.agent.config = config
	return b
}
func (b *Builder) ToolExecutor(toolExecutor ...ToolExecutor) *Builder {
	b.agent.toolExecutors = append(b.agent.toolExecutors, toolExecutor...)
	return b
}
func (b *Builder) Lifecycle(lifecycle *FuncLifecycle) *Builder {
	b.agent.lifecycle = lifecycle
	return b
}

func (b *Builder) Build() *Agent {
	systemPrompt := b.agent.composeSystem()
	b.agent.systemPrompt = systemPrompt
	b.agent.mid.Store(uint64(util.GetMilliTime()))
	return b.agent
}
func (l *Agent) SendBlock(block chat.Block) uint64 {
	return l.agentContext.SendBlock(l.store.no, block)
}

func (l *Agent) SendSignalBlock(block chat.Block) uint64 {
	return l.agentContext.SendSignalBlock(l.store.no, block)
}

func (l *Agent) getMid() uint64 {
	return l.mid.Add(1)
}

type Done struct {
	f  func()
	do func()
}

func (d *Done) Done(f func()) {
	d.f = f
	d.do()
}

// HandleRoundMessage 登记本轮完成回调并返回句柄：拿到句柄后调用 Done(f) 才真正把消息写进去，
// 本轮结束时回调 f。
func (l *Agent) HandleRoundMessage(blocks chat.Blocks) *Done {
	done := &Done{}
	l.done = func() {
		done.f()
	}
	done.do = func() {
		l.HandleMessage(blocks)
	}
	return done
}
func (l *Agent) HandleMessage(blocks chat.Blocks) {
	l.runLock.Lock()
	defer l.runLock.Unlock()
	if !l.running {
		l.running = true
		log.Info("[loop] round started", "session", l.agentContext.SessionId())
		qm := chat.NewUserBlock(l.getMid(), blocks, chat.Sent)
		// OnMessage hook
		if l.lifecycle != nil {
			l.lifecycle.OnMessage(l.agentContext, qm)
		}
		l.SendSignalBlock(qm)
		l.inbox.Write(qm)
		util.GoWithRecover(func() {
			l.runLock.Lock()
			defer func() {
				doneBlock := chat.NewDoneBlock()
				doneStart := l.SendBlock(doneBlock)
				// DoneBlock 持久化，保证 WS 历史回放包含轮次结束标记
				l.store.AppendHistory(&chat.Message{Start: doneStart, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{doneBlock}})
				l.store.RecordLastStart(doneStart)
				l.running = false
				l.inbox.Reset()
				log.Info("[loop] round done", "session", l.agentContext.SessionId())
				l.runLock.Unlock()
				if l.done != nil {
					l.done()
				}
				// OnRoundDone hook
				if l.lifecycle != nil {
					l.lifecycle.OnRoundDone(l.agentContext)
				}
			}()
			err := l.store.LoadAllHistory()
			if err != nil {
				log.Error("[loop] LoadAllHistory failed", "session", l.agentContext.SessionId(), "error", err)
				l.SendSignalBlock(chat.NewErrorBlock(fmt.Sprintf("internal error: %v", err)))
				return
			}
			log.Debug("[loop] LoadAllHistory done", "session", l.agentContext.SessionId(), "historyLen", l.store.HistoryLen(), "transferStart", l.agentContext.GetTransferStart())
			l.do()
		}, func(r any) {
			log.Error("[loop] panic recovered", "session", l.agentContext.SessionId(), "panic", r)
			evt := chat.NewErrorBlock(fmt.Sprintf("internal error: %v", r))
			l.SendSignalBlock(evt)
		})
	} else {
		log.Debug("[loop] message queued", "session", l.agentContext.SessionId())
		qm := chat.NewUserBlock(l.getMid(), blocks, chat.Queued)
		l.SendSignalBlock(qm)
		l.inbox.Write(qm)
	}
}
func (l *Agent) composeSystem() string {
	effective := l.config
	toolExecutors := l.toolExecutors
	system := effective.GetSystemPrompt()
	var prompts []string
	for _, exec := range toolExecutors {
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
func (l *Agent) lastUserBlocks() (*chat.BlockGroup, bool) {
	values, fa := l.inbox.ReadAll()
	if fa && len(values) > 0 {
		firstStart := uint64(0)
		lastStart := uint64(0)
		var blocks chat.Blocks
		for _, qm := range values {
			userBlock := chat.NewUserBlock(qm.ID, qm.Content, chat.Consume)
			start := l.SendBlock(userBlock)
			if firstStart == 0 {
				firstStart = start
			}
			lastStart = start
			blocks = append(blocks, userBlock)
		}
		return &chat.BlockGroup{Start: firstStart, Offset: uint64(len(values)), LastStart: lastStart, Content: blocks}, true
	}
	return nil, false
}

func (l *Agent) buildRequest() *chat.Messages {
	toolExecutors := l.toolExecutors

	effective := chat.DefaultConfig()
	effective.Merge(l.config)
	// 拼接后的 system 存放在 Agent 上（composeSystem 结果），不在 l.config 中，
	// Merge 覆盖不到，必须在此显式回填，否则工具引导词会丢失。
	effective.SystemPrompt(l.systemPrompt)

	tools := make([]*chat.ToolFunction, len(toolExecutors))
	for index, exec := range toolExecutors {
		tools[index] = exec.Definition()
	}
	// 注入历史上下文：先消费本轮 inbox 的用户消息并落历史，再整体做上下文过滤
	msg, fa := l.lastUserBlocks()
	if fa {
		l.appendUserMessage(msg)
	}
	history := l.store.History()
	messages := chat.NewMessages(effective, tools)
	for _, m := range history {
		msg := *m
		// 只保留进上下文的块：UserBlock 解包取 Content、ToolResult 下钻过滤、
		// CustomText 等 ForContext()==false 的块剔除；过滤后为空的整条消息跳过
		// （Anthropic 不接受空 content）。
		msg.Content = l.blocksForContext(m.Content)
		if len(msg.Content) == 0 {
			continue
		}
		messages.AddMessage(&msg)
	}
	return messages
}
func (l *Agent) loop() bool {
	l.runLock.Unlock()
	defer l.runLock.Lock()
	l.ctxLock.Lock()
	if l.lCancel != nil {
		l.lCancel()
	}
	l.lContext, l.lCancel = context.WithCancel(l.pContext)
	l.ctxLock.Unlock()

	blockGroup, stopReason, err := l.chatWithStream()

	if err != nil {
		log.Error("[loop] chatWithStream failed", "session", l.agentContext.SessionId(), "error", err)
		l.SendSignalBlock(chat.NewErrorBlock(fmt.Sprintf("internal error: %v", err)))
		return true
	}
	if l.roundStopped() {
		return true
	}
	l.appendAssistantMessage(blockGroup)
	if stopReason == chat.StopReasonToolUse {

		results, toolStop := l.executeTools(blockGroup)
		l.appendUserMessage(results)

		if l.roundStopped() {
			return true
		}
		if toolStop == chat.StopReasonUserWait {
			return true
		}
		return false
	}
	return true

}
func (l *Agent) do() {
	for {
		select {
		case <-l.pContext.Done():
			return
		default:
		}
		if l.loop() && l.inbox.IsEmpty() {
			return
		}
	}
}
func (l *Agent) executeTools(inputBlockGroup *chat.BlockGroup) (*chat.BlockGroup, chat.StopReason) {

	var blockGroups []*chat.BlockGroup
	var results chat.Blocks
	stopReason := chat.StopReason("")
	for _, block := range inputBlockGroup.Content {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		if l.roundStopped() {
			toolsErrorBlock := chat.NewToolsErrorFullTextBlock(tu.ID, "（该工具的执行已被用户停止）")
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{toolsErrorBlock},
			))
			blockGroup := l.SendSingleBlock(toolsErrorBlock)
			blockGroups = append(blockGroups, blockGroup)
			continue
		}

		exec := l.findExecutor(tu.Name)
		if exec == nil {
			log.Warn("[loop] unknown tool", "session", l.agentContext.SessionId(), "tool", tu.Name)
			toolsErrorBlock := chat.NewToolsErrorFullTextBlock(tu.ID, fmt.Sprintf("未知工具: %s", tu.Name))
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{toolsErrorBlock},
			))
			blockGroup := l.SendSingleBlock(toolsErrorBlock)
			blockGroups = append(blockGroups, blockGroup)
			continue
		}
		log.Info("[loop] tool executing", "session", l.agentContext.SessionId(), "tool", tu.Name)
		blockGroup, toolStop, err := l.runTool(tu, exec)
		if err != nil {
			// 工具 panic：补一条占位 tool_result，保证每个 tool_use 都有配对结果进历史，
			// 否则落盘后回放会出现悬空 tool_use。工具异常不连累整轮，继续处理下一个工具。
			log.Error("[loop] tool panicked", "session", l.agentContext.SessionId(), "tool", tu.Name, "panic", err)
			blockGroup = l.SendSingleBlock(chat.NewToolsErrorFullTextBlock(tu.ID, fmt.Sprintf("工具执行异常: %v", err)))
		}
		blockGroups = append(blockGroups, blockGroup)
		results = append(results, chat.NewToolResultBlock(tu.ID, blockGroup.Content))
		if toolStop == chat.StopReasonUserWait {
			stopReason = chat.StopReasonUserWait
		}
	}
	return l.mergeToolsBlockGroup(blockGroups, results), stopReason
}
func (l *Agent) mergeToolsBlockGroup(blockGroups []*chat.BlockGroup, results chat.Blocks) *chat.BlockGroup {
	minStart := blockGroups[0].Start
	maxEnd := blockGroups[0].Start + blockGroups[0].Offset
	lastStart := blockGroups[0].LastStart
	for _, bg := range blockGroups[1:] {
		if bg.Start < minStart {
			minStart = bg.Start
		}
		if end := bg.Start + bg.Offset; end > maxEnd {
			maxEnd = end
		}
		if bg.LastStart > lastStart {
			lastStart = bg.LastStart
		}
	}
	return &chat.BlockGroup{Start: minStart, Offset: maxEnd - minStart, LastStart: lastStart, Content: results}
}

func (l *Agent) SendSingleBlock(block chat.Block) *chat.BlockGroup {
	start := l.SendBlock(block)
	return &chat.BlockGroup{
		Start:     start,
		Offset:    1,
		LastStart: start,
		Content: chat.Blocks{
			block,
		},
	}
}

// runTool 执行工具并收集它产出的 blocks。工具自身 panic 时以 error 返回，
// 由调用方决定后续处理。
func (l *Agent) runTool(tu *chat.ToolUseBlock, exec ToolExecutor) (*chat.BlockGroup, chat.StopReason, error) {

	turn := &Turn{ctx: l.agentContext, args: tu.Input}

	writer := chat.NewBlockStream(l)
	// 工具轮次默认停止原因为 ToolResult（已产出 tool_result，继续调用 LLM）；
	// 需要暂停的工具（如 ask_user_question）在 Execute 内覆盖为 UserWait
	writer.StopReason(chat.StopReasonToolResult)
	err := util.Recover(func() error {
		exec.Execute(turn, chat.NewToolResultBlockStream(writer, tu.ID))
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return writer.ReadBlockGroup(), writer.GetStopReason(), nil
}

// findExecutor 按名称查找已注册的工具执行器。
func (l *Agent) findExecutor(name string) ToolExecutor {
	toolExecutors := l.toolExecutors
	for _, exec := range toolExecutors {
		if exec.Name() == name {
			return exec
		}
	}
	return nil
}

// appendAssistantMessage 将 LLM 返回的 content blocks 作为 assistant 消息写入历史。
func (l *Agent) appendAssistantMessage(blocks *chat.BlockGroup) {
	assistantMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleAssistant, Content: blocks.Content}
	l.store.AppendHistory(assistantMsg)
	// 不在此记录水位：本轮工具还没执行，这里记下的边界会让单独的 tool_use 落盘成悬空配对
}
func (l *Agent) appendUserMessage(blocks *chat.BlockGroup) {
	assistantMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleUser, Content: blocks.Content}
	l.store.AppendHistory(assistantMsg)
	// 水位取本条消息真实的最后一个事件序号：用户输入、已配对的 tool_result 都自成一段完整历史，可安全落盘到此
	l.store.RecordLastStart(blocks.LastStart)
}

func (l *Agent) roundStopped() bool {
	select {
	case <-l.lContext.Done():
		return true
	default:
		return false
	}
}

// blocksForContext 过滤出可进入 LLM 上下文的块。
//
// ToolResultBlock 本身 ForContext()==true，但它的 Content 里可能嵌着
// ForContext()==false 的子块（如 CustomTextBlock 承载的资源卡片 JSON）。
// 只判顶层会把这些子块原样带进请求体，既浪费 token，也会因为
// Anthropic 的 tool_result.content 只接受 text/image 而报 400。
// 所以这里对 ToolResultBlock 下钻一层，按子块的 ForContext() 再过滤一次。
func (l *Agent) blocksForContext(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
		// UserBlock 是事件流包装器，LLM 需要的是里面的 Content（TextBlock 等）
		if ub, ok := b.(*chat.UserBlock); ok {
			result = append(result, l.blocksForContext(ub.Content)...)
			continue
		}
		if tr, ok := b.(*chat.ToolResultBlock); ok {
			result = append(result, l.toolResultForContext(tr))
			continue
		}
		if b.ForContext() {
			result = append(result, b)
		}
	}
	return result
}

// toolResultForContext 返回 tr 的浅拷贝，Content 只保留 ForContext()==true 的子块。
//
// 必须拷贝而非原地修改：history 里存的是同一批指针，原地改会同时毁掉
// 落库内容和断线重连时的回放数据（前端靠回放里的 CustomTextBlock 重建卡片）。
func (l *Agent) toolResultForContext(tr *chat.ToolResultBlock) *chat.ToolResultBlock {
	kept := make(chat.Blocks, 0, len(tr.Content))
	for _, c := range tr.Content {
		if c.ForContext() {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(tr.Content) {
		return tr // 没有需要剔除的子块，避免无谓拷贝
	}
	// 全被剔除时补一句占位：Anthropic 要求每个 tool_use 都有配对且非空的
	// tool_result，留空会导致整条消息被 buildRequest 跳过、进而 400。
	if len(kept) == 0 {
		kept = append(kept, chat.NewFullTextBlock("(结果已输出到前端)"))
	}
	cp := *tr
	cp.Content = kept
	return &cp
}
func (l *Agent) chatWithStream() (*chat.BlockGroup, chat.StopReason, error) {
	stream := chat.NewBlockStream(l)
	err := l.agentContext.GetChat().ChatWithStream(l.lContext, l.buildRequest(), stream)
	if err != nil {
		return nil, "", err
	}
	return stream.ReadBlockGroup(), stream.GetStopReason(), nil
}

func (l *Agent) Stop() {
	l.ctxLock.Lock()
	defer l.ctxLock.Unlock()
	if l.lCancel != nil {
		log.Info("[loop] stop requested", "session", l.agentContext.SessionId())
		l.lCancel()
	}
}
