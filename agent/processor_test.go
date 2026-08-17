package agent_test

import (
	"context"
	"encoding/json"
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

func (f *singleResponseProvider) ChatWithStream(_ context.Context, _ *chat.Request, w *chat.BlockStream) error {
	w.Write(&chat.Start{})
	if f.toolUse != nil {
		w.Write(&chat.ToolUseBlockStart{Id: f.toolUse.ID, Name: f.toolUse.Name})
		if f.toolUse.Input != nil {
			inputJSON, _ := json.Marshal(f.toolUse.Input)
			w.Write(&chat.Delta{Content: string(inputJSON)})
		}
		w.StopReason(chat.StopReasonToolUse)
	} else {
		w.Write(&chat.TextBlockStart{})
		w.Write(&chat.Delta{Content: f.text})
		w.StopReason(f.stopReason)
	}
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
	blockType chat.BlockType
	text      string
	toolID    string
	toolName  string
}

func (f *orderedProvider) ChatWithStream(_ context.Context, _ *chat.Request, w *chat.BlockStream) error {
	i := int(f.idx.Add(1)) - 1
	if i >= len(f.responses) {
		// 超出预设响应，返回单文本
		w.Write(&chat.TextBlockStart{})
		w.Write(&chat.Delta{Content: "fallback"})
		w.StopReason(chat.StopReasonEndTurn)
		return nil
	}

	resp := f.responses[i]

	// 如果设置了 text，使用快捷方式
	if resp.text != "" {
		w.Write(&chat.TextBlockStart{})
		w.Write(&chat.Delta{Content: resp.text})
	} else {
		for _, bs := range resp.blocks {
			switch bs.blockType {
			case chat.BlockTypeToolUse:
				// 模拟真实 LLM：start 只带 id/name，入参经 Delta 流式下发
				w.Write(&chat.ToolUseBlockStart{Id: bs.toolID, Name: bs.toolName})
				w.Write(&chat.Delta{Content: `{"command":"echo hi"}`})
			case chat.BlockTypeThinking:
				w.Write(&chat.ThinkingBlockStart{})
				if bs.text != "" {
					w.Write(&chat.Delta{Content: bs.text})
				}
			default:
				w.Write(&chat.TextBlockStart{})
				if bs.text != "" {
					w.Write(&chat.Delta{Content: bs.text})
				}
			}
		}
	}

	w.StopReason(resp.reason)
	return nil
}

// ── Tools ──

type echoTool struct{}

func (t *echoTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "echo", Description: "echo tool", InputSchema: map[string]any{"type": "object"}}
}
func (t *echoTool) Name() string { return "echo" }
func (t *echoTool) UsagePrompt() string { return "" }
func (t *echoTool) Execute(turn *agent.Turn, w *chat.BlockStream) {
	w.WriteBlock(chat.NewTextBlock("echo output"))
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
	manager := agent.NewAgent()
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
	manager := agent.NewAgent()
	manager.AddTools(&echoTool{})

	// 第一次返回 tool_use，第二次返回 end_turn
	manager.RegisterChat("fake", &orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.BlockTypeToolUse, toolID: "tu_1", toolName: "echo"}}, reason: chat.StopReasonToolUse},
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
	manager := agent.NewAgent()
	manager.AddTools(&echoTool{}) // 只注册 echo，不注册 other_tool

	// LLM 请求 unknown_tool → 自动补错误 tool_result（不触发 tool_execution 事件）
	// → 第二轮 LLM → done
	manager.RegisterChat("fake", &orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.BlockTypeToolUse, toolID: "tu_1", toolName: "unknown_tool"}}, reason: chat.StopReasonToolUse},
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
	manager := agent.NewAgent()
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
	manager := agent.NewAgent()
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

// blockingProvider 首次调用阻塞到 ctx 被取消（模拟长耗时生成），后续调用立即返回文本。
type blockingProvider struct {
	calls   atomic.Int32
	entered chan struct{}
}

func (p *blockingProvider) ChatWithStream(ctx context.Context, _ *chat.Request, w *chat.BlockStream) error {
	if p.calls.Add(1) == 1 {
		close(p.entered) // 通知首轮生成已开始
		<-ctx.Done()
		return ctx.Err()
	}
	w.Write(&chat.TextBlockStart{})
	w.Write(&chat.Delta{Content: "正常回复"})
	w.StopReason(chat.StopReasonEndTurn)
	return nil
}

// TestStopOnlyAffectsCurrentRound 验证单轮停止语义：
// Stop 中止正在生成的当前轮（以 done 结束而非 error 事件），
// 后续新消息照常触发新一轮（停止只对单轮生效）。
func TestStopOnlyAffectsCurrentRound(t *testing.T) {
	manager := agent.NewAgent()
	provider := &blockingProvider{entered: make(chan struct{})}
	manager.RegisterChat("fake", provider, true)

	client, err := manager.GetClient("stop-round", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SendText("开始长耗时生成"); err != nil {
		t.Fatal(err)
	}

	// 等首轮生成确实开始后再停止
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("等待生成开始超时")
	}
	client.Stop()

	// 被停轮应以 done 结束（而非 error 事件）
	if evt := readUntilDone(t, client, 10*time.Second); evt == nil {
		t.Fatal("expected done event for the stopped round")
	}

	// 后续新消息正常触发新一轮
	if err := client.SendText("下一条消息"); err != nil {
		t.Fatal(err)
	}
	if evt := readUntilDone(t, client, 10*time.Second); evt == nil {
		t.Fatal("expected done event after stop")
	}
	if provider.calls.Load() != 2 {
		t.Errorf("expected 2 provider calls, got %d", provider.calls.Load())
	}
}

func TestTwoClientsSameSession(t *testing.T) {
	manager := agent.NewAgent()
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
	manager := agent.NewAgent()
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
