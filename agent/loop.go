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
	context.Context
	GetSeq() uint64
	ID() string
	SendBlock(no uint64, block chat.Block)
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
	store         *Store0
	lContext      context.Context
	lCancel       context.CancelFunc
	provider      string
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
func (b *LoopBuilder) HistoryStore(historyStore HistoryStore) *LoopBuilder {
	b.loop.store.SetHistoryStore(historyStore)
	return b
}
func (b *LoopBuilder) Provider(provider string) *LoopBuilder {
	b.loop.provider = provider
	return b
}
func (b *LoopBuilder) Build() *Loop {
	return b.loop
}
func NewLoopBuilder(store *Store0, No uint64, loopContext LoopContext) *LoopBuilder {
	pContext, plCancel := context.WithCancel(loopContext)
	return &LoopBuilder{loop: &Loop{
		no:            No,
		loopContext:   loopContext,
		options:       chat.DefaultOptions(),
		toolExecutors: make([]ToolExecutor, 0),
		store:         store,
		pContext:      pContext,
		pCancel:       plCancel,
	}}
}
func (l *Loop) SendBlock(block chat.Block) {
	l.loopContext.SendBlock(l.no, block)
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

	return &chat.Request{}
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
	blocks, stopReason, err := l.chatWithStream()
	if err != nil {
		l.SendBlock(chat.NewErrorBlock(fmt.Sprintf("internal error: %v", err)))
		goto END
	}
	if l.roundStopped() {
		goto END
	}
	if blocks == nil {
		goto END
	}
	l.appendAssistantMessage(blocks)
	if stopReason == chat.StopReasonToolUse {
		results, toolStop := l.executeTools(blocks)
		l.store.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: results})
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

func (l *Loop) executeTools(blocks chat.Blocks) (chat.Blocks, chat.StopReason) {
	var results chat.Blocks
	stopReason := chat.StopReason("")
	for _, block := range blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		if l.roundStopped() {
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewFullTextBlock("（该工具的执行已被用户停止）")},
			))
			continue
		}

		exec := l.findExecutor(tu.Name)
		if exec == nil {
			results = append(results, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewErrorFullTextBlock(fmt.Sprintf("未知工具: %s", tu.Name))},
			))
			continue
		}
		blocks, toolStop := l.runTool(tu, exec)
		results = append(results, chat.NewToolResultBlock(tu.ID, blocks))
		if toolStop == chat.StopReasonUserWait {
			stopReason = chat.StopReasonUserWait
		}
	}
	return results, stopReason

}

func (l *Loop) runTool(tu *chat.ToolUseBlock, exec ToolExecutor) (chat.Blocks, chat.StopReason) {

	turn := &Turn{ctx: l.loopContext, args: tu.Input}

	writer := chat.NewBlockStream(l)
	// 工具轮次默认停止原因为 ToolResult（已产出 tool_result，继续调用 LLM）；
	// 需要暂停的工具（如 ask_user_question）在 Execute 内覆盖为 UserWait
	writer.StopReason(chat.StopReasonToolResult)
	exec.Execute(turn, chat.NewToolResultBlockStream(writer, tu.ID))
	return writer.ReadBlocks(), writer.GetStopReason()
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
func (l *Loop) appendAssistantMessage(blocks chat.Blocks) {
	assistantMsg := &chat.Message{Role: chat.RoleAssistant, Content: blocks}
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

func (l *Loop) chatWithStream() (chat.Blocks, chat.StopReason, error) {
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
	return stream.ReadBlocks(), stream.GetStopReason(), nil
}
