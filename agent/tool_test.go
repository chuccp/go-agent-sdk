package agent

import (
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
	if turn.Context() != nil {
		t.Error("expected nil context from NewTurn")
	}
}

func TestTurn_Context_WithSession(t *testing.T) {
	ctx := &SessionContext{sessionId: "test"}
	turn := &Turn{ctx: ctx}
	if turn.Context().ID() != "test" {
		t.Errorf("expected sessionId 'test', got %q", turn.Context().ID())
	}
}

func TestNewTurn(t *testing.T) {
	turn := NewTurn(value.NewObjectFromMap(map[string]any{"key": "value"}))
	if turn.Args().GetString("key") != "value" {
		t.Error("NewTurn should preserve args")
	}
	if turn.Context() != nil {
		t.Error("NewTurn should have nil context")
	}
}

// fakeTool 用于验证 ToolExecutor 接口。
type fakeTool struct{}

func (f *fakeTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "fake", Description: "a fake tool"}
}
func (f *fakeTool) Name() string                         { return "fake" }
func (f *fakeTool) UsagePrompt() string                  { return "" }
func (f *fakeTool) Execute(_ *Turn, _ *chat.BlockStream) {}

var _ ToolExecutor = (*fakeTool)(nil)
