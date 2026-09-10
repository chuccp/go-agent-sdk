package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// ── Execute（非阻塞）──

// TestExecute_NonBlocking 验证 Execute 立即返回：tool_result 文本陈述已提问等待回答，
// 停止原因置 user_wait，且不再向前端重复推送问题内容——问题 JSON 只存在于 LLM 的
// tool_use 入参中，由前端自行解析渲染。
func TestExecute_NonBlocking(t *testing.T) {
	tool := NewAskUserQuestionTool()
	config := agent.NewConfig()
	manager := config.CreateServer(context.Background())
	ctx := manager.SessionContext("ask-s2")
	client := manager.GetOrCreateSession("ask-s2").Client(context.Background(), 0)
	defer client.Close()

	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "What color?",
				"header":   "Color",
				"options": []any{
					map[string]any{"label": "Red", "description": "Red color"},
					map[string]any{"label": "Blue", "description": "Blue color"},
				},
			},
		},
	}

	w := chat.NewBlockStream(&ctxReceiver{ctx: ctx})
	done := make(chan struct{}, 1)
	go func() {
		tool.Execute(agent.NewTurnWithContext(ctx, value.NewObjectFromMap(args)), chat.NewToolResultBlockStream(w, "ask"))
		done <- struct{}{}
	}()

	// 不依赖任何用户回答，Execute 必须立即返回
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Execute blocked: expected immediate return")
	}

	// tool_result 文本陈述已提问、等待用户回答（作为历史上下文）
	if text := drainText(w); !strings.Contains(text, "已向用户提出问题") {
		t.Errorf("expected tool_result context text, got %q", text)
	}

	// 工具置 user_wait：会话主循环据此跳过 LLM 收尾调用、直接结束本轮
	if got := w.GetStopReason(); got != chat.StopReasonUserWait {
		t.Errorf("expected StopReasonUserWait, got %q", got)
	}

	// 问题内容不由本工具推送：LLM 的入参已在 tool_use 块中随消息流到达前端，
	// 前端从该入参解析并渲染问题卡片，此处不应再出现 ask_user CustomTextBlock
	for _, ev := range readEventsUntilIdle(client, 300*time.Millisecond) {
		for _, b := range ev.Blocks {
			if cb, ok := b.(*chat.CustomTextBlock); ok && cb.TextType == chat.AskUserTextType {
				t.Errorf("ask_user CustomTextBlock 不应再由工具推送: %s", cb.Text)
			}
		}
	}
}

// ── Tool interface compliance ──

func TestAskUserQuestion_Name(t *testing.T) {
	tool := NewAskUserQuestionTool()
	if tool.Name() != "ask_user_question" {
		t.Errorf("expected ask_user_question, got %s", tool.Name())
	}
}

func TestAskUserQuestion_Definition(t *testing.T) {
	tool := NewAskUserQuestionTool()
	def := tool.Definition()
	if def.Name != "ask_user_question" {
		t.Errorf("expected ask_user_question, got %s", def.Name)
	}
	if _, ok := def.InputSchema["properties"].(map[string]any)["questions"]; !ok {
		t.Error("expected questions in input schema")
	}
}

func TestAskUserQuestion_ImplementsToolExecutor(t *testing.T) {
	var _ agent.ToolExecutor = NewAskUserQuestionTool()
}

// ── CustomTextBlock(ask_user) 序列化 ──

// TestAskUserBlock_RoundTrip 验证 ask_user 自定义文本块无损往返，且往返后仍是
// CustomTextBlock（不再是降级为纯文本块）。后端已不再生产该块，此用例保留用于
// 覆盖旧历史数据中已持久化的 ask_user 块的解析路径。
func TestAskUserBlock_RoundTrip(t *testing.T) {
	orig := chat.Blocks{chat.NewCustomTextBlock(`[{"question":"What color?"}]`, chat.AskUserTextType)}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw[0]["type"] != "custom_text" || raw[0]["text_type"] != "ask_user" {
		t.Fatalf("serialized type/text_type mismatch: %v", raw[0])
	}

	var got chat.Blocks
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	b, ok := got[0].(*chat.CustomTextBlock)
	if !ok {
		t.Fatalf("round-trip type = %T, want *chat.CustomTextBlock", got[0])
	}
	if b.Text != `[{"question":"What color?"}]` || b.TextType != chat.AskUserTextType {
		t.Errorf("round-trip mismatch: %+v", b)
	}
	if b.ForContext() {
		t.Error("CustomTextBlock.ForContext() should remain false after round-trip")
	}
}
