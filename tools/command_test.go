package tools

import (
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// eventRecorder 记录工具流式输出推送的客户端事件。
type eventRecorder struct {
	events []*chat.ClientEvent
}

func (r *eventRecorder) AddEvent(evt *chat.ClientEvent) {
	r.events = append(r.events, evt)
}

// TestCommand_StreamingOutput 验证命令输出逐行流式回显：
// 执行过程中实时推送 chunk 事件，结束后完整输出也被收集进 tool_result。
func TestCommand_StreamingOutput(t *testing.T) {
	rec := &eventRecorder{}
	w := agent.NewBlockStream(rec)
	tool := NewCommandTool()
	err := tool.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"command": "echo streaming-test"})), w)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// 实时收到了携带输出的 chunk 事件
	found := false
	for _, e := range rec.events {
		if e.EventType == chat.EventTypeChunk && strings.Contains(e.Content, "streaming-test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("未收到携带输出的 chunk 事件，共 %d 个事件", len(rec.events))
	}

	// 输出同时进入 tool_result
	blocks, _ := w.ReadBlocks()
	var text string
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			text += tb.Text
		}
	}
	if !strings.Contains(text, "streaming-test") {
		t.Errorf("输出未被收集: %q", text)
	}
}

// TestCommand_StreamingMultiline 验证多行输出逐行推送且顺序完整。
func TestCommand_StreamingMultiline(t *testing.T) {
	if isWindows() {
		t.Skip("printf 非 Windows cmd 内建命令，仅 POSIX 平台验证")
	}
	rec := &eventRecorder{}
	w := agent.NewBlockStream(rec)
	tool := NewCommandTool()
	// printf 在 sh 与 cmd 下均可用；两行输出应产生至少 2 个 chunk 事件
	err := tool.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"command": "printf \"aaa\\nbbb\\n\""})), w)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	chunks := 0
	var streamed string
	for _, e := range rec.events {
		if e.EventType == chat.EventTypeChunk {
			chunks++
			streamed += e.Content
		}
	}
	if chunks < 2 {
		t.Errorf("期望至少 2 个逐行 chunk 事件，实际 %d", chunks)
	}
	if !strings.Contains(streamed, "aaa") || !strings.Contains(streamed, "bbb") {
		t.Errorf("流式内容不完整: %q", streamed)
	}
}
