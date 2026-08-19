package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type LoopContext interface {
	GetSeq() uint64
	History() []*chat.Message
	ConsumeMessage(qm *QueuedMessage)
}

type UserMessage struct {
	ctx    *SessionContext
	id     uint64
	blocks chat.Blocks
}

type Loop struct {
	inbox       *util.SliceQueue[*UserMessage]
	loopContext LoopContext
	options     *chat.Options
}

func NewLoop(loopContext LoopContext, options *chat.Options) *Loop {
	return &Loop{
		loopContext: loopContext,
		options:     options,
		inbox:       new(util.SliceQueue[*UserMessage]),
	}
}

func (l *Loop) HandleMessage(blocks chat.Blocks) {
	qm := &UserMessage{
		id:     l.loopContext.GetSeq(),
		blocks: blocks,
	}
	l.inbox.Write(qm)
}
func (l *Loop) do() {

}
func (l *Loop) Options(Option ...chat.Option) {
	if l.options == nil {
		l.options = chat.DefaultOptions()
	}
	for _, o := range Option {
		o(l.options)
	}
}
