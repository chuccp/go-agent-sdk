package agent_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// ── Fake providers for lifecycle testing ──

// singleResponseProvider 返回单条消息后结束。
type singleResponseProvider struct {
	stopReason chat.StopReason
	text       string
	toolUse    *chat.ToolUseBlock // 非 nil 时返回 tool_use
}

func (f *singleResponseProvider) ChatWithStream(_ context.Context, _ *chat.Request, w chat.StreamWriter) error {
	w.Write(&chat.MessageStartEvent{ID: "m1", Model: "fake", Role: "assistant"})
	if f.toolUse != nil {
		w.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewToolUseBlock(f.toolUse.ID, f.toolUse.Name, f.toolUse.Input)})
		w.Write(&chat.ContentBlockStopEvent{Index: 0})
		w.Write(&chat.MessageDeltaEvent{StopReason: chat.StopReasonToolUse})
	} else {
		w.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewTextBlock("")})
		w.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: f.text}})
		w.Write(&chat.ContentBlockStopEvent{Index: 0})
		w.Write(&chat.MessageDeltaEvent{StopReason: f.stopReason})
	}
	w.Write(&chat.MessageStopEvent{})
	return nil
}

// orderedProvider 按顺序返回响应列表。
type orderedProvider struct {
	responses []orderedResponse
	idx       atomic.Int32
}

type orderedResponse struct {
	blocks []blockSpec
	reason chat.StopReason
	text   string // 快捷方式：单文本块
}

type blockSpec struct {
	blockType chat.ContentType
	text      string
	toolID    string
	toolName  string
}

func (f *orderedProvider) ChatWithStream(_ context.Context, _ *chat.Request, w chat.StreamWriter) error {
	i := int(f.idx.Add(1)) - 1
	if i >= len(f.responses) {
		// 超出预设响应，返回单文本
		w.Write(&chat.MessageStartEvent{ID: "m-fallback", Model: "fake", Role: "assistant"})
		w.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewTextBlock("")})
		w.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: "fallback"}})
		w.Write(&chat.ContentBlockStopEvent{Index: 0})
		w.Write(&chat.MessageDeltaEvent{StopReason: chat.StopReasonEndTurn})
		w.Write(&chat.MessageStopEvent{})
		return nil
	}

	resp := f.responses[i]
	w.Write(&chat.MessageStartEvent{ID: "m1", Model: "fake", Role: "assistant"})

	// 如果设置了 text，使用快捷方式
	if resp.text != "" {
		w.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewTextBlock("")})
		w.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: resp.text}})
		w.Write(&chat.ContentBlockStopEvent{Index: 0})
	} else {
		for _, bs := range resp.blocks {
			var cb chat.Block
			switch bs.blockType {
			case chat.ContentTypeText:
				cb = chat.NewTextBlock(bs.text)
			case chat.ContentTypeToolUse:
				cb = chat.NewToolUseBlock(bs.toolID, bs.toolName, map[string]any{"command": "echo hi"})
			case chat.ContentTypeThinking:
				cb = chat.NewThinkingBlock(bs.text)
			default:
				cb = chat.NewTextBlock(bs.text)
			}
			w.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: cb})
			if bs.text != "" && bs.blockType == chat.ContentTypeText {
				w.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: bs.text}})
			}
			w.Write(&chat.ContentBlockStopEvent{Index: 0})
		}
	}

	w.Write(&chat.MessageDeltaEvent{StopReason: resp.reason})
	w.Write(&chat.MessageStopEvent{})
	return nil
}

// ── Tools ──

type echoTool struct{}

func (t *echoTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "echo", Description: "echo tool", InputSchema: map[string]any{"type": "object"}}
}
func (t *echoTool) Name() string { return "echo" }
func (t *echoTool) Execute(turn *agent.Turn, w chat.StreamWriter) error {
	return w.WriteBlock(chat.NewTextBlock("echo output"))
}

// ── Helpers ──

// readUntilDone 读完所有事件，返回遇到的 done 事件（超时则 fail）。
func readUntilDone(t *testing.T, client *agent.Client, timeout time.Duration) *chat.ClientEvent {
	t.Helper()
	deadline := time.After(timeout)
	done := make(chan *chat.ClientEvent, 1)
	go func() {
		for {
			evt := client.ReadEvent()
			if evt == nil {
				return
			}
			if evt.EventType == chat.EventTypeDone {
				done <- evt
				return
			}
		}
	}()
	select {
	case evt := <-done:
		return evt
	case <-deadline:
		t.Fatal("timeout waiting for done event")
		return nil
	}
}

// collectEvents 收集所有事件直到队列为空。
func collectEvents(t *testing.T, client *agent.Client) []*chat.ClientEvent {
	t.Helper()
	var events []*chat.ClientEvent
	deadline := time.After(3 * time.Second)
	for {
		var evt *chat.ClientEvent
		select {
		case <-deadline:
			t.Fatal("timeout collecting events")
			return events
		default:
			evt = client.ReadEvent()
		}
		if evt == nil {
			return events
		}
		events = append(events, evt)
		if evt.EventType == chat.EventTypeDone {
			return events
		}
	}
}

// assertEventType 验证事件列表中包含指定类型的事件。
func assertEventType(t *testing.T, events []*chat.ClientEvent, wantType string) {
	t.Helper()
	for _, e := range events {
		if e.EventType == wantType {
			return
		}
	}
	t.Errorf("expected event type %q not found in %d events", wantType, len(events))
}

// ── Tests ──

func TestSingleRoundText(t *testing.T) {
	manager := agent.NewManager()
	manager.RegisterChat("fake", &singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "Hello, world!",
	}, true)

	client, err := manager.GetClient("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.SendText("hi")

	events := collectEvents(t, client)
	assertEventType(t, events, chat.EventTypeChunk)
	assertEventType(t, events, chat.EventTypeDone)
}

func TestToolUseWithRegisteredTool(t *testing.T) {
	manager := agent.NewManager()
	manager.AddTools(&echoTool{})

	// 第一次返回 tool_use，第二次返回 end_turn
	manager.RegisterChat("fake", &orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.ContentTypeToolUse, toolID: "tu_1", toolName: "echo"}}, reason: chat.StopReasonToolUse},
			{text: "tool result processed", reason: chat.StopReasonEndTurn},
		},
	}, true)

	client, err := manager.GetClient("s2", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.SendText("use echo tool")

	events := collectEvents(t, client)
	assertEventType(t, events, chat.EventTypeToolExecution)
	assertEventType(t, events, chat.EventTypeChunk)
	assertEventType(t, events, chat.EventTypeDone)
}

func TestToolUse_UnknownTool(t *testing.T) {
	manager := agent.NewManager()
	manager.AddTools(&echoTool{}) // 只注册 echo，不注册 other_tool

	// LLM 请求 unknown_tool → 自动补错误 tool_result（不触发 tool_execution 事件）
	// → 第二轮 LLM → done
	manager.RegisterChat("fake", &orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.ContentTypeToolUse, toolID: "tu_1", toolName: "unknown_tool"}}, reason: chat.StopReasonToolUse},
			{text: "unknown tool handled", reason: chat.StopReasonEndTurn},
		},
	}, true)

	client, err := manager.GetClient("s3", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.SendText("use unknown tool")

	events := collectEvents(t, client)
	// unknown tool 不会产生 tool_execution 事件（executeTools 中直接生成错误 result）
	assertEventType(t, events, chat.EventTypeDone)
}

func TestMultipleRounds(t *testing.T) {
	manager := agent.NewManager()
	manager.RegisterChat("fake", &singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "response",
	}, true)

	client, err := manager.GetClient("s4", 0)
	if err != nil {
		t.Fatal(err)
	}

	// 第一轮
	client.SendText("round 1")
	readUntilDone(t, client, 10*time.Second)

	// 第二论
	client.SendText("round 2")
	evt := readUntilDone(t, client, 10*time.Second)
	if evt == nil {
		t.Fatal("expected done event in round 2")
	}
}

func TestStopGeneration(t *testing.T) {
	manager := agent.NewManager()
	manager.RegisterChat("fake", &singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "response after stop",
	}, true)

	client, err := manager.GetClient("s5", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.SendText("hello")

	// 等待 doLoop 启动后再 stop
	time.Sleep(50 * time.Millisecond)
	client.Stop()

	// stop 后可发送新消息，验证系统仍可正常工作
	client.SendText("after stop")
	evt := readUntilDone(t, client, 10*time.Second)
	if evt == nil {
		t.Fatal("expected done event after restart")
	}
}

func TestTwoClientsSameSession(t *testing.T) {
	manager := agent.NewManager()
	manager.RegisterChat("fake", &singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "shared response",
	}, true)

	client1, err := manager.GetClient("s6", 0)
	if err != nil {
		t.Fatal(err)
	}
	client2, err := manager.GetClient("s6", 0)
	if err != nil {
		t.Fatal(err)
	}

	// client1 发消息，两个 client 都应该能读到事件
	client1.SendText("hello")

	// 两个 client 都应该能读到 done
	evt1 := readUntilDone(t, client1, 10*time.Second)
	if evt1 == nil {
		t.Fatal("client1: expected done")
	}
	evt2 := readUntilDone(t, client2, 10*time.Second)
	if evt2 == nil {
		t.Fatal("client2: expected done")
	}

	client1.Close()
	// client2 仍可独立存在
	client2.Close()
}

func TestMaxTokensStopReason(t *testing.T) {
	manager := agent.NewManager()
	manager.RegisterChat("fake", &singleResponseProvider{
		stopReason: chat.StopReasonMaxTokens,
		text:       "partial response...",
	}, true)

	client, err := manager.GetClient("s7", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.SendText("hi")

	events := collectEvents(t, client)
	assertEventType(t, events, chat.EventTypeChunk)
	assertEventType(t, events, chat.EventTypeDone)
}
