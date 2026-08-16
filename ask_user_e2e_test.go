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

// askUserProvider 模拟 LLM 三轮响应：
// 第 1 次 → tool_use(ask_user_question)；第 2 次 → 文本 end_turn（提问轮结束）；
// 第 3 次 → 文本 end_turn（收到用户回答后的最终回复）。
type askUserProvider struct {
	calls atomic.Int32
	// lastRequest 记录最近一次请求，用于验证用户回答作为普通消息进入历史
	lastReq atomic.Pointer[chat.Request]
}

func (f *askUserProvider) ChatWithStream(_ context.Context, req *chat.Request, w *chat.BlockStream) error {
	f.lastReq.Store(req)
	n := f.calls.Add(1)
	switch n {
	case 1:
		w.Write(&chat.ToolUseBlockStart{Id: "tu_ask", Name: "ask_user_question"})
		// 入参按真实协议以 Delta 流式送达
		w.Write(&chat.Delta{Content: `{"questions":[{"question":"What color?","header":"Color","options":[` +
			`{"label":"Red","description":"Red color"},{"label":"Blue","description":"Blue color"}]}]}`})
		w.StopReason(chat.StopReasonToolUse)
	default:
		text := "等待用户回答"
		if n > 2 {
			text = "已收到用户回答"
		}
		w.Write(&chat.TextBlockStart{})
		w.Write(&chat.Delta{Content: text})
		w.StopReason(chat.StopReasonEndTurn)
	}
	return nil
}

// findEvent 在事件列表中查找指定类型的事件。
func findEvent(events []*chat.ClientEvent, eventType string) *chat.ClientEvent {
	for _, e := range events {
		if e.EventType == eventType {
			return e
		}
	}
	return nil
}

// TestAskUserQuestion_E2E_NonBlocking 全链路验证非阻塞问答：
// 用户提问 → LLM 调 ask_user_question → ask_user 事件推前端 → 工具不阻塞，
// 本轮正常走到 done → 用户回答作为普通消息触发新一轮 → done。
func TestAskUserQuestion_E2E_NonBlocking(t *testing.T) {
	manager := agent.NewAgent()
	manager.AddTools(tools.NewAskUserQuestionTool())
	provider := &askUserProvider{}
	manager.RegisterChat("fake", provider, true)

	client, err := manager.GetClient("ask-e2e", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// ── 第一轮：触发 ask_user_question ──
	if err := client.SendText("帮我选个颜色"); err != nil {
		t.Fatal(err)
	}
	events := collectUntilDone(t, client)

	// ask_user 事件已推送，content 为问题列表 JSON
	askEvt := findEvent(events, chat.EventTypeAskUser)
	if askEvt == nil {
		t.Fatal("未收到 ask_user 事件")
	}
	var questions []tools.Question
	if err := json.Unmarshal([]byte(askEvt.Content), &questions); err != nil {
		t.Fatalf("ask_user content 不是问题列表 JSON: %v", err)
	}
	if len(questions) != 1 || questions[0].Question != "What color?" {
		t.Fatalf("问题内容不符: %+v", questions)
	}

	// tool_execution 事件存在（工具非阻塞执行完成并产出 tool_result）
	if findEvent(events, chat.EventTypeToolExecution) == nil {
		t.Error("未收到 tool_execution 事件")
	}

	// ── 第二轮：用户回答作为普通消息 ──
	if err := client.SendText("Red"); err != nil {
		t.Fatal(err)
	}
	events2 := collectUntilDone(t, client)
	if findEvent(events2, chat.EventTypeChunk) == nil {
		t.Error("回答轮未收到 chunk 事件")
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

	// LLM 共被调用 3 次：tool_use 轮 + 提问收尾轮 + 回答轮
	if got := provider.calls.Load(); got != 3 {
		t.Errorf("期望 LLM 调用 3 次，实际 %d 次", got)
	}
}
