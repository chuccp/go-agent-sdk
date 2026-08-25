package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"emperror.dev/errors"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type LoopContext interface {
	GetSeq() uint64
	SessionId() string
	SendBlock(no uint64, block chat.Block) uint64
	GetService(provider string) chat.Service
	AppendMainAssistantMessage(blocks *chat.BlockGroup)
	AppendMainUserMessage(blocks *chat.BlockGroup)
}

type Loop struct {
	no            uint64
	inbox         *util.SliceQueue[*chat.UserBlock]
	loopContext   LoopContext
	options       *chat.Options
	toolExecutors []ToolExecutor
	service       chat.Service
	running       bool
	pContext      context.Context
	pCancel       context.CancelFunc
	runLock       sync.Mutex
	store         *Store
	compressor    Compressor
	lContext      context.Context
	lCancel       context.CancelFunc
	provider      string
	done          func()
}

type LoopBuilder struct {
	loop *Loop
}

func (b *LoopBuilder) ToolExecutor(toolExecutors ...ToolExecutor) *LoopBuilder {
	b.loop.toolExecutors = append(b.loop.toolExecutors, toolExecutors...)
	return b
}
func (b *LoopBuilder) Options(Option ...chat.Option) *LoopBuilder {
	for _, o := range Option {
		o(b.loop.options)
	}
	return b
}
func (b *LoopBuilder) Provider(provider string) *LoopBuilder {
	b.loop.provider = provider
	return b
}
func (b *LoopBuilder) Done(done func()) *LoopBuilder {
	b.loop.done = done
	return b
}
func (b *LoopBuilder) Compressor(c Compressor) *LoopBuilder {
	b.loop.compressor = c
	return b
}
func (b *LoopBuilder) Build() *Loop {
	return b.loop
}
func NewLoopBuilder(ctx context.Context, loopContext LoopContext, No uint64, store *Store) *LoopBuilder {
	pContext, plCancel := context.WithCancel(ctx)
	return &LoopBuilder{loop: &Loop{
		no:            No,
		loopContext:   loopContext,
		options:       chat.DefaultOptions(),
		toolExecutors: make([]ToolExecutor, 0),
		store:         store,
		pContext:      pContext,
		pCancel:       plCancel,
		inbox:         new(util.SliceQueue[*chat.UserBlock]),
	}}
}

func (l *Loop) SendBlock(block chat.Block) uint64 {
	return l.loopContext.SendBlock(l.no, block)
}
func (l *Loop) UpdateProvider(provider string) {
	l.provider = provider
}
func (l *Loop) HandleMessage(blocks chat.Blocks) {
	l.runLock.Lock()
	defer l.runLock.Unlock()
	if !l.running {
		l.running = true
		qm := chat.NewUserBlock(l.loopContext.GetSeq(), blocks, chat.Sent)
		l.SendBlock(qm)
		l.inbox.Write(qm)
		util.GoWithRecover(func() {
			l.runLock.Lock()
			defer l.runLock.Unlock()
			l.do()
			l.running = false
			if l.done != nil {
				l.done()
			}
		}, func(r any) {
			evt := chat.NewErrorBlock(fmt.Sprintf("internal error: %v", r))
			l.SendBlock(evt)
		})
	} else {
		qm := chat.NewUserBlock(l.loopContext.GetSeq(), blocks, chat.Queued)
		l.SendBlock(qm)
		l.inbox.Write(qm)
	}
}
func (l *Loop) composeSystem() string {
	system := l.options.SystemPrompt
	var prompts []string
	for _, exec := range l.toolExecutors {
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
func (l *Loop) buildRequest() *chat.Request {

	values, fa := l.inbox.ReadAll()
	if fa {
		for _, qm := range values {
			// 不复用 qm（已作为 sent/queued 事件发送），而是新建 consume 块：
			// 否则 mutate qm.BlockUserType 会同时改写之前事件的序列化结果，
			// 前端可能收到两条 consume 而把用户消息显示两遍。
			start := l.SendBlock(chat.NewUserBlock(qm.ID, qm.Content, chat.Consume))
			// 记录 Start/Offset，供 mergeMessages 精确去重；否则 user 消息的
			// Start/Offset 为 0，会把去重区间错误地扩展到 [0, ...) 覆盖所有事件。
			l.store.AppendHistory(&chat.Message{Start: start, Offset: 1, Role: chat.RoleUser, Content: qm.Content})
		}
	}
	// 注入历史上下文
	history := l.store.History()
	if len(history) == 0 && !fa {
		return nil
	}

	// 压缩（压缩器内部自行持久化标记和摘要）
	var summaryMsg *chat.Message
	if l.compressor != nil {
		summaryMsg = l.compressor.Compress(l.loopContext, history)
	}

	// 倒序过滤：从最新消息往前，跳过已压缩的
	effective := l.options
	messages := &chat.Request{
		System:   l.composeSystem(),
		Messages: make([]chat.Message, 0, len(history)),
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

	// 摘要消息插入最前面
	if summaryMsg != nil {
		messages.Messages = append([]chat.Message{*summaryMsg}, messages.Messages...)
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

	if len(l.toolExecutors) > 0 {
		tools := make([]chat.ToolFunction, 0, len(l.toolExecutors))
		for _, exec := range l.toolExecutors {
			tools = append(tools, *exec.Definition())
		}
		messages.Tools = tools
	}

	return messages
}

func (l *Loop) do() {

LOOP:

	select {
	case <-l.pContext.Done():
		return
	default:
	}

	if l.lCancel != nil {
		l.lCancel()
	}
	l.lContext, l.lCancel = context.WithCancel(l.pContext)
	blockGroup, stopReason, err := l.chatWithStream()
	if err != nil {
		l.SendBlock(chat.NewErrorBlock(fmt.Sprintf("internal error: %v", err)))
		goto END
	}
	if l.roundStopped() {
		goto END
	}

	l.appendAssistantMessage(blockGroup)
	if stopReason == chat.StopReasonToolUse {
		results, toolStop := l.executeTools(blockGroup)
		l.appendUserMessage(results)
		if l.roundStopped() {
			goto END
		}
		if toolStop == chat.StopReasonUserWait {
			goto END
		}
		l.save()
		goto LOOP
	}
	goto END

END:
	l.save()
	if l.inbox.IsEmpty() {
		l.SendBlock(chat.NewDoneBlock())
		return
	}
	goto LOOP

}

func (l *Loop) save() {
	if err := l.store.Save(); err != nil {
		log.Printf("[chatSession] save history failed: %v", err)
	}
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
	for _, exec := range l.toolExecutors {
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

func (l *Loop) UpdateOptions(Option ...chat.Option) {
	for _, o := range Option {
		o(l.options)
	}
}

func (l *Loop) chatWithStream() (*chat.BlockGroup, chat.StopReason, error) {
	stream := chat.NewBlockStream(l)
	if util.IsBlank(l.provider) {
		return nil, "", errors.New("blank provider")
	}
	service := l.loopContext.GetService(l.provider)
	if service == nil {
		return nil, "", errors.Errorf("service not found: %s", l.provider)
	}
	err := service.ChatWithStream(l.lContext, l.buildRequest(), stream)
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
