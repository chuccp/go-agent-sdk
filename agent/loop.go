package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type LoopContext interface {
	context.Context
	SessionId() string
	SendBlock(no uint64, block chat.Block) uint64
	GetChat() *chat.Chat
	AppendMainAssistantMessage(blocks *chat.BlockGroup)
	AppendMainUserMessage(blocks *chat.BlockGroup)
}

type Loop struct {
	no            uint64
	inbox         *util.SliceQueue[*chat.UserBlock]
	loopContext   LoopContext
	service       chat.Service
	running       bool
	pContext      context.Context
	pCancel       context.CancelFunc
	runLock       sync.Mutex
	store         *Store
	lContext      context.Context
	lCancel       context.CancelFunc
	seq           uint64
	toolExecutors []ToolExecutor
	config        *chat.Config
	systemPrompt  string
}

type LoopBuilder struct {
	loop *Loop
}

func NewLoopBuilder(No uint64, loopContext LoopContext) *LoopBuilder {
	pContext, plCancel := context.WithCancel(loopContext)
	return &LoopBuilder{loop: &Loop{
		no:            No,
		loopContext:   loopContext,
		toolExecutors: make([]ToolExecutor, 0),
		inbox:         new(util.SliceQueue[*chat.UserBlock]),
		pContext:      pContext,
		pCancel:       plCancel,
		config:        chat.DefaultConfig(),
	}}
}
func (b *LoopBuilder) Store(store *Store) *LoopBuilder {
	b.loop.store = store
	return b
}
func (b *LoopBuilder) Config(config ...*chat.Config) *LoopBuilder {
	b.loop.config.Merge(config...)
	return b
}
func (b *LoopBuilder) ToolExecutor(toolExecutor ...ToolExecutor) *LoopBuilder {
	b.loop.toolExecutors = append(b.loop.toolExecutors, toolExecutor...)
	return b
}

func (b *LoopBuilder) Build() *Loop {
	b.loop.systemPrompt = b.loop.composeSystem()
	b.loop.config.SetSystemPrompt(b.loop.systemPrompt)
	return b.loop
}
func (l *Loop) SendBlock(block chat.Block) uint64 {
	start := l.loopContext.SendBlock(l.no, block)
	return start
}

func (l *Loop) getSeq() uint64 {
	return atomic.AddUint64(&l.seq, 1)
}

func (l *Loop) HandleMessage(blocks chat.Blocks) {
	l.runLock.Lock()
	defer l.runLock.Unlock()
	if !l.running {
		l.running = true
		qm := chat.NewUserBlock(l.getSeq(), blocks, chat.Sent)
		l.SendBlock(qm)
		l.inbox.Write(qm)
		util.GoWithRecover(func() {
			l.runLock.Lock()
			defer func() {
				l.store.RecordDone(l.SendBlock(chat.NewDoneBlock()))
				l.running = false
				l.inbox.Reset()
				l.runLock.Unlock()
			}()
			l.do()
		}, func(r any) {
			evt := chat.NewErrorBlock(fmt.Sprintf("internal error: %v", r))
			l.SendBlock(evt)
		})
	} else {
		qm := chat.NewUserBlock(l.getSeq(), blocks, chat.Queued)
		l.SendBlock(qm)
		l.inbox.Write(qm)
	}
}
func (l *Loop) composeSystem() string {
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
func (l *Loop) buildRequest() *chat.Messages {
	effective := l.config
	toolExecutors := l.toolExecutors
	values, fa := l.inbox.ReadAll()
	if fa {
		firstStart := uint64(0)

		var blocks chat.Blocks

		for _, qm := range values {

			userBlock := chat.NewUserBlock(qm.ID, qm.Content, chat.Consume)

			start := l.SendBlock(userBlock)
			if firstStart == 0 {
				firstStart = start
			}
			blocks = append(blocks, userBlock)
		}
		// 记录 Start/Offset，供 mergeMessages 精确去重；否则 user 消息的
		// Start/Offset 为 0，会把去重区间错误地扩展到 [0, ...) 覆盖所有事件。
		offset := uint64(len(values))
		if offset > 0 && firstStart > 0 {
			l.store.AppendHistory(&chat.Message{Start: firstStart, Offset: uint64(len(values)), Role: chat.RoleUser, Content: blocks})
		}
	}
	// 注入历史上下文
	history := l.store.History()
	if len(history) == 0 && !fa {
		return nil
	}
	messages := &chat.Messages{
		Messages: make([]chat.Message, 0, len(history)),
		Config:   effective,
	}
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		msg := *m
		msg.Content = l.blocksForContext(m.Content)
		if len(msg.Content) == 0 {
			continue
		}
		messages.Messages = append(messages.Messages, msg)
	}
	// 翻转（倒序收集的）
	for i, j := 0, len(messages.Messages)-1; i < j; i, j = i+1, j-1 {
		messages.Messages[i], messages.Messages[j] = messages.Messages[j], messages.Messages[i]
	}
	if len(toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(toolExecutors))
		for _, exec := range toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}
func (l *Loop) loop() bool {
	l.runLock.Unlock()
	defer l.runLock.Lock()
	if l.lCancel != nil {
		l.lCancel()
	}
	l.lContext, l.lCancel = context.WithCancel(l.pContext)

	blockGroup, stopReason, err := l.chatWithStream()

	if err != nil {
		l.SendBlock(chat.NewErrorBlock(fmt.Sprintf("internal error: %v", err)))
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
func (l *Loop) do() {

LOOP:
	select {
	case <-l.pContext.Done():
		return
	default:
	}
	if l.loop() {
		goto END
	}
	goto LOOP
END:
	if l.inbox.IsEmpty() {
		return
	}
	goto LOOP
}
func (l *Loop) executeTools(inputBlockGroup *chat.BlockGroup) (*chat.BlockGroup, chat.StopReason) {

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
			toolsErrorBlock := chat.NewToolsErrorFullTextBlock(tu.ID, fmt.Sprintf("未知工具: %s", tu.Name))
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{toolsErrorBlock},
			))
			blockGroup := l.SendSingleBlock(toolsErrorBlock)
			blockGroups = append(blockGroups, blockGroup)
			continue
		}
		blockGroup, toolStop := l.runTool(tu, exec)
		blockGroups = append(blockGroups, blockGroup)
		results = append(results, chat.NewToolResultBlock(tu.ID, blockGroup.Content))
		if toolStop == chat.StopReasonUserWait {
			stopReason = chat.StopReasonUserWait
		}
	}
	return l.mergeToolsBlockGroup(blockGroups, results), stopReason
}

func (l *Loop) mergeToolsBlockGroup(blockGroups []*chat.BlockGroup, results chat.Blocks) *chat.BlockGroup {
	var bg chat.BlockGroup
	bg.Start = blockGroups[0].Start
	if len(blockGroups) == 1 {
		bg.Offset = blockGroups[0].Offset
	} else {
		bg.Offset = blockGroups[len(blockGroups)-1].Offset + blockGroups[len(blockGroups)-1].Start - bg.Start
	}
	bg.Content = results
	return &bg
}

func (l *Loop) SendSingleBlock(block chat.Block) *chat.BlockGroup {
	start := l.SendBlock(block)
	return &chat.BlockGroup{
		Start:  start,
		Offset: 1,
		Content: chat.Blocks{
			block,
		},
	}
}

func (l *Loop) runTool(tu *chat.ToolUseBlock, exec ToolExecutor) (*chat.BlockGroup, chat.StopReason) {

	turn := &Turn{ctx: l.loopContext, args: tu.Input}

	writer := chat.NewBlockStream(l)
	// 工具轮次默认停止原因为 ToolResult（已产出 tool_result，继续调用 LLM）；
	// 需要暂停的工具（如 ask_user_question）在 Execute 内覆盖为 UserWait
	writer.StopReason(chat.StopReasonToolResult)
	exec.Execute(turn, chat.NewToolResultBlockStream(writer, tu.ID))
	return writer.ReadBlockGroup(), writer.GetStopReason()
}

// findExecutor 按名称查找已注册的工具执行器。
func (l *Loop) findExecutor(name string) ToolExecutor {
	toolExecutors := l.toolExecutors
	for _, exec := range toolExecutors {
		if exec.Name() == name {
			return exec
		}
	}
	return nil
}

// appendAssistantMessage 将 LLM 返回的 content blocks 作为 assistant 消息写入历史。
func (l *Loop) appendAssistantMessage(blocks *chat.BlockGroup) {
	assistantMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleAssistant, Content: blocks.Content}
	l.store.AppendHistory(assistantMsg)
}
func (l *Loop) appendUserMessage(blocks *chat.BlockGroup) {
	assistantMsg := &chat.Message{Start: blocks.Start, Offset: blocks.Offset, Role: chat.RoleUser, Content: blocks.Content}
	l.store.AppendHistory(assistantMsg)
}

func (l *Loop) roundStopped() bool {
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
func (l *Loop) blocksForContext(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
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
func (l *Loop) toolResultForContext(tr *chat.ToolResultBlock) *chat.ToolResultBlock {
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
func (l *Loop) chatWithStream() (*chat.BlockGroup, chat.StopReason, error) {
	stream := chat.NewBlockStream(l)
	err := l.loopContext.GetChat().ChatWithStream(l.lContext, l.buildRequest(), stream)
	if err != nil {
		return nil, "", err
	}
	return stream.ReadBlockGroup(), stream.GetStopReason(), nil
}

func (l *Loop) Stop() {
	if l.lCancel != nil {
		l.lCancel()
	}
}
