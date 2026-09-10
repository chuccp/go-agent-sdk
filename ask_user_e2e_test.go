package agent_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
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
		w.BlockDelta(`{"questions":[{"question":"What color?","header":"Color","options":[` +
			`{"label":"Red","description":"Red color"},{"label":"Blue","description":"Blue color"}]}]}`)
		w.StopReason(chat.StopReasonToolUse)
	default:
		w.BlockTextStart()
		w.BlockDelta("已收到用户回答")
		w.StopReason(chat.StopReasonEndTurn)
	}
	return nil
}

// askQuestion 对齐 ask_user_question 入参中单个问题的结构（前端也从入参自行解析）。
type askQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
}

// askUserToolUseArgs 从事件流中重建 ask_user_question 的入参 JSON。
// 入参不随 StartBlock 抵达——StartBlock 只带 ID/Name，JSON 由其后的 DeltaBlock
// 分片送达（前端同样按分片累加后解析），这里按 Start 升序还原。
func askUserToolUseArgs(events []*agent.Event) string {
	ordered := make([]*agent.Event, len(events))
	copy(ordered, events)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })

	var sb strings.Builder
	active := false
	for _, e := range ordered {
		for _, b := range e.Blocks {
			switch v := b.(type) {
			case *chat.StartBlock:
				tub, ok := v.Block.(*chat.ToolUseBlock)
				active = ok && tub.Name == "ask_user_question"
			case *chat.ToolUseBlock:
				active = v.Name == "ask_user_question"
			case *chat.DeltaBlock:
				if active {
					sb.WriteString(v.Content)
				}
			}
		}
	}
	return sb.String()
}

// hasAskUserCustomText 报告事件流中是否出现 ask_user 的 CustomTextBlock。
func hasAskUserCustomText(events []*agent.Event) bool {
	var walk func(blocks chat.Blocks) bool
	walk = func(blocks chat.Blocks) bool {
		for _, b := range blocks {
			if cb, ok := b.(*chat.CustomTextBlock); ok && cb.TextType == chat.AskUserTextType {
				return true
			}
			if trb, ok := b.(*chat.ToolResultBlock); ok && walk(trb.Content) {
				return true
			}
		}
		return false
	}
	for _, e := range events {
		if walk(e.Blocks) {
			return true
		}
	}
	return false
}

// TestAskUserQuestion_E2E_NonBlocking 全链路验证非阻塞问答：
// 用户提问 → LLM 调 ask_user_question → 问题随 tool_use 入参抵达前端 → 工具不阻塞，
// 本轮正常走到 done → 用户回答作为普通消息触发新一轮 → done。
//
// 工具不再单独推送 ask_user CustomTextBlock：问题 JSON 已在 tool_use 入参中随消息流
// 到达前端，重复推送会产生第二份副本。
func TestAskUserQuestion_E2E_NonBlocking(t *testing.T) {
	config := agent.NewConfig()
	config.AddTools(tools.NewAskUserQuestionTool())
	provider := &askUserProvider{}
	config.RegisterChat(provider)

	manager := config.CreateServer(context.Background())
	session := manager.GetOrCreateSession("ask-e2e")
	client := session.Client(context.Background(), 0)
	defer client.Close()

	// ── 第一轮：触发 ask_user_question ──
	session.WriteText("帮我选个颜色")
	events := collectUntilDone(t, client)

	// 问题内容随 tool_use 入参抵达前端
	args := askUserToolUseArgs(events)
	if args == "" {
		t.Fatal("未收到 ask_user_question 的 tool_use 入参分片")
	}
	var payload struct {
		Questions []askQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		t.Fatalf("tool_use 入参不是问题列表 JSON: %v (入参=%q)", err, args)
	}
	if len(payload.Questions) != 1 || payload.Questions[0].Question != "What color?" {
		t.Fatalf("问题内容不符: %+v", payload.Questions)
	}

	// 工具不再重复推送 ask_user block
	if hasAskUserCustomText(events) {
		t.Error("ask_user CustomTextBlock 不应再由工具推送（问题已随 tool_use 入参到达）")
	}

	// 工具输出已流式推送（TextBlock），不再单独发 ToolExecutionBlock

	// ── 第二轮：用户回答作为普通消息 ──
	session.WriteText("Red")
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
