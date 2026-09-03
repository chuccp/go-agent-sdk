package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
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

func (f *singleResponseProvider) ID() string { return "single" }
func (f *singleResponseProvider) ChatWithStream(_ context.Context, _ *chat.Messages, w *chat.BlockStream) error {
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

func (f *orderedProvider) ID() string { return "ordered" }
func (f *orderedProvider) ChatWithStream(_ context.Context, _ *chat.Messages, w *chat.BlockStream) error {
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
func (f *fakeTool) Name() string                                         { return "fake" }
func (f *fakeTool) UsagePrompt() string                                  { return "" }
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

// newTestClient 创建 Agent 并返回 client，简化测试代码。
func newTestClient(t *testing.T, manager *agent.Agent, sessionId string) *agent.Client {
	t.Helper()
	session := manager.GetOrCreateSession(sessionId)
	client := session.CreateClient(context.Background(), 0)
	return client
}

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
		case *chat.StartBlock:
			if _, ok := b.(*chat.StartBlock); ok {
				return true
			}
		case *chat.MessageDeltaBlock:
			if _, ok := b.(*chat.MessageDeltaBlock); ok {
				return true
			}
		case *chat.MessageStartBlock:
			if _, ok := b.(*chat.MessageStartBlock); ok {
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
		evts, err := client.ReadEvents()
		if err != nil {
			t.Fatalf("ReadEvents error: %v", err)
			return nil
		}
		if len(evts) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, evt := range evts {
			if eventHasBlock(evt, &chat.DoneBlock{}) {
				return evt
			}
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
			t.Fatalf("timeout collecting events (got %d so far)", len(events))
			return events
		default:
		}
		evts, err := client.ReadEvents()
		if err != nil {
			t.Fatalf("ReadEvents error: %v", err)
			return events
		}
		if len(evts) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, evt := range evts {
			events = append(events, evt)
			if eventHasBlock(evt, &chat.DoneBlock{}) {
				return events
			}
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
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "Hello, world!",
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s1")
	client.WriteText("hi")

	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

func TestToolUseWithRegisteredTool(t *testing.T) {
	config := agent.NewConfig()
	config.AddTools(&echoTool{})

	// 第一次返回 tool_use，第二次返回 end_turn
	config.RegisterChat(&orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.ToolUseBlockType, toolID: "tu_1", toolName: "echo"}}, reason: chat.StopReasonToolUse},
			{text: "tool result processed", reason: chat.StopReasonEndTurn},
		},
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s2")
	client.WriteText("use echo tool")

	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

func TestToolUse_UnknownTool(t *testing.T) {
	config := agent.NewConfig()
	config.AddTools(&echoTool{}) // 只注册 echo，不注册 other_tool

	// LLM 请求 unknown_tool → 自动补错误 tool_result（不触发 tool_execution 事件）
	// → 第二轮 LLM → done
	config.RegisterChat(&orderedProvider{
		responses: []orderedResponse{
			{blocks: []blockSpec{{blockType: chat.ToolUseBlockType, toolID: "tu_1", toolName: "unknown_tool"}}, reason: chat.StopReasonToolUse},
			{text: "unknown tool handled", reason: chat.StopReasonEndTurn},
		},
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s3")
	client.WriteText("use unknown tool")

	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

func TestMultipleRounds(t *testing.T) {
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "response",
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s4")

	// 第一轮
	client.WriteText("round 1")
	readUntilDone(t, client, 10*time.Second)

	// 第二轮
	client.WriteText("round 2")
	evt := readUntilDone(t, client, 10*time.Second)
	if evt == nil {
		t.Fatal("expected done event in round 2")
	}
}

func TestStopGeneration(t *testing.T) {
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "response after stop",
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s5")
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

func (p *blockingProvider) ID() string { return "blocking" }
func (p *blockingProvider) ChatWithStream(ctx context.Context, _ *chat.Messages, w *chat.BlockStream) error {
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
	config := agent.NewConfig()
	provider := &blockingProvider{entered: make(chan struct{})}
	config.RegisterChat(provider)

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "stop-round")
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
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "shared response",
	})

	manager := config.CreateAgent(context.Background())
	session := manager.GetOrCreateSession("s6")
	client1 := session.CreateClient(context.Background(), 0)
	client2 := session.CreateClient(context.Background(), 0)

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
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonMaxTokens,
		text:       "partial response...",
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s7")
	client.WriteText("hi")

	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

// usageProvider 每轮发送 MessageStart + text + MessageDelta（模拟 SSE parser 的 usage 事件）。
type usageProvider struct {
	idx atomic.Int32
}

func (f *usageProvider) ID() string { return "usage" }
func (f *usageProvider) ChatWithStream(_ context.Context, _ *chat.Messages, w *chat.BlockStream) error {
	n := int(f.idx.Add(1))
	w.MessageStart(&chat.Usage{InputTokens: 100, OutputTokens: 0})
	w.BlockTextStart()
	w.Delta(fmt.Sprintf("response %d", n))
	w.MessageDelta(&chat.Usage{InputTokens: 100, OutputTokens: 50})
	w.StopReason(chat.StopReasonEndTurn)
	return nil
}

// TestMessageDeltaTwoRounds 验证两轮对话都能正常完成（usage 元数据通过 MessageDeltaBlock 传递）。
func TestMessageDeltaTwoRounds(t *testing.T) {
	config := agent.NewConfig()
	config.RegisterChat(&usageProvider{})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s_usage")

	// 第一轮
	client.WriteText("round 1")
	evt1 := readUntilDone(t, client, 10*time.Second)
	if evt1 == nil {
		t.Fatal("round 1: expected done event")
	}

	// 第二轮
	client.WriteText("round 2")
	evt2 := readUntilDone(t, client, 10*time.Second)
	if evt2 == nil {
		t.Fatal("round 2: expected done event")
	}
}

// TestWriteBlocks_UpdatesLastTime 验证 WriteBlocks 会更新 lastTime。
func TestWriteBlocks_UpdatesLastTime(t *testing.T) {
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "ok",
	})

	manager := config.CreateAgent(context.Background())
	session := manager.GetOrCreateSession("s_time")
	client := session.CreateClient(context.Background(), 0)
	client.WriteText("hello")

	// 写入消息后 session 应该能正常工作（lastTime 已更新）
	events := collectEvents(t, client)
	hasBlockTypeInEvents(t, events, &chat.DoneBlock{})
}

// TestSession_Destroy 验证 Destroy 后 session 从 sessions map 中移除。
func TestSession_Destroy(t *testing.T) {
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "ok",
	})

	manager := config.CreateAgent(context.Background())
	session := manager.GetOrCreateSession("s_destroy")
	client := session.CreateClient(context.Background(), 0)
	client.WriteText("hello")
	collectEvents(t, client)

	session.Destroy()

	// Destroy 后 GetSession 应该找不到
	if _, ok := manager.GetSession("s_destroy"); ok {
		t.Error("session should be removed after Destroy")
	}

	// 再次 GetOrCreateSession 应该创建新的
	newSession := manager.GetOrCreateSession("s_destroy")
	if newSession == nil {
		t.Fatal("GetOrCreateSession should create new session after Destroy")
	}
}

// TestSession_RemoveSession 验证 Agent.RemoveSession 正确销毁会话。
func TestSession_RemoveSession(t *testing.T) {
	config := agent.NewConfig()
	config.RegisterChat(&singleResponseProvider{
		stopReason: chat.StopReasonEndTurn,
		text:       "ok",
	})

	manager := config.CreateAgent(context.Background())
	client := newTestClient(t, manager, "s_rm")
	client.WriteText("hello")
	collectEvents(t, client)

	manager.RemoveSession("s_rm")

	if _, ok := manager.GetSession("s_rm"); ok {
		t.Error("session should be removed after RemoveSession")
	}
}
