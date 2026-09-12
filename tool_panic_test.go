package agent_test

import (
	"context"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// panicRoundProvider 第 1 次调用让模型请求 panickingTool，第 2 次返回普通文本结束本轮。
type panicRoundProvider struct{ calls int }

func (p *panicRoundProvider) ID() string { return "panic-fake" }
func (p *panicRoundProvider) ChatWithStream(_ context.Context, _ *chat.Messages, w *chat.BlockStream) error {
	p.calls++
	if p.calls == 1 {
		w.BlockToolUseStart("tu_panic", "panicking_tool")
		w.BlockDelta(`{}`)
		w.StopReason(chat.StopReasonToolUse)
		return nil
	}
	w.BlockTextStart()
	w.BlockDelta("已收到工具异常")
	w.StopReason(chat.StopReasonEndTurn)
	return nil
}

// panickingTool 执行即 panic，用于验证工具异常不会让 tool_use 悬空落盘。
type panickingTool struct{}

func (t *panickingTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "panicking_tool", Description: "panic", InputSchema: map[string]any{"type": "object"}}
}
func (t *panickingTool) Name() string        { return "panicking_tool" }
func (t *panickingTool) UsagePrompt() string { return "" }
func (t *panickingTool) Execute(_ *agent.Turn, _ *chat.ToolResultBlockStream) {
	panic("boom")
}

// recordingStore 记录所有落盘消息，用于断言历史配对情况。
type recordingStore struct{ appended []*chat.Message }

func (s *recordingStore) Append(_ string, messages []*chat.Message) error {
	s.appended = append(s.appended, messages...)
	return nil
}
func (s *recordingStore) LoadAfter(string, uint64, int) ([]*chat.Message, error) { return nil, nil }
func (s *recordingStore) LoadSummary(string) (*chat.Message, error)              { return nil, nil }
func (s *recordingStore) SaveSummary(string, *chat.Message) error                { return nil }

// TestToolPanicKeepsToolUsePaired 验证工具 panic 后该轮仍产出配对的 tool_result。
// 落盘的 assistant tool_use 若没有配对的 tool_result，下次加载时 Anthropic 会直接报错。
func TestToolPanicKeepsToolUsePaired(t *testing.T) {
	rec := &recordingStore{}
	config := agent.NewConfig()
	config.AddTools(&panickingTool{})
	config.RegisterChat(&panicRoundProvider{})
	config.MessageStore(rec)

	manager := config.CreateServer(context.Background())
	session := manager.GetOrCreateSession("panic-session")
	client := session.Client(context.Background(), 0)

	session.WriteText("调用 panicking_tool")
	waitForDone(t, client, "panic-tool-round")

	// 补一次落盘，确保本轮消息全部写出
	session.Destroy()

	var hasToolUse, hasToolResult bool
	for _, m := range rec.appended {
		for _, b := range m.Content {
			switch blk := b.(type) {
			case *chat.ToolUseBlock:
				if blk.ID == "tu_panic" {
					hasToolUse = true
				}
			case *chat.ToolResultBlock:
				if blk.ToolUseID == "tu_panic" {
					hasToolResult = true
				}
			}
		}
	}
	if !hasToolUse {
		t.Fatalf("assistant 的 tool_use 未落盘，测试前提不成立（落盘 %d 条消息）", len(rec.appended))
	}
	if !hasToolResult {
		t.Fatalf("tool_use tu_panic 已落盘却无配对的 tool_result，回放会悬空")
	}
}
