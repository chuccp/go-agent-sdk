package agent

import (
	"sync"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// ── buildRequest helpers ──

type fakeProvider struct {
	calls int
}

func (f *fakeProvider) ChatWithStream(_ any, _ *chat.Request, _ any) error { return nil }

// newTestSessionContext 创建一个构造好基础组件的 SessionContext，可以直接测 buildRequest。
func newTestSessionContext(opts ...chat.Option) *SessionContext {
	o := chat.DefaultOptions()
	o.Model = "test-model"
	o.MaxTokens = 100
	for _, opt := range opts {
		opt(o)
	}
	return &SessionContext{
		sessionId:    "test-session",
		inbox:        new(util.SliceQueue[*QueuedMessage]),
		events:       chat.NewStore("test-session", nil),
		registry:     chat.NewProviderRegistry(),
		chatClients:  new(util.SliceArray[*ChatClient]),
		opts:         o,
		clientMutex:  new(sync.Mutex),
		runLock:      sync.Mutex{},
	}
}

// enqueue 向 inbox 写入一条用户消息。
func enqueue(ctx *SessionContext, text string) {
	ctx.inbox.Write(&QueuedMessage{
		ctx:  ctx,
		id:   ctx.getSeq(),
		msg:  &chat.RevMessage{Text: text},
		opts: nil,
	})
}

// seedHistory 写入一条 user 消息和一个 assistant 响应到历史。
func seedHistory(ctx *SessionContext, userBlocks chat.Blocks, assistantBlocks chat.Blocks) {
	ctx.events.AppendHistory(&chat.Message{Role: chat.RoleUser, Content: userBlocks})
	ctx.events.AppendHistory(&chat.Message{Role: chat.RoleAssistant, Content: assistantBlocks})
}

// ── buildRequest 测试 ──

func TestBuildRequest_EmptyInbox(t *testing.T) {
	ctx := newTestSessionContext()
	// 没有历史也没有 inbox → buildRequest 返回 nil
	req := ctx.buildRequest()
	if req != nil {
		t.Errorf("expected nil request for empty inbox + no history, got %v", req)
	}
}

func TestBuildRequest_WithHistory(t *testing.T) {
	ctx := newTestSessionContext()
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewTextBlock("hi there")},
	)
	enqueue(ctx, "follow up")

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	// seed(2) + enqueue(1 ConsumeMessage 追加) = 3 条历史
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages (2 seed + 1 consumed), got %d", len(req.Messages))
	}
	if req.Model != "test-model" {
		t.Errorf("expected model test-model, got %s", req.Model)
	}
	if req.MaxTokens != 100 {
		t.Errorf("expected MaxTokens=100, got %d", req.MaxTokens)
	}
	if !req.Stream {
		t.Error("expected stream=true by default")
	}
}

func TestBuildRequest_StripsThinkingFromHistory(t *testing.T) {
	ctx := newTestSessionContext()
	// assistant 消息包含 thinking + text block
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewThinkingBlock("hmm..."), chat.NewTextBlock("reply")},
	)
	enqueue(ctx, "next")

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}

	// assistant 消息的 content 应被剥离 thinking
	assistantMsg := req.Messages[1]
	if len(assistantMsg.Content) != 1 {
		t.Fatalf("expected 1 block after stripping thinking, got %d", len(assistantMsg.Content))
	}
	if assistantMsg.Content[0].Type() != chat.ContentTypeText {
		t.Errorf("expected text block, got %s", assistantMsg.Content[0].Type())
	}
}

func TestBuildRequest_AllThinkingSkipped(t *testing.T) {
	ctx := newTestSessionContext()
	// 一条纯 thinking 的 assistant 消息，剥离后 content 为空
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewThinkingBlock("only thinking")},
	)
	enqueue(ctx, "next")

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	// seed user + 纯thinking assistant(被跳过) + consumed user = 2 条
	if len(req.Messages) != 2 {
		t.Errorf("expected 2 messages (thinking-only skipped), got %d", len(req.Messages))
	}
}

func TestBuildRequest_PerTurnOptionMerge(t *testing.T) {
	ctx := newTestSessionContext()
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewTextBlock("hi")},
	)
	// 入队两条消息，第二条携带 per-turn 覆盖
	enqueue(ctx, "first")
	enqueueOpts(ctx, "second", chat.WithModel("per-turn-model"), chat.WithMaxTokens(200))

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	if req.Model != "per-turn-model" {
		t.Errorf("expected per-turn model, got %s", req.Model)
	}
	if req.MaxTokens != 200 {
		t.Errorf("expected per-turn max_tokens=200, got %d", req.MaxTokens)
	}
	// 全局 opts 中未被覆盖的字段应保持
	if !req.Stream {
		t.Error("expected stream to remain true from global opts")
	}
}

func TestBuildRequest_NoPerTurnOpts(t *testing.T) {
	ctx := newTestSessionContext(chat.WithModel("global-model"))
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewTextBlock("hi")},
	)
	enqueue(ctx, "just text")

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	if req.Model != "global-model" {
		t.Errorf("expected global-model, got %s", req.Model)
	}
}

func TestBuildRequest_ToolDefinitions(t *testing.T) {
	ctx := newTestSessionContext()
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewTextBlock("hi")},
	)
	enqueue(ctx, "run tool")

	ctx.toolExecutors = append(ctx.toolExecutors, &fakeTool{})

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Tools))
	}
	if req.Tools[0].Name != "fake" {
		t.Errorf("expected tool name 'fake', got %s", req.Tools[0].Name)
	}
}

func TestBuildRequest_SystemPrompt(t *testing.T) {
	ctx := newTestSessionContext()
	ctx.system = "You are a helpful assistant."
	seedHistory(ctx,
		chat.Blocks{chat.NewTextBlock("hello")},
		chat.Blocks{chat.NewTextBlock("hi")},
	)
	enqueue(ctx, "tell me something")

	req := ctx.buildRequest()
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	if req.System != "You are a helpful assistant." {
		t.Errorf("expected system prompt, got %q", req.System)
	}
}

func TestBuildRequest_ThinkingConfig(t *testing.T) {
	tests := []struct {
		level    chat.ThinkingLevel
		expectOn bool
	}{
		{chat.ThinkingOff, false},   // disabled
		{chat.ThinkingLow, true},
		{chat.ThinkingHigh, true},
		{"", false}, // unset → nil
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			ctx := newTestSessionContext(chat.WithThinking(tt.level))
			seedHistory(ctx,
				chat.Blocks{chat.NewTextBlock("hello")},
				chat.Blocks{chat.NewTextBlock("hi")},
			)
			enqueue(ctx, "hi")

			req := ctx.buildRequest()
			if req == nil {
				t.Fatal("expected non-nil request")
			}
			if tt.expectOn {
				if req.Thinking == nil || req.Thinking.Type != "enabled" {
					t.Errorf("expected enabled thinking, got %+v", req.Thinking)
				}
			} else {
				if tt.level == chat.ThinkingOff {
					if req.Thinking == nil || req.Thinking.Type != "disabled" {
						t.Errorf("expected disabled thinking, got %+v", req.Thinking)
					}
				} else {
					if req.Thinking != nil {
						t.Errorf("expected nil thinking config, got %+v", req.Thinking)
					}
				}
			}
		})
	}
}

// enqueueOpts 向 inbox 写入一条带 per-turn 选项的用户消息。
func enqueueOpts(ctx *SessionContext, text string, opt ...chat.Option) {
	ctx.inbox.Write(&QueuedMessage{
		ctx:  ctx,
		id:   ctx.getSeq(),
		msg:  &chat.RevMessage{Text: text},
		opts: opt,
	})
}

// ── SessionContext 基础方法 ──

func TestSessionContext_ID(t *testing.T) {
	ctx := &SessionContext{sessionId: "s1"}
	if ctx.ID() != "s1" {
		t.Errorf("expected s1, got %s", ctx.ID())
	}
}

func TestSessionContext_Done_NilWhenNotRunning(t *testing.T) {
	ctx := newTestSessionContext()
	// 主循环未启动，Done() 应返回 nil
	done := ctx.Done()
	if done != nil {
		t.Error("expected nil Done channel when not running")
	}
}

// ── drainInbox ──

func TestDrainInbox_ConsumesAllMessages(t *testing.T) {
	ctx := newTestSessionContext()
	enqueue(ctx, "msg1")
	enqueue(ctx, "msg2")

	ctx.drainInbox()
	// drain 后 inbox 应为空
	if !ctx.inbox.IsEmpty() {
		t.Error("expected empty inbox after drain")
	}
	// 两条消息应被追加到 history
	if ctx.events.HistoryLen() != 2 {
		t.Errorf("expected 2 history entries, got %d", ctx.events.HistoryLen())
	}
}

// ── consumeMessage ──

func TestConsumeMessage_AddsToHistory(t *testing.T) {
	ctx := newTestSessionContext()
	qm := &QueuedMessage{
		ctx:  ctx,
		id:   1,
		msg:  &chat.RevMessage{Text: "test message"},
		opts: nil,
	}

	opts := ctx.ConsumeMessage(qm)
	if opts != nil {
		t.Errorf("expected nil opts, got %v", opts)
	}
	if ctx.events.HistoryLen() != 1 {
		t.Errorf("expected 1 history entry, got %d", ctx.events.HistoryLen())
	}
}
