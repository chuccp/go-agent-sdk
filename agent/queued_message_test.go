package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// ── QueuedMessage 访问器测试 ──

func TestQueuedMessage_Msg(t *testing.T) {
	msg := &chat.RevMessage{Text: "hello"}
	qm := &QueuedMessage{msg: msg}
	if qm.Msg().Text != "hello" {
		t.Errorf("expected 'hello', got %q", qm.Msg().Text)
	}
}

func TestQueuedMessage_Context(t *testing.T) {
	ctx := &SessionContext{sessionId: "sess-1"}
	qm := &QueuedMessage{ctx: ctx}
	if qm.Context().ID() != "sess-1" {
		t.Errorf("expected sessionId 'sess-1', got %q", qm.Context().ID())
	}
}
