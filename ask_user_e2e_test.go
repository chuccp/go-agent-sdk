package agent_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/tools"
)

// askUserProvider 模拟 LLM 两轮响应：
// 第 1 次 → tool_use(ask_user_question)；第 2 次 → 文本 end_turn（收到用户回答后的最终回复）。
// 提问轮不再有 LLM 收尾调用：ask_user_question 置 user_wait 后 doLoop 直接结束本轮。
type askUserProvider struct {
	calls atomic.Int32
	// lastRequest 记录最近一次请求，用于验证用户回答作为普通消息进入历史
	lastReq atomic.Pointer[chat.Messages]
}

func (f *askUserProvider) ID() string { return "ask" }
func (f *askUserProvider) ChatWithStream(_ context.Context, req *chat.Messages, w *chat.BlockStream) error {
	f.lastReq.Store(req)
	n := f.calls.Add(1)
	switch n {
	case 1:
		w.BlockToolUseStart("tu_ask", "ask_user_question")
		// 入参按真实协议以 Delta 流式送达
		w.Delta(`{"questions":[{"question":"What color?","header":"Color","options":[` +
			`{"label":"Red","description":"Red color"},{"label":"Blue","description":"Blue color"}]}]}`)
		w.StopReason(chat.StopReasonToolUse)
	default:
		w.BlockTextStart()
		w.Delta("已收到用户回答")
		w.StopReason(chat.StopReasonEndTurn)
	}
	return nil
}

// findAskUserBlock 在事件列表中查找 ask_user 的 CustomTextBlock（可能嵌套在 ToolResultBlock.Content 内）。
func findAskUserBlock(events []*agent.Event) *chat.CustomTextBlock {
	for _, e := range events {
		if ab := findInBlocks(e.Blocks); ab != nil {
			return ab
		}
	}
	return nil
}

func findInBlocks(blocks chat.Blocks) *chat.CustomTextBlock {
	for _, b := range blocks {
		if cb, ok := b.(*chat.CustomTextBlock); ok && cb.TextType == chat.AskUserTextType {
			return cb
		}
		if trb, ok := b.(*chat.ToolResultBlock); ok {
			if ab := findInBlocks(trb.Content); ab != nil {
				return ab
			}
		}
	}
	return nil
}

// TestAskUserQuestion_E2E_NonBlocking 全链路验证非阻塞问答：
// 用户提问 → LLM 调 ask_user_question → ask_user block 推前端 → 工具不阻塞，
// 本轮正常走到 done → 用户回答作为普通消息触发新一轮 → done。
func TestAskUserQuestion_E2E_NonBlocking(t *testing.T) {
	config := agent.NewConfig()
	config.AddTools(tools.NewAskUserQuestionTool())
	provider := &askUserProvider{}
	config.RegisterChat(provider)

	manager := config.CreateAgent(context.Background())
	client := manager.GetOrCreateSession("ask-e2e").CreateClient(context.Background(), 0)
	defer client.Close()

	// ── 第一轮：触发 ask_user_question ──
	client.WriteText("帮我选个颜色")
	events := collectUntilDone(t, client)

	// ask_user block 已推送，text 为问题列表 JSON
	askBlock := findAskUserBlock(events)
	if askBlock == nil {
		t.Fatal("未收到 ask_user CustomTextBlock")
	}
	var questions []tools.Question
	if err := json.Unmarshal([]byte(askBlock.Text), &questions); err != nil {
		t.Fatalf("ask_user block text 不是问题列表 JSON: %v", err)
	}
	if len(questions) != 1 || questions[0].Question != "What color?" {
		t.Fatalf("问题内容不符: %+v", questions)
	}

	// 工具输出已流式推送（TextBlock），不再单独发 ToolExecutionBlock

	// ── 第二轮：用户回答作为普通消息 ──
	client.WriteText("Red")
	events2 := collectUntilDone(t, client)
	// TextBlock 可能作为顶层事件到达（StartBlock 被 dedup），
	// 也可能嵌套在 StartBlock 中（取决于时序），两种情况都算通过。
	hasText := false
	for _, e := range events2 {
		if _, ok := e.Blocks[0].(*chat.TextBlock); ok {
			hasText = true
			break
		}
		if sb, ok := e.Blocks[0].(*chat.StartBlock); ok {
			if _, ok := sb.Block.(*chat.TextBlock); ok {
				hasText = true
				break
			}
		}
	}
	if !hasText {
		t.Error("回答轮未收到 text block")
	}

	// 用户回答以普通 user 消息进入请求历史
	req := provider.lastReq.Load()
	if req == nil {
		t.Fatal("未记录到第二轮请求")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != chat.RoleUser {
		t.Fatalf("期望最后一条为 user 消息，实际 %s", last.Role)
	}
	found := false
	for _, b := range last.Content {
		if tb, ok := b.(*chat.TextBlock); ok && tb.Text == "Red" {
			found = true
		}
	}
	if !found {
		t.Errorf("回答轮请求中未包含用户回答 'Red'，最后一条消息: %+v", last.Content)
	}

	// LLM 共被调用 2 次：tool_use 轮（提问）+ 回答轮；提问轮不再有 LLM 收尾调用
	if got := provider.calls.Load(); got != 2 {
		t.Errorf("期望 LLM 调用 2 次，实际 %d 次", got)
	}
}
