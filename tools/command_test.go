package tools

import (
	"strings"
	"testing"
	"time"

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

// readEventsUntilIdle 读取 client 事件直到空闲（ReadEvent 无事件时会阻塞，
// 故在协程中读取并以超时判定流已排空）。
func readEventsUntilIdle(client *agent.Client, idle time.Duration) []*chat.ClientEvent {
	var events []*chat.ClientEvent
	for {
		ch := make(chan *chat.ClientEvent, 1)
		go func() { ch <- client.ReadEvent() }()
		select {
		case evt := <-ch:
			if evt == nil {
				return events
			}
			events = append(events, evt)
		case <-time.After(idle):
			return events
		}
	}
}

// TestCommand_StreamingOutput 验证命令输出逐行流式回显：
// 执行过程中实时推送 chunk 事件，结束后完整输出也被收集进 tool_result。
func TestCommand_StreamingOutput(t *testing.T) {
	rec := &eventRecorder{}
	w := chat.NewBlockStream(rec)
	tool := NewCommandTool()
	tool.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"command": "echo streaming-test"})), w)

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
	blocks := w.ReadBlocks()
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

// TestCommand_CommandEvent 验证有 SessionContext 时输出以专属 command 事件增量推送：
// 事件携带命令（Message）与输出（Content），不再产生 chunk；完整输出进入 tool_result。
func TestCommand_CommandEvent(t *testing.T) {
	manager := agent.NewAgent()
	ctx := manager.SessionContext("cmd-s1")
	client, err := manager.GetClient("cmd-s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	w := chat.NewBlockStream(nil)
	tool := NewCommandTool()
	tool.Execute(agent.NewTurnWithContext(ctx, value.NewObjectFromMap(map[string]any{"command": "echo event-test"})), w)

	// 收到携带命令与输出的 command 事件
	events := readEventsUntilIdle(client, 300*time.Millisecond)
	var cmdEvt *chat.ClientEvent
	for _, evt := range events {
		if evt.EventType == EventTypeCommand {
			cmdEvt = evt
		}
		if evt.EventType == chat.EventTypeChunk {
			t.Errorf("command 事件路径不应再产生 chunk 事件")
		}
	}
	if cmdEvt == nil {
		t.Fatal("未收到 command 事件")
	}
	if cmdEvt.Message != "echo event-test" {
		t.Errorf("事件未携带命令，Message = %q", cmdEvt.Message)
	}
	if !strings.Contains(cmdEvt.Content, "event-test") {
		t.Errorf("事件未携带输出，Content = %q", cmdEvt.Content)
	}

	// 完整输出同时进入 tool_result
	var text string
	for _, b := range w.ReadBlocks() {
		if tb, ok := b.(*chat.TextBlock); ok {
			text += tb.Text
		}
	}
	if !strings.Contains(text, "event-test") {
		t.Errorf("输出未被收集: %q", text)
	}
}

// TestCommand_CustomEventFactory 验证 WithCommandEventFactory 定制命令事件构造器。
func TestCommand_CustomEventFactory(t *testing.T) {
	manager := agent.NewAgent()
	ctx := manager.SessionContext("cmd-s2")
	client, err := manager.GetClient("cmd-s2", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	w := chat.NewBlockStream(nil)
	tool := NewCommandTool(WithCommandEventFactory(func(command, output string) *chat.ClientEvent {
		return &chat.ClientEvent{EventSource: chat.SourceAI, EventType: "custom_command", Message: command, Content: output}
	}))
	tool.Execute(agent.NewTurnWithContext(ctx, value.NewObjectFromMap(map[string]any{"command": "echo factory-test"})), w)

	found := false
	for _, evt := range readEventsUntilIdle(client, 300*time.Millisecond) {
		if evt.EventType == "custom_command" && strings.Contains(evt.Content, "factory-test") {
			found = true
		}
	}
	if !found {
		t.Error("未收到定制工厂产生的 custom_command 事件")
	}
}

// TestCommand_StreamingMultiline 验证多行输出逐行推送且顺序完整。
func TestCommand_StreamingMultiline(t *testing.T) {
	if isWindows() {
		t.Skip("printf 非 Windows cmd 内建命令，仅 POSIX 平台验证")
	}
	rec := &eventRecorder{}
	w := chat.NewBlockStream(rec)
	tool := NewCommandTool()
	// printf 在 sh 与 cmd 下均可用；两行输出应产生至少 2 个 chunk 事件
	tool.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"command": "printf \"aaa\\nbbb\\n\""})), w)

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
