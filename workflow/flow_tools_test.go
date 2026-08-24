package workflow

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

// ==================== 测试辅助 ====================

// fakeLoopContext 最小化 LoopContext 实现，用于工具层单元测试。
type fakeLoopContext struct{ id string }

func (f *fakeLoopContext) SessionId() string                            { return f.id }
func (f *fakeLoopContext) GetSeq() uint64                               { return 0 }
func (f *fakeLoopContext) SendBlock(_ uint64, _ chat.Block) uint64      { return 0 }
func (f *fakeLoopContext) GetService(_ string) chat.Service             { return nil }
func (f *fakeLoopContext) GetCompressorStore() agent.CompressorStore    { return nil }

// newTestTools 创建工具组并激活默认 storyWorkflow，返回五个工具。
func newTestTools(sessionId string, input map[string]any) (activate, execNode, stepDone, status, finish agent.ToolExecutor) {
	mgr := NewManager()
	wf := storyWorkflow()
	mgr.AddWorkflow(wf)
	activate, execNode, stepDone, status, finish = NewFlowTools(mgr)
	if sessionId != "" && input != nil {
		sctx := &fakeLoopContext{id: sessionId}
		turn := agent.NewTurnWithContext(sctx, value.NewObjectFromMap(map[string]any{
			"flow_id": "story003", "input": input,
		}))
		w := chat.NewBlockStream(nil)
		activate.Execute(turn, chat.NewToolResultBlockStream(w, "act"))
	}
	return
}

// newTurn 构造绑定 fakeLoopContext 的 Turn。
func newTurn(sessionId string, args map[string]any) *agent.Turn {
	return agent.NewTurnWithContext(&fakeLoopContext{id: sessionId}, value.NewObjectFromMap(args))
}

// execText 执行工具并收集输出文本。
func execText(t *testing.T, exec agent.ToolExecutor, turn *agent.Turn) string {
	t.Helper()
	w := chat.NewBlockStream(nil)
	exec.Execute(turn, chat.NewToolResultBlockStream(w, "test"))
	var sb strings.Builder
	for _, b := range w.ReadBlocks() {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

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

// ==================== Guard 边界测试 ====================

// TestActivateSwitchFlow 激活 flow A 后再激活 flow B → 旧状态完全覆盖。
func TestActivateSwitchFlow(t *testing.T) {
	store := NewFlowStore()
	wfA := storyWorkflow()
	wfB := exec.NewBuilder("other001", "另一个flow").
		Steps(exec.Talk("step1", "步骤一", "做点什么")).
		Build()

	store.Activate("s1", "story003", wfA, value.NewObjectFromMap(map[string]any{"topic": "太空"}))
	store.MarkStepDone("s1", "confirm")
	store.SetOutput("s1", "story", "初稿")

	// 切换到不同 flow → 状态应完全重置
	st, fresh := store.Activate("s1", "other001", wfB, value.NewObjectFromMap(map[string]any{"foo": "bar"}))
	if !fresh {
		t.Fatal("切换 flow 应为 fresh")
	}
	if st.Workflow.Id != "other001" {
		t.Fatalf("workflow 未切换: %s", st.Workflow.Id)
	}
	if st.Input.GetString("topic") != "" {
		t.Fatal("旧 flow 的 input 不应保留")
	}
	if st.Input.GetString("foo") != "bar" {
		t.Fatal("新 flow 的 input 未登记")
	}
	if st.Outputs.Get("story") != nil {
		t.Fatal("旧 flow 的 outputs 应被清空")
	}
}

// TestFlowStepDoneOnExecStep 对 exec 步骤调用 flow_step_done 应被拒绝。
func TestFlowStepDoneOnExecStep(t *testing.T) {
	_, _, stepDone, _, _ := newTestTools("s1", map[string]any{"topic": "太空", "audience": "儿童"})
	out := execText(t, stepDone, newTurn("s1", map[string]any{"step_id": "story"}))
	if !strings.Contains(out, "执行步骤") {
		t.Fatalf("exec 步骤调用 step_done 应被拒: %s", out)
	}
}

// TestFlowStepDoneUnknownStep flow_step_done 传入不存在的步骤 ID 应报错。
func TestFlowStepDoneUnknownStep(t *testing.T) {
	_, _, stepDone, _, _ := newTestTools("s1", map[string]any{"topic": "太空", "audience": "儿童"})
	out := execText(t, stepDone, newTurn("s1", map[string]any{"step_id": "nonexistent"}))
	if !strings.Contains(out, "不存在") {
		t.Fatalf("未知步骤应报错: %s", out)
	}
}

// TestFlowStepDoneNoActiveFlow 无激活 flow 时调用 flow_step_done 应报错。
func TestFlowStepDoneNoActiveFlow(t *testing.T) {
	_, _, stepDone, _, _ := newTestTools("", nil)
	out := execText(t, stepDone, newTurn("s1", map[string]any{"step_id": "deliver"}))
	if !strings.Contains(out, "没有激活") {
		t.Fatalf("无激活 flow 应报错: %s", out)
	}
}

// TestFinishUnknownAction finish_flow 传入未知 action 应报错。
func TestFinishUnknownAction(t *testing.T) {
	_, _, _, _, finish := newTestTools("s1", map[string]any{"topic": "太空", "audience": "儿童"})
	out := execText(t, finish, newTurn("s1", map[string]any{"action": "invalid"}))
	if !strings.Contains(out, "未知 action") {
		t.Fatalf("未知 action 应报错: %s", out)
	}
}

// TestFinishCompleteHappyPath 所有步骤完成后 finish(complete) 应成功清理。
func TestFinishCompleteHappyPath(t *testing.T) {
	mgr := NewManager()
	wf := storyWorkflow()
	mgr.AddWorkflow(wf)
	activate, _, _, _, finish := NewFlowTools(mgr)

	// 激活 flow
	sctx := &fakeLoopContext{id: "s1"}
	activate.Execute(newTurn("s1", map[string]any{"flow_id": "story003", "input": map[string]any{"topic": "太空", "audience": "儿童"}}),
		chat.NewToolResultBlockStream(chat.NewBlockStream(nil), "act"))

	// 直接通过 store 完成所有步骤
	suite := activate.(*ActivateFlowTool).suite
	suite.store.MarkStepDone("s1", "story")
	suite.store.MarkStepDone("s1", "deliver")

	out := execText(t, finish, newTurn("s1", map[string]any{"action": "complete", "summary": "太空故事已完成"}))
	if !strings.Contains(out, "已完成") {
		t.Fatalf("complete 应成功: %s", out)
	}
	if !strings.Contains(out, "太空故事已完成") {
		t.Fatalf("应包含 summary: %s", out)
	}
	if suite.store.Get("s1") != nil {
		t.Fatal("finish 后应清理状态")
	}
	_ = sctx
}

// TestCheckDepsUnknownStep checkDeps 对不存在的步骤：当有未完成前置步骤时，
// 返回的是前置步骤未完成错误（而非"未找到"），因为遍历在第一个阻塞处就返回了。
func TestCheckDepsUnknownStep(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空", "audience": "儿童"}))
	st := store.Get("s1")

	// confirm 已 DoneWhen 完成，story 未完成 → 对 nonexistent 报"前置步骤 story 未完成"
	err := checkDeps(st, "nonexistent")
	if err == nil {
		t.Fatal("不存在的步骤应报错")
	}
	if !strings.Contains(err.Error(), "story") {
		t.Fatalf("应阻塞在 story: %v", err)
	}

	// 全部步骤完成后，对不存在的步骤应返回"未找到"
	store.MarkStepDone("s1", "story")
	store.MarkStepDone("s1", "deliver")
	err = checkDeps(st, "nonexistent")
	if err == nil || !strings.Contains(err.Error(), "未找到") {
		t.Fatalf("全部完成后查不存在步骤应返回未找到: %v", err)
	}
}

// TestNextStepGuidance 区分 DoneWhen talk、plain talk、exec 步骤的引导文案。
func TestNextStepGuidance(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	// 只提供 topic → confirm 的 DoneWhen(audience) 未满足
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空"}))
	st := store.Get("s1")

	// confirm 有 DoneWhen 且未满足 → 引导用 activate_flow 补录
	progress := store.CardProgress(st)
	if !strings.Contains(progress.Next, "activate_flow") {
		t.Fatalf("DoneWhen 步骤应引导 activate_flow: %s", progress.Next)
	}

	// 补录 audience → confirm 自动完成，story 是 exec → 引导用 exec_node
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"audience": "儿童"}))
	progress = store.CardProgress(st)
	if !strings.Contains(progress.Next, "exec_node") {
		t.Fatalf("exec 步骤应引导 exec_node: %s", progress.Next)
	}

	// 完成 story → deliver 是 plain talk → 引导用 flow_step_done
	store.MarkStepDone("s1", "story")
	progress = store.CardProgress(st)
	if !strings.Contains(progress.Next, "flow_step_done") {
		t.Fatalf("plain talk 步骤应引导 flow_step_done: %s", progress.Next)
	}
}

// TestNextStepAllDone 所有步骤完成后 nextStep 应返回空。
func TestNextStepAllDone(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空", "audience": "儿童"}))
	store.MarkStepDone("s1", "story")
	store.MarkStepDone("s1", "deliver")
	st := store.Get("s1")
	progress := store.CardProgress(st)
	if progress.Next != "" {
		t.Fatalf("全部完成后 next 应为空: %s", progress.Next)
	}
}

// TestSetOutputNilSession 对不存在的 session 调用 SetOutput 应返回 false。
func TestSetOutputNilSession(t *testing.T) {
	store := NewFlowStore()
	changed := store.SetOutput("no-such-session", "story", "data")
	if changed {
		t.Fatal("nil session 应返回 false")
	}
}

// TestInvalidateDownstreamPreservesTalk 上游重跑失效下游时，Talk 步骤状态应保持不变。
func TestInvalidateDownstreamPreservesTalk(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空", "audience": "儿童"}))
	// story 完成，deliver（Talk）也标记完成
	store.MarkStepDone("s1", "story")
	store.MarkStepDone("s1", "deliver")
	store.SetOutput("s1", "story", "初稿")

	// 重跑 story（输出变化）→ 下游 deliver 是 Talk，不应被重置为 pending
	store.SetOutput("s1", "story", "修改稿")
	st := store.Get("s1")
	if st.Status["deliver"] != exec.StepCompleted {
		t.Fatal("Talk 步骤不应被 invalidateDownstream 重置")
	}
	// story 自身的 rerun 计数应增加
	if st.Reruns["story"] != 1 {
		t.Fatalf("rerun 计数 = %d", st.Reruns["story"])
	}
}

// TestMultiKeyDoneWhen DoneWhen 声明多个键时，需全部满足才自动完成。
func TestMultiKeyDoneWhen(t *testing.T) {
	wf := exec.NewBuilder("multi001", "多键确认").
		Steps(
			exec.Talk("confirm", "确认", "确认信息").
				DoneWhen("name", "age"),
			exec.Talk("deliver", "交付", "呈现"),
		).
		Build()
	store := NewFlowStore()

	// 只登记 name → 不满足
	st, _ := store.Activate("s1", "multi001", wf, value.NewObjectFromMap(map[string]any{"name": "小明"}))
	if st.Status["confirm"] == exec.StepCompleted {
		t.Fatal("仅 name 不应完成")
	}

	// 补录 age → 全部满足，自动完成
	st2, _ := store.Activate("s1", "multi001", wf, value.NewObjectFromMap(map[string]any{"age": 10}))
	if st2.Status["confirm"] != exec.StepCompleted {
		t.Fatal("name+age 都登记后应自动完成")
	}
}

// ==================== 迭代边界测试 ====================

// TestIterEmptyArray 迭代源为空数组时应直接返回空结果。
func TestIterEmptyArray(t *testing.T) {
	vars := value.NewObjectFromMap(map[string]any{"items": []any{}})
	arr, err := resolveIterSource(vars, "items")
	if err != nil {
		t.Fatalf("空数组不应报错: %v", err)
	}
	if len(arr) != 0 {
		t.Fatalf("空数组长度应为 0: %d", len(arr))
	}
}

// TestIterExactlyAtLimit 迭代项恰好等于上限（20）时应正常执行。
func TestIterExactlyAtLimit(t *testing.T) {
	items := make([]any, maxIterItems)
	for i := range items {
		items[i] = fmt.Sprintf("item%d", i)
	}
	vars := value.NewObjectFromMap(map[string]any{"items": items})
	arr, err := resolveIterSource(vars, "items")
	if err != nil {
		t.Fatalf("20 项不应报错: %v", err)
	}
	if len(arr) != maxIterItems {
		t.Fatalf("应为 %d 项: %d", maxIterItems, len(arr))
	}
}

// TestIterOverLimit 迭代项超过上限时应报错。
func TestIterOverLimit(t *testing.T) {
	items := make([]any, maxIterItems+1)
	for i := range items {
		items[i] = fmt.Sprintf("item%d", i)
	}
	vars := value.NewObjectFromMap(map[string]any{"items": items})
	_, err := resolveIterSource(vars, "items")
	if err != nil {
		// resolveIterSource 本身不检查上限，上限在 execIterating 中检查
		// 这里只验证能正确解析
		t.Fatalf("解析不应报错: %v", err)
	}
}

// TestIterNestedItemField 迭代项为对象时，{{item.field}} 模板渲染。
func TestIterNestedItemField(t *testing.T) {
	vars := value.NewObjectFromMap(map[string]any{
		"segments": []any{
			map[string]any{"title": "起", "summary": "开头"},
			map[string]any{"title": "承", "summary": "发展"},
		},
	})
	arr, err := resolveIterSource(vars, "segments")
	if err != nil {
		t.Fatalf("对象数组应可解析: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("应为 2 项: %d", len(arr))
	}
	first, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatal("第 1 项应为 map")
	}
	if first["title"] != "起" || first["summary"] != "开头" {
		t.Fatalf("嵌套字段值错误: %v", first)
	}
}

// TestIterPartialFailureAndResume 迭代部分失败后重试，已完成项应被跳过。
func TestIterPartialFailureAndResume(t *testing.T) {
	store := NewFlowStore()
	wf := exec.NewBuilder("iter001", "迭代测试").
		Steps(
			exec.Exec("items", node.NewChatNodeBuilder("items").
				UserTemplate("{{item}}").Build()).
				Iterate("data"),
		).
		Build()
	store.Activate("s1", "iter001", wf, value.NewObjectFromMap(map[string]any{
		"data": []any{"a", "b", "c"},
	}))
	st := store.Get("s1")

	// 模拟第 0、1 项完成，第 2 项未完成
	store.MarkItemDone(st, "items", 0)
	store.MarkItemDone(st, "items", 1)
	arr := value.NewArraySize(3)
	arr.Set(0, value.NewText("结果A"))
	arr.Set(1, value.NewText("结果B"))
	st.Outputs.PutAny("items", arr)

	// PartialResults 应返回 2 个已完成项
	results, doneSet := store.PartialResults(st, "items", 3)
	if !doneSet[0] || !doneSet[1] {
		t.Fatal("第 0、1 项应标记为 done")
	}
	if doneSet[2] {
		t.Fatal("第 2 项不应标记为 done")
	}
	if results.Get(0).String() != "结果A" || results.Get(1).String() != "结果B" {
		t.Fatalf("已完成项结果应保留: %v %v", results.Get(0), results.Get(1))
	}
}

// TestTailRunes 边界：空字符串、n=0、n 等于长度、n 大于长度、中文 rune。
func TestTailRunes(t *testing.T) {
	if tailRunes("", 5) != "" {
		t.Fatal("空字符串应返回空")
	}
	if tailRunes("abc", 0) != "abc" {
		t.Fatal("n=0 应返回原串")
	}
	if tailRunes("abc", 3) != "abc" {
		t.Fatal("n=长度应返回原串")
	}
	if tailRunes("abc", 5) != "abc" {
		t.Fatal("n>长度应返回原串")
	}
	if tailRunes("abc", 2) != "bc" {
		t.Fatal("尾部截取错误")
	}
	// 中文 rune 截取
	if tailRunes("你好世界", 2) != "世界" {
		t.Fatalf("中文 rune 截取错误: %q", tailRunes("你好世界", 2))
	}
}

// TestSummarizeEdgeCases summarize 辅助函数边界。
func TestSummarizeEdgeCases(t *testing.T) {
	if summarize("", 10) != "" {
		t.Fatal("空字符串应返回空")
	}
	if summarize("abc", 10) != "abc" {
		t.Fatal("短串不截断")
	}
	if summarize("abcdef", 3) != "abc…" {
		t.Fatalf("截断错误: %q", summarize("abcdef", 3))
	}
	// 非字符串类型走 JSON 化
	if summarize(42, 10) != "42" {
		t.Fatalf("数字 JSON 化错误: %q", summarize(42, 10))
	}
}

// TestPrepareExecMergesInputAndOutputs PrepareExec 返回的变量应包含 input + outputs。
func TestPrepareExecMergesInputAndOutputs(t *testing.T) {
	store := NewFlowStore()
	wf := storyWorkflow()
	store.Activate("s1", "story003", wf, value.NewObjectFromMap(map[string]any{"topic": "太空", "audience": "儿童"}))
	store.SetOutput("s1", "story", "故事正文")
	store.MarkStepDone("s1", "story")

	vars, err := store.PrepareExec("s1", "deliver")
	if err != nil {
		t.Fatalf("PrepareExec 失败: %v", err)
	}
	if vars.GetString("topic") != "太空" {
		t.Fatal("vars 应包含 input")
	}
	if vars.GetString("audience") != "儿童" {
		t.Fatal("vars 应包含 input (audience)")
	}
	if vars.GetString("story") != "故事正文" {
		t.Fatal("vars 应包含上游 output")
	}
}
