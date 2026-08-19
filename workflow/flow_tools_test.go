package workflow

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

func storyWorkflow() *exec.Workflow {
	return exec.NewBuilder("story003", "故事生成").
		Steps(
			exec.Talk("confirm", "确认受众", "确认受众").DoneWhen("audience"),
			exec.Exec("story", node.NewChatNodeBuilder("story").
				UserTemplate("主题：{{topic}}，受众：{{audience}}").Build()),
			exec.Talk("deliver", "交付", "呈现故事"),
		).
		Build()
}

func TestActivateIdempotentMerge(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()

	st, fresh := store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空"}))
	if !fresh {
		t.Fatal("首次激活应为 fresh")
	}
	// confirm 的 DoneWhen(audience) 未满足
	if st.Status["confirm"] == exec.StepCompleted {
		t.Fatal("audience 未登记，confirm 不应完成")
	}

	// 幂等更新：合并 audience → confirm 自动完成（DoneWhen 声明式判定）
	st2, fresh2 := store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"audience": "儿童"}))
	if fresh2 {
		t.Fatal("同 flow 再次激活应为幂等更新")
	}
	if st2.Input.GetString("topic") != "太空" || st2.Input.GetString("audience") != "儿童" {
		t.Fatalf("input 合并错误: %v", st2.Input)
	}
	if st2.Status["confirm"] != exec.StepCompleted {
		t.Fatal("audience 登记后 confirm 应自动完成")
	}
}

func TestCheckDepsAndProgress(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空", "audience": "儿童"}))

	st := store.Get("s1")
	// confirm 已 done（DoneWhen），story 前置满足
	if err := checkDeps(st, "story"); err != nil {
		t.Fatalf("story 依赖应满足: %v", err)
	}
	// deliver 是 Talk，未完成时 next 应指向它
	progress := store.CardProgress(st)
	if progress.Next == "" {
		t.Fatal("应有建议下一步")
	}

	store.MarkStepDone("s1", "story")
	ok, missing := store.AllDone(st)
	if ok {
		t.Fatal("deliver 未完成，不应全部完成")
	}
	if len(missing) != 1 || missing[0] != "交付" {
		t.Fatalf("missing = %v", missing)
	}
}

func TestRerunInvalidatesDownstream(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空", "audience": "儿童"}))
	store.SetOutput("s1", "story", "初稿")
	store.MarkStepDone("s1", "story")

	// 重跑 story（输出变化）→ 下游 deliver 状态保持（Talk 无输出），输出清空验证在 exec 链上
	store.SetOutput("s1", "story", "修改稿")
	st := store.Get("s1")
	if got := st.Outputs.GetString("story"); got != "修改稿" {
		t.Fatalf("输出未覆盖: %v", got)
	}
	if st.Reruns["story"] != 1 {
		t.Fatalf("重跑计数 = %d", st.Reruns["story"])
	}

	// 输出不变则不计重跑
	store.SetOutput("s1", "story", "修改稿")
	if st.Reruns["story"] != 1 {
		t.Fatalf("相同输出不应计重跑: %d", st.Reruns["story"])
	}
}

func TestIterSourceResolve(t *testing.T) {
	vars := value.NewObjectFromMap(map[string]any{
		"paragraphs": []any{"a", "b"},
		"split":      `[{"title":"一"},{"title":"二"}]`, // 节点产出常为 JSON 文本
		"bad":        "不是数组",
	})
	if arr, err := resolveIterSource(vars, "paragraphs"); err != nil || len(arr) != 2 {
		t.Fatalf("paragraphs: %v %v", arr, err)
	}
	if arr, err := resolveIterSource(vars, "split"); err != nil || len(arr) != 2 {
		t.Fatalf("split JSON 文本应可解析: %v %v", arr, err)
	}
	if _, err := resolveIterSource(vars, "bad"); err == nil {
		t.Fatal("bad 应报错")
	}
	if _, err := resolveIterSource(vars, "missing"); err == nil {
		t.Fatal("missing 应报错")
	}
}
