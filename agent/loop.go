package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type LoopContext interface {
	context.Context
	GetSeq() uint64
	ID() string
	History() []*chat.Message
	SendBlock(no uint64, block chat.Block)
	ChatComplete(ctx context.Context, chatMessages *chat.Request, stream *chat.BlockStream) error
}

type Loop struct {
	no            uint64
	inbox         *util.SliceQueue[*chat.UserBlock]
	loopContext   LoopContext
	options       *chat.Options
	events        *Store
	toolExecutors []ToolExecutor

	running  bool
	pContext context.Context
	pCancel  context.CancelFunc
	runLock  sync.Mutex
	lContext context.Context
	lCancel  context.CancelFunc
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
	b.loop.events.SetHistoryStore(historyStore)
	return b
}
func (b *LoopBuilder) Build() *Loop {
	return b.loop
}
func NewLoopBuilder(No uint64, loopContext LoopContext) *LoopBuilder {
	return &LoopBuilder{loop: &Loop{
		no:            No,
		loopContext:   loopContext,
		options:       chat.DefaultOptions(),
		toolExecutors: make([]ToolExecutor, 0),
		events:        NewStore(loopContext.ID()),
	}}
}
func (l *Loop) SendBlock(block chat.Block) {
	l.loopContext.SendBlock(l.no, block)
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
func (l *Loop) buildRequest() *chat.Request {
	return &chat.Request{}
}

func (l *Loop) do() {

	for {

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
			if l.inbox.IsEmpty() {
				break
			}
			continue
		}
		if l.roundStopped() {
			if l.inbox.IsEmpty() {
				break
			}
			continue
		}
		if blocks == nil {
			if l.inbox.IsEmpty() {
				break
			}
			continue
		}
		if stopReason == chat.StopReasonToolUse {
			l.appendAssistantMessage(blocks)
		}

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
	l.events.AppendTempHistory(assistantMsg)
}

func (l *Loop) roundStopped() bool {
	select {
	case <-l.lContext.Done():
		return true
	default:
		return false
	}
}

func (l *Loop) UpdateOptions(Option ...chat.Option) {
	for _, o := range Option {
		o(l.options)
	}
}

func (l *Loop) chatWithStream() (chat.Blocks, chat.StopReason, error) {
	stream := chat.NewBlockStream(l)
	err := l.loopContext.ChatComplete(l.lContext, l.buildRequest(), stream)
	if err != nil {
		return nil, "", err
	}
	return stream.ReadBlocks(), stream.GetStopReason(), nil
}
