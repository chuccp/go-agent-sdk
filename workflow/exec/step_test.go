package exec

import (
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/workflow/node"
)

func buildTestWorkflow() *Workflow {
	storyNode := node.NewChatNodeBuilder("story").
		SystemTemplate("你是一位故事创作者").
		UserTemplate("主题：{{topic}}，受众：{{audience}}，写一个 800 字的故事").
		Build()
	return NewBuilder("story003", "故事生成").
		Description("根据主题生成定制故事").
		Steps(
			Talk("confirm", "确认受众", "确认受众并登记").DoneWhen("audience"),
			Exec("story", storyNode),
			Talk("deliver", "交付", "呈现故事"),
		).
		Build()
}

func TestBuilderSteps(t *testing.T) {
	w := buildTestWorkflow()
	if len(w.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(w.Steps))
	}
	if w.Steps[0].Kind() != StepTalk || w.Steps[1].Kind() != StepExec {
		t.Fatalf("step kinds wrong: %v, %v", w.Steps[0].Kind(), w.Steps[1].Kind())
	}
	if got := w.Steps[0].DoneWhenKeys(); len(got) != 1 || got[0] != "audience" {
		t.Fatalf("doneWhen = %v", got)
	}
	// UserTemplate bug 修复验证
	nd := w.Steps[1].Node()
	if nd.UserTemplate() == "" || nd.SystemTemplate() != "你是一位故事创作者" {
		t.Fatalf("templates wrong: system=%q user=%q", nd.SystemTemplate(), nd.UserTemplate())
	}
}

func TestRenderCard(t *testing.T) {
	w := buildTestWorkflow()
	progress := &CardProgress{
		Status: map[string]StepStatus{"confirm": StepCompleted},
		Next:   "exec_node(\"story\")",
	}
	card := w.RenderCard(progress)
	for _, want := range []string{"【执行守则】", "【当前 Flow】故事生成（story003）", "【步骤】", "✓ 确认受众", "○ 交付", "exec_node(\"story\")"} {
		if !strings.Contains(card, want) {
			t.Errorf("card missing %q\n%s", want, card)
		}
	}
	footer := w.ProgressFooter(progress)
	if !strings.Contains(footer, "confirm✓") || !strings.Contains(footer, "下一步: exec_node(\"story\")") {
		t.Errorf("footer wrong: %s", footer)
	}
}

func TestRenderTemplate(t *testing.T) {
	vars := map[string]any{
		"topic":    "太空",
		"audience": "儿童",
		"item":     map[string]any{"title": "第一章", "summary": "起航"},
		"split":    []any{"a", "b"},
	}
	got := RenderTemplate("主题：{{topic}}，受众：{{audience}}", vars)
	if got != "主题：太空，受众：儿童" {
		t.Errorf("render = %q", got)
	}
	got = RenderTemplate("段：{{item.title}}-{{item.summary}}，共{{split.1}}", vars)
	if got != "段：第一章-起航，共b" {
		t.Errorf("nested render = %q", got)
	}
	// 未命中原样保留
	got = RenderTemplate("缺：{{missing}}", vars)
	if got != "缺：{{missing}}" {
		t.Errorf("missing render = %q", got)
	}
}
