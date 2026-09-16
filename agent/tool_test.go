package agent

import (
	"context"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

func TestToolArgsDisplay_CommandField(t *testing.T) {
	got := toolArgsDisplay(value.NewObjectFromMap(map[string]any{"command": "ls -la", "cwd": "/tmp"}))
	if got != "ls -la" {
		t.Errorf("expected command value, got %q", got)
	}
}

func TestToolArgsDisplay_EmptyCommand(t *testing.T) {
	got := toolArgsDisplay(value.NewObjectFromMap(map[string]any{"command": "", "other": "val"}))
	if got == "" {
		t.Error("expected JSON fallback, got empty string")
	}
}

func TestToolArgsDisplay_NoCommand(t *testing.T) {
	got := toolArgsDisplay(value.NewObjectFromMap(map[string]any{"key": "value"}))
	expected := `{"key":"value"}`
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestToolArgsDisplay_EmptyArgs(t *testing.T) {
	got := toolArgsDisplay(value.NewObjectFromMap(map[string]any{}))
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestToolArgsDisplay_NilArgs(t *testing.T) {
	got := toolArgsDisplay(nil)
	if got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

func TestTurn_Args(t *testing.T) {
	args := value.NewObjectFromMap(map[string]any{"a": "1", "b": 2})
	turn := &Turn{args: args}
	got := turn.Args()
	if got.GetString("a") != "1" || got.GetString("b") != "2" {
		t.Errorf("Args() returned unexpected: %v", got)
	}
}

func TestTurn_Context_Nil(t *testing.T) {
	turn := NewTurn(value.NewObjectFromMap(map[string]any{"x": "y"}))
	if turn.AgentContext() != nil {
		t.Error("expected nil context from NewTurn")
	}
}

func TestTurn_Context_WithSession(t *testing.T) {
	ctx := &RunContext{session: &SessionContext{sessionId: "test"}}
	turn := &Turn{ctx: ctx}
	if turn.AgentContext().SessionId() != "test" {
		t.Errorf("expected sessionId 'test', got %q", turn.AgentContext().SessionId())
	}
}

// Build 交付给工具的 RunContext，Ctx() 必须是能用的。
//
// loopContext 要到 loop() 里才创建，Build 时还是 nil；Ctx() 靠 pContext 兜底
// （WithoutCancel(会话)），否则工具把 turn.Context().Ctx() 递给 net/http（client 带
// Timeout）就会崩：setRequestCancel 会读 req.Context().Deadline()，整个工具只回一句
// nil pointer dereference。
func TestBuiltRunContextCarriesUsableContext(t *testing.T) {
	built := NewBuilder(&SessionContext{sessionId: "s1", Context: context.Background()}, nil).Build()

	ctx := built.agentContext.Ctx()
	if ctx == nil {
		t.Fatal("loop() 之前 Ctx() 应回落到 pContext，不能为 nil")
	}
	select {
	case <-ctx.Done():
		t.Error("会话还没取消，Done() 不该已经关闭")
	default:
	}
	if _, ok := ctx.Deadline(); ok {
		t.Error("会话 ctx 不该带 deadline")
	}
}

func TestNewTurn(t *testing.T) {
	turn := NewTurn(value.NewObjectFromMap(map[string]any{"key": "value"}))
	if turn.Args().GetString("key") != "value" {
		t.Error("NewTurn should preserve args")
	}
	if turn.AgentContext() != nil {
		t.Error("NewTurn should have nil context")
	}
}

// fakeTool 用于验证 ToolExecutor 接口。
type fakeTool struct{}

func (f *fakeTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "fake", Description: "a fake tool"}
}
func (f *fakeTool) Name() string                                   { return "fake" }
func (f *fakeTool) UsagePrompt() string                            { return "" }
func (f *fakeTool) Execute(_ *Turn, _ *chat.ToolResultBlockStream) {}

var _ ToolExecutor = (*fakeTool)(nil)
