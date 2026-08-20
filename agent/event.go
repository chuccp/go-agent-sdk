package agent

import "github.com/chuccp/go-agent-sdk/chat"

type Event struct {
	No    uint64     `json:"no"`
	Seq   uint64     `json:"seq"`
	Block chat.Block `json:"block"`
}

func NewEvent(no uint64, seq uint64, block chat.Block) *Event {
	return &Event{
		No:    no,
		Seq:   seq,
		Block: block,
	}
}

type Position struct {
	start uint64
}

type SendEvent struct {
	seq uint64 // 事件序号计数器（下一个 event.Seq），entries 被裁空也不回退
}
