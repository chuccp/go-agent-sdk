package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// ── Execute（非阻塞）──

func TestExecute_NilContext(t *testing.T) {
	tool := NewAskUserQuestionTool()
	err := tool.Execute(agent.NewTurn(map[string]any{}), &capturingWriter{})
	if err == nil {
		t.Error("expected error when SessionContext is not injected")
	}
}

func TestExecute_InvalidQuestions(t *testing.T) {
	tool := NewAskUserQuestionTool()
	manager := agent.NewAgent()
	ctx := manager.SessionContext("ask-s1")

	err := tool.Execute(agent.NewTurnWithContext(ctx, map[string]any{}), &capturingWriter{})
	if err == nil {
		t.Error("expected error for missing questions")
	}
}

// TestExecute_NonBlocking 验证 Execute 推送 ask_user 事件后立即返回：
// 事件内容为问题列表 JSON，且 tool_result 文本提示 LLM 等待用户回答。
func TestExecute_NonBlocking(t *testing.T) {
	tool := NewAskUserQuestionTool()
	manager := agent.NewAgent()
	ctx := manager.SessionContext("ask-s2")
	client, err := manager.GetClient("ask-s2", 0)
	if err != nil {
		t.Fatal(err)
	}
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

	w := &capturingWriter{}
	done := make(chan error, 1)
	go func() {
		done <- tool.Execute(agent.NewTurnWithContext(ctx, args), w)
	}()

	// 不依赖任何用户回答，Execute 必须立即返回
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute blocked: expected immediate return")
	}

	// tool_result 文本提示 LLM 等待用户回答
	if !strings.Contains(w.text.String(), "问题已发送给用户") {
		t.Errorf("expected tool_result guidance text, got %q", w.text.String())
	}

	// 前端收到 ask_user 事件，content 为问题列表 JSON
	evt := client.ReadEvent()
	if evt == nil {
		t.Fatal("expected ask_user event")
	}
	if evt.EventType != chat.EventTypeAskUser {
		t.Fatalf("expected ask_user event, got %q", evt.EventType)
	}
	var questions []Question
	if err := json.Unmarshal([]byte(evt.Content), &questions); err != nil {
		t.Fatalf("event content is not question list JSON: %v", err)
	}
	if len(questions) != 1 || questions[0].Question != "What color?" {
		t.Errorf("unexpected questions in event: %+v", questions)
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
