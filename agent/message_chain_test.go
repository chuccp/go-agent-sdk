package agent

import (
	"errors"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// ── MessageFilter 链单元测试 ──

// countingFilter 记录自己被调用的次数并决定是否继续链。
// core=true 时不调用 chain.Next()（模拟 coreMessageFilter 的终端行为）。
type countingFilter struct {
	name    string
	calls   *[]string
	consume bool // true = 不调用 Next，消费消息
	core    bool // true = 终端过滤器，绝不调用 Next
}

func (f *countingFilter) HandleRevMessage(chain MessageFilterChain, _ *QueuedMessage) error {
	*f.calls = append(*f.calls, f.name)
	if f.core || f.consume {
		return nil
	}
	return chain.Next()
}

func TestMessageFilterChain_FullProgression(t *testing.T) {
	var calls []string
	f1 := &countingFilter{name: "f1", calls: &calls}
	f2 := &countingFilter{name: "f2", calls: &calls}
	core := &countingFilter{name: "core", calls: &calls, core: true}

	qm := &QueuedMessage{id: 1}
	chain := newMessageFilterChain(qm, core, f1, f2)
	err := chain.Next()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %v", len(calls), calls)
	}
	if calls[0] != "f1" || calls[1] != "f2" || calls[2] != "core" {
		t.Errorf("expected [f1, f2, core], got %v", calls)
	}
}

func TestMessageFilterChain_EarlyConsume(t *testing.T) {
	var calls []string
	f1 := &countingFilter{name: "f1", calls: &calls, consume: true}
	f2 := &countingFilter{name: "f2", calls: &calls}
	core := &countingFilter{name: "core", calls: &calls, core: true}

	qm := &QueuedMessage{id: 2}
	chain := newMessageFilterChain(qm, core, f1, f2)
	_ = chain.Next()

	if len(calls) != 1 {
		t.Fatalf("expected 1 call (consumed), got %d: %v", len(calls), calls)
	}
	if calls[0] != "f1" {
		t.Errorf("expected f1 to consume, got %v", calls)
	}
}

func TestMessageFilterChain_NoFilters(t *testing.T) {
	var calls []string
	core := &countingFilter{name: "core", calls: &calls, core: true}

	qm := &QueuedMessage{id: 3}
	chain := newMessageFilterChain(qm, core) // 无额外过滤器
	_ = chain.Next()

	if len(calls) != 1 || calls[0] != "core" {
		t.Errorf("expected only core, got %v", calls)
	}
}

func TestMessageFilterChain_TerminalNoNext(t *testing.T) {
	// core 作为终端过滤器不调用 Next()，链正确终止不会栈溢出。
	var calls []string
	f1 := &countingFilter{name: "f1", calls: &calls}
	core := &countingFilter{name: "core", calls: &calls, core: true}

	qm := &QueuedMessage{id: 4}
	chain := newMessageFilterChain(qm, core, f1)
	_ = chain.Next()

	if len(calls) != 2 {
		t.Errorf("expected 2 calls (f1, core), got %d", len(calls))
	}
}

// errorFilter 在拦截时返回错误。
type errorFilter struct {
	err error
}

func (f *errorFilter) HandleRevMessage(_ MessageFilterChain, _ *QueuedMessage) error {
	return f.err
}

func TestMessageFilterChain_ErrorPropagation(t *testing.T) {
	e := &errorFilter{err: errors.New("test error")}

	qm := &QueuedMessage{id: 5}
	chain := newMessageFilterChain(qm, &countingFilter{name: "noreach", calls: &[]string{}, core: true}, e)
	err := chain.Next()

	if err == nil {
		t.Error("expected error propagation, got nil")
	}
}

// QueuedMessage 访问器测试

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
