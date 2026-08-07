package tools

import (
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// ── parseQuestions ──

func TestParseQuestions_Valid(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "What color?",
				"header":   "Color",
				"options": []any{
					map[string]any{"label": "Red", "description": "Red color"},
					map[string]any{"label": "Blue", "description": "Blue color"},
				},
				"multi_select": false,
			},
		},
	}

	qs, err := parseQuestions(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	q := qs[0]
	if q.Question != "What color?" || q.Header != "Color" {
		t.Errorf("question/header mismatch: %q / %q", q.Question, q.Header)
	}
	if len(q.Options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(q.Options))
	}
	if q.Options[0].Label != "Red" {
		t.Errorf("expected Red, got %q", q.Options[0].Label)
	}
}

func TestParseQuestions_MaxFourQuestions(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Q1", "header": "H1",
				"options": []any{
					map[string]any{"label": "A", "description": "d"},
					map[string]any{"label": "B", "description": "d"},
				},
			},
			map[string]any{
				"question": "Q2", "header": "H2",
				"options": []any{
					map[string]any{"label": "A", "description": "d"},
					map[string]any{"label": "B", "description": "d"},
				},
			},
			map[string]any{
				"question": "Q3", "header": "H3",
				"options": []any{
					map[string]any{"label": "A", "description": "d"},
					map[string]any{"label": "B", "description": "d"},
				},
			},
			map[string]any{
				"question": "Q4", "header": "H4",
				"options": []any{
					map[string]any{"label": "A", "description": "d"},
					map[string]any{"label": "B", "description": "d"},
				},
			},
		},
	}

	qs, err := parseQuestions(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 4 {
		t.Errorf("expected 4, got %d", len(qs))
	}

	// 5 questions should fail
	bad := map[string]any{"questions": []any{
		map[string]any{"question": "Q1", "header": "H", "options": []any{map[string]any{"label": "A", "description": "d"}, map[string]any{"label": "B", "description": "d"}}},
		map[string]any{"question": "Q2", "header": "H", "options": []any{map[string]any{"label": "A", "description": "d"}, map[string]any{"label": "B", "description": "d"}}},
		map[string]any{"question": "Q3", "header": "H", "options": []any{map[string]any{"label": "A", "description": "d"}, map[string]any{"label": "B", "description": "d"}}},
		map[string]any{"question": "Q4", "header": "H", "options": []any{map[string]any{"label": "A", "description": "d"}, map[string]any{"label": "B", "description": "d"}}},
		map[string]any{"question": "Q5", "header": "H", "options": []any{map[string]any{"label": "A", "description": "d"}, map[string]any{"label": "B", "description": "d"}}},
	}}
	_, err = parseQuestions(bad)
	if err == nil {
		t.Error("expected error for 5 questions")
	}
}

func TestParseQuestions_MissingQuestions(t *testing.T) {
	_, err := parseQuestions(map[string]any{})
	if err == nil {
		t.Error("expected error for missing questions")
	}
}

func TestParseQuestions_EmptyArray(t *testing.T) {
	_, err := parseQuestions(map[string]any{"questions": []any{}})
	if err == nil {
		t.Error("expected error for empty questions")
	}
}

func TestParseQuestions_NotArray(t *testing.T) {
	_, err := parseQuestions(map[string]any{"questions": "not array"})
	if err == nil {
		t.Error("expected error for non-array questions")
	}
}

func TestParseQuestions_MissingOptions(t *testing.T) {
	_, err := parseQuestions(map[string]any{
		"questions": []any{
			map[string]any{"question": "Q1", "header": "H1"},
		},
	})
	if err == nil {
		t.Error("expected error for missing options")
	}
}

func TestParseQuestions_TooFewOptions(t *testing.T) {
	_, err := parseQuestions(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Q1", "header": "H1",
				"options": []any{
					map[string]any{"label": "Only", "description": "d"},
				},
			},
		},
	})
	if err == nil {
		t.Error("expected error for <2 options")
	}
}

func TestParseQuestions_MissingLabel(t *testing.T) {
	_, err := parseQuestions(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Q1", "header": "H1",
				"options": []any{
					map[string]any{"description": "no label"},
					map[string]any{"label": "B", "description": "d"},
				},
			},
		},
	})
	if err == nil {
		t.Error("expected error for missing label")
	}
}

func TestParseQuestions_WithPreview(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which layout?",
				"header":   "Layout",
				"options": []any{
					map[string]any{"label": "Grid", "description": "Grid layout", "preview": "```\n[ ][ ]\n```"},
					map[string]any{"label": "List", "description": "List layout"},
				},
			},
		},
	}

	qs, err := parseQuestions(args)
	if err != nil {
		t.Fatal(err)
	}
	if qs[0].Options[0].Preview != "```\n[ ][ ]\n```" {
		t.Errorf("expected preview, got %q", qs[0].Options[0].Preview)
	}
	if qs[0].Options[1].Preview != "" {
		t.Errorf("expected empty preview, got %q", qs[0].Options[1].Preview)
	}
}

// ── formatResponse ──

func TestFormatResponse(t *testing.T) {
	questions := []Question{
		{Question: "What color?", Header: "Color", Options: []Option{
			{Label: "Red", Description: "Red"},
			{Label: "Blue", Description: "Blue"},
		}},
	}
	answers := map[string]string{"What color?": "Red"}
	response := "I like red"

	result, err := formatResponse(questions, answers, response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Red") {
		t.Errorf("expected Red in response, got %q", result)
	}
	if !strings.Contains(result, "I like red") {
		t.Errorf("expected free response, got %q", result)
	}
}

func TestFormatResponse_NoAnswersOrResponse(t *testing.T) {
	_, err := formatResponse(nil, nil, "")
	if err == nil {
		t.Error("expected error for no answers and no response")
	}
}

// ── HandleRevMessage (MessageFilter) ──

// recordingChain 记录 Next 被调用，用于验证消息未被拦截。
type recordingChain struct {
	called bool
}

func (r *recordingChain) Next() error {
	r.called = true
	return nil
}

func TestHandleRevMessage_NoContext(t *testing.T) {
	tool := NewAskUserQuestionTool().(*AskUserQuestionTool)

	// 消息没有 SessionContext，deliverAnswer 返回 false，应继续链
	qm := &agent.QueuedMessage{}
	chain := &recordingChain{}

	err := tool.HandleRevMessage(chain, qm)
	if err != nil {
		t.Fatal(err)
	}
	if !chain.called {
		t.Error("expected chain.Next() to be called when no context")
	}
}

func TestHandleRevMessage_NoWaitingChannel(t *testing.T) {
	tool := NewAskUserQuestionTool().(*AskUserQuestionTool)

	// 消息带有 context 但没有注册 waiting channel → 透传
	// ctx != nil 但 deliverAnswer 返回 false（无 waiting channel）
	// → chain.Next() 被调用

	// 验证内部状态：无 session 的 waiting channel
	if _, exists := tool.waiting["nosuch"]; exists {
		t.Error("expected no waiting channel for unknown session")
	}
	// TakeConsumedAnswer 在无记录时返回 nil
	if msg := tool.TakeConsumedAnswer("nosuch"); msg != nil {
		t.Error("expected nil for unknown session")
	}
}

// ── deliverAnswer ──

func TestDeliverAnswer_NoWaitingChannel(t *testing.T) {
	tool := NewAskUserQuestionTool().(*AskUserQuestionTool)

	// 没有注册等待通道，deliverAnswer 应返回 false
	result := tool.deliverAnswer("nosuch", &chat.RevMessage{Text: "hello"})
	if result {
		t.Error("expected false when no waiting channel")
	}
}

func TestDeliverAnswer_WithWaitingChannel(t *testing.T) {
	tool := NewAskUserQuestionTool().(*AskUserQuestionTool)

	sid := "test-session"
	ch := make(chan *chat.RevMessage, 1)
	tool.mu.Lock()
	tool.waiting[sid] = ch
	tool.mu.Unlock()

	msg := &chat.RevMessage{Text: "my answer"}
	result := tool.deliverAnswer(sid, msg)
	if !result {
		t.Error("expected true when waiting channel exists")
	}

	// 消息应该被投递到 channel
	received := <-ch
	if received.Text != "my answer" {
		t.Errorf("expected 'my answer', got %q", received.Text)
	}
}

// ── TakeConsumedAnswer ──

func TestTakeConsumedAnswer_RemovesAfterTake(t *testing.T) {
	tool := NewAskUserQuestionTool().(*AskUserQuestionTool)

	sid := "s1"
	tool.mu.Lock()
	tool.consumed[sid] = &chat.RevMessage{Text: "consumed answer"}
	tool.mu.Unlock()

	msg := tool.TakeConsumedAnswer(sid)
	if msg == nil || msg.Text != "consumed answer" {
		t.Fatalf("expected consumed answer, got %v", msg)
	}

	// 第二次获取返回 nil（已清除）
	msg = tool.TakeConsumedAnswer(sid)
	if msg != nil {
		t.Error("expected nil after take")
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

func TestAskUserQuestion_ImplementsMessageFilter(t *testing.T) {
	var _ agent.MessageFilter = NewAskUserQuestionTool().(*AskUserQuestionTool)
}

func TestAskUserQuestion_ImplementsAnswerConsumer(t *testing.T) {
	var _ agent.AnswerConsumer = NewAskUserQuestionTool().(*AskUserQuestionTool)
}
