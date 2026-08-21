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
	ID() string
	SendBlock(no uint64, block chat.Block) uint64
	GetService(provider string) chat.Service
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
			qm.BlockUserType = chat.Consume
			l.SendBlock(qm)
		}
	}
	// 注入历史上下文
	history := l.store.History()
	if len(history) == 0 && !fa {
		return nil
	}
	effective := l.options
	messages := &chat.Request{
		System:   l.composeSystem(),
		Messages: make([]chat.Message, 0, len(history)),
	}
	for _, m := range history {
		msg := *m
		msg.Content = l.blocksForContext(m.Content)
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
		l.saveAndReset()
		goto LOOP
	}
	goto END

END:
	l.saveAndReset()
	if l.inbox.IsEmpty() {
		l.SendBlock(chat.NewDoneBlock())
		return
	}
	goto LOOP

}

func (l *Loop) saveAndReset() {
	if err := l.store.ResetAndSave(); err != nil {
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
	if len(blockGroups) == 1 {
		blockGroups[0].Content = results
		return blockGroups[0]
	}
	var blockGroup chat.BlockGroup
	blockGroup.Start = blockGroups[0].Start
	blockGroup.Offset = blockGroups[len(blockGroups)-1].Offset + blockGroups[len(blockGroups)-1].Start - blockGroup.Start
	blockGroup.Content = results
	return &blockGroup
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

func (l *Loop) blocksForContext(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
		if b.ForContext() {
			result = append(result, b)
		}
	}
	return result
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
