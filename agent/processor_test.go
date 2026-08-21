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
	if f.toolUse != nil {
		w.BlockToolUseStart(f.toolUse.ID, f.toolUse.Name)
		if f.toolUse.Input != nil {
			inputJSON, _ := json.Marshal(f.toolUse.Input)
			w.Delta(string(inputJSON))
		}
		w.StopReason(chat.StopReasonToolUse)
	} else {
		w.BlockTextStart()
		w.Delta(f.text)
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
		w.BlockTextStart()
		w.Delta("fallback")
		w.StopReason(chat.StopReasonEndTurn)
		return nil
	}

	resp := f.responses[i]

	// 如果设置了 text，使用快捷方式
	if resp.text != "" {
		w.BlockTextStart()
		w.Delta(resp.text)
	} else {
		for _, bs := range resp.blocks {
			switch bs.blockType {
			case chat.ToolUseBlockType:
				// 模拟真实 LLM：start 只带 id/name，入参经 Delta 流式下发
				w.BlockToolUseStart(bs.toolID, bs.toolName)
				w.Delta(`{"command":"echo hi"}`)
			case chat.ThinkingBlockType:
				w.BlockThinkingStart()
				if bs.text != "" {
					w.Delta(bs.text)
				}
			default:
				w.BlockTextStart()
				if bs.text != "" {
					w.Delta(bs.text)
				}
			}
		}
	}

	w.StopReason(resp.reason)
	return nil
}

// ── Tools ──

// fakeTool 用于 TestToolUse_UnknownTool 等场景（agent 包内 fakeTool 不可导出）。
type fakeTool struct{}

func (f *fakeTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "fake", Description: "a fake tool"}
}
func (f *fakeTool) Name() string                         { return "fake" }
func (f *fakeTool) UsagePrompt() string                  { return "" }
func (f *fakeTool) Execute(_ *agent.Turn, _ *chat.ToolResultBlockStream) {}

type echoTool struct{}

func (t *echoTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "echo", Description: "echo tool", InputSchema: map[string]any{"type": "object"}}
}
func (t *echoTool) Name() string        { return "echo" }
func (t *echoTool) UsagePrompt() string { return "" }
func (t *echoTool) Execute(turn *agent.Turn, w *chat.ToolResultBlockStream) {
	w.FullText("echo output")
}

// ── Helpers ──

// eventHasBlock 检查事件的 Blocks 中是否包含指定类型的 block。
func eventHasBlock(evt *agent.Event, target chat.Block) bool {
	for _, b := range evt.Blocks {
		switch target.(type) {
		case *chat.DoneBlock:
			if _, ok := b.(*chat.DoneBlock); ok {
				return true
			}
		case *chat.TextBlock:
			if _, ok := b.(*chat.TextBlock); ok {
				return true
			}
		case *chat.ToolExecutionBlock:
			if _, ok := b.(*chat.ToolExecutionBlock); ok {
				return true
			}
		case *chat.StartBlock:
			if _, ok := b.(*chat.StartBlock); ok {
				return true
			}
		}
	}
	return false
}

// readUntilDone 持续轮询事件，返回遇到的 done 事件（超时则 fail）。
func readUntilDone(t *testing.T, client *agent.Client, timeout time.Duration) *agent.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for done event")
			return nil
		default:
		}
		evt := client.ReadEvent()
		if evt == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if eventHasBlock(evt, &chat.DoneBlock{}) {
			return evt
		}
	}
}

// collectEvents 持续轮询收集事件，直到遇到 DoneBlock 或超时。
func collectEvents(t *testing.T, client *agent.Client) []*agent.Event {
	t.Helper()
	var events []*agent.Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout collecting events")
			return events
		default:
		}
		evt := client.ReadEvent()
		if evt == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		events = append(events, evt)
		if eventHasBlock(evt, &chat.DoneBlock{}) {
			return events
		}
	}
}

// hasBlockTypeInEvents 验证事件列表中包含指定 block 类型的事件。
func hasBlockTypeInEvents(t *testing.T, events []*agent.Event, block chat.Block) {
	t.Helper()
	for _, e := range events {
		if eventHasBlock(e, block) {
			return
		}
	}
	t.Errorf("expected block type %T not found in %d events", block, len(events))
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
	client.WriteText("hi")

	events := collectEvents(t, client)
	t.Logf("got %d events", len(events))
	for i, e := range events {
		t.Logf("  event[%d]: Start=%d Offset=%d Blocks=%d", i, e.Start, e.Offset, len(e.Blocks))
		for j, b := range e.Blocks {
			t.Logf("    block[%d]: %T", j, b)
		}
	}
	hasBlockTypeInEvents(t, events, &chat.StartBlock{})
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

func TestToolUseWithRegisteredTool(t *testing.T) {
	manager := agent.NewAgent()
	manager.AddTools(&echoTool{})

	// 第一次返回 tool_use，第二次返回 end_turn
	manager.RegisterChat("fake", &orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.ToolUseBlockType, toolID: "tu_1", toolName: "echo"}}, reason: chat.StopReasonToolUse},
			{text: "tool result processed", reason: chat.StopReasonEndTurn},
		},
	}, true)

	client, err := manager.GetClient("s2", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.WriteText("use echo tool")

	events := collectEvents(t, client)
	// 工具输出已通过 TextBlock 流式推送，不再单独发 ToolExecutionBlock（避免重复）
	hasBlockTypeInEvents(t, events, &chat.TextBlock{})
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

func TestToolUse_UnknownTool(t *testing.T) {
	manager := agent.NewAgent()
	manager.AddTools(&echoTool{}) // 只注册 echo，不注册 other_tool

	// LLM 请求 unknown_tool → 自动补错误 tool_result（不触发 tool_execution 事件）
	// → 第二轮 LLM → done
	manager.RegisterChat("fake", &orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.ToolUseBlockType, toolID: "tu_1", toolName: "unknown_tool"}}, reason: chat.StopReasonToolUse},
			{text: "unknown tool handled", reason: chat.StopReasonEndTurn},
		},
	}, true)

	client, err := manager.GetClient("s3", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.WriteText("use unknown tool")

	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
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
	client.WriteText("round 1")
	readUntilDone(t, client, 10*time.Second)

	// 第二论
	client.WriteText("round 2")
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
	client.WriteText("hello")

	// 等待 doLoop 启动后再 stop
	time.Sleep(50 * time.Millisecond)
	client.Stop()

	// stop 后可发送新消息，验证系统仍可正常工作
	client.WriteText("after stop")
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
	w.BlockTextStart()
	w.Delta("正常回复")
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
	client.WriteText("开始长耗时生成")

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
	client.WriteText("下一条消息")
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
	client1.WriteText("hello")

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
	client.WriteText("hi")

	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.StartBlock{})
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}
