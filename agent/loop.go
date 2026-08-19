package agent

import (
	"context"
	"fmt"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type LoopContext interface {
	context.Context
	GetSeq() uint64
	History() []*chat.Message
	SendBlock(no uint64, block chat.Block)
	ChatComplete(ctx context.Context, chatMessages *chat.Request, stream *chat.BlockStream) error
}

type Loop struct {
	no          uint64
	inbox       *util.SliceQueue[*chat.UserBlock]
	loopContext LoopContext
	options     *chat.Options
	running     bool
	pContext    context.Context
	pCancel     context.CancelFunc

	lContext context.Context
	lCancel  context.CancelFunc
}

func NewLoop(loopContext LoopContext, No uint64, options *chat.Options) *Loop {
	pContext, pCancel := context.WithCancel(loopContext)
	return &Loop{
		no:          No,
		loopContext: loopContext,
		options:     options,
		inbox:       new(util.SliceQueue[*chat.UserBlock]),
		running:     false,
		pContext:    pContext,
		pCancel:     pCancel,
	}
}
func (l *Loop) SendBlock(block chat.Block) {
	l.loopContext.SendBlock(l.no, block)
}

func (l *Loop) HandleMessage(blocks chat.Blocks) {
	if !l.running {
		l.running = true
		qm := chat.NewUserBlock(l.loopContext.GetSeq(), blocks, chat.Sent)
		l.SendBlock(qm)
		l.inbox.Write(qm)
		util.GoWithRecover(func() {
			l.do()
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

	for l.running {
		if l.lCancel != nil {
			l.lCancel()
		}
		l.lContext, l.lCancel = context.WithCancel(l.pContext)
		blocks, stopReason, err := l.chatWithStream()
		if err != nil {
			return
		}
		if stopReason != "" {

		}
		if blocks == nil {
			// inbox 为空，executeRound 已将 running 置 false
			continue
		}

	}

}
func (l *Loop) Options(Option ...chat.Option) {
	if l.options == nil {
		l.options = chat.DefaultOptions()
	}
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
