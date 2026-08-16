package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

// ==================== ExecNodeTool：零上下文执行核 ====================

type ExecNodeTool struct{ suite *FlowToolSuite }

func (t *ExecNodeTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *ExecNodeTool) UsagePrompt() string { return "" }

func (t *ExecNodeTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "exec_node",
		Description: "执行当前激活 flow 中的一个执行步骤（节点零上下文运行，自动取上游产出）。" +
			"执行前校验前置步骤已完成；已完成步骤可再次调用（重跑，输出覆盖并使下游失效）；" +
			"迭代步骤失败后重试只补跑失败项。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"step_id": map[string]any{
					"type":        "string",
					"description": "要执行的步骤 ID（卡片中的执行步骤）",
				},
			},
			"required": []string{"step_id"},
		},
	}
}

// Execute 执行节点：依赖校验 → 组装变量 → 零上下文 LLM 调用（或逐项迭代）→
// 登记输出（重跑使下游失效）→ 标记完成；执行错误直接写入 writer。
func (t *ExecNodeTool) Execute(turn *agent.Turn, writer *agent.BlockStream) {
	args := turn.Args()
	stepId := args.GetString("step_id")
	if stepId == "" {
		writer.WriteBlock(chat.NewTextBlock("缺少 step_id 参数"))
		return
	}
	sctx := turn.Context()
	if sctx == nil {
		writer.WriteBlock(chat.NewTextBlock("exec_node 需要会话上下文"))
		return
	}
	sessionId := sctx.ID()

	st := t.suite.store.Get(sessionId)
	if st == nil {
		writer.WriteBlock(chat.NewTextBlock("当前没有激活的 flow，请先调用 activate_flow"))
		return
	}
	step := findStep(st.Workflow, stepId)
	if step == nil {
		writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("flow %s 中不存在步骤 %s", st.Workflow.Id, stepId)))
		return
	}
	if step.Kind() != exec.StepExec || step.Node() == nil {
		writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("步骤 %q 是对话步骤，请按剧本在对话中完成，不能用 exec_node", step.Title())))
		return
	}

	vars, err := t.suite.store.PrepareExec(sessionId, stepId)
	if err != nil {
		writer.WriteBlock(chat.NewTextBlock(err.Error()))
		return
	}

	t.emitProgress(sctx, st.Workflow.Id, stepId, "start", "")

	var output any
	var summary string
	if step.IsIterating() {
		output, summary, err = t.execIterating(turn, st, step, vars)
	} else {
		output, summary, err = t.execSingle(turn, step, vars)
	}
	if err != nil {
		t.emitProgress(sctx, st.Workflow.Id, stepId, "error", err.Error())
		writer.WriteBlock(chat.NewTextBlock(err.Error()))
		return
	}

	t.suite.store.SetOutput(sessionId, stepId, output)
	t.suite.store.MarkStepDone(sessionId, stepId)
	t.emitProgress(sctx, st.Workflow.Id, stepId, "done", summarize(output, 500))

	// 产出去向：context 模式全文返回主 LLM（交付型产出，摘要无意义）；
	// event 模式全文随事件直达前端，主 LLM 只拿摘要
	var resultText string
	if step.Node().Deliver() == node.DeliverContext {
		if str, ok := output.(string); ok {
			resultText = fmt.Sprintf("步骤「%s」执行完成，产出全文：\n%s", step.Title(), str)
		} else {
			resultText = fmt.Sprintf("步骤「%s」执行完成。%s", step.Title(), summary)
		}
	} else {
		resultText = fmt.Sprintf("步骤「%s」执行完成。%s", step.Title(), summary)
	}
	resultText += t.suite.footer(t.suite.store.Get(sessionId))
	writer.WriteBlock(chat.NewTextBlock(resultText))
}

// execSingle 单次执行：渲染模板 → 零上下文 LLM 调用。
func (t *ExecNodeTool) execSingle(turn *agent.Turn, step *exec.Step, vars *value.Object) (any, string, error) {
	nd := step.Node()
	text, err := t.nodeCall(turn, nd, vars, nil)
	if err != nil {
		return nil, "", err
	}
	return text, "产出摘要: " + summarize(text, 200), nil
}

// execIterating 迭代执行：展开数组 → 逐项零上下文调用（{{item}}/{{index}}/{{prev}}）→
// 聚合。已完成项自动跳过（index 级断点续跑）。
func (t *ExecNodeTool) execIterating(turn *agent.Turn, st *FlowState, step *exec.Step, vars *value.Object) (any, string, error) {
	arr, err := resolveIterSource(vars, step.IterateSource())
	if err != nil {
		return nil, "", err
	}
	if len(arr) > maxIterItems {
		return nil, "", fmt.Errorf("迭代项 %d 超过上限 %d，请拆分后再试", len(arr), maxIterItems)
	}

	sctx := turn.Context()
	results, doneSet := t.suite.store.PartialResults(st, step.Name(), len(arr))
	var failures []string
	for i, raw := range arr {
		if doneSet[i] {
			continue // index 级跳过：只补跑失败/缺失项
		}
		itemVars := map[string]any{"item": raw, "index": i + 1, "total": len(arr)}
		if i > 0 {
			if prev := results.Get(i - 1); prev != nil && prev.IsText() {
				itemVars["prev"] = tailRunes(prev.String(), step.PrevWindowSize())
			}
		}
		text, callErr := t.nodeCall(turn, step.Node(), vars, itemVars)
		if callErr != nil {
			failures = append(failures, fmt.Sprintf("第%d项: %v", i+1, callErr))
			break // 保留已完成部分，失败即返回（重试时跳过已完成项）
		}
		results.Set(i, value.NewText(text))
		t.suite.store.MarkItemDone(st, step.Name(), i)
		t.emitProgress(sctx, st.Workflow.Id, step.Name(), "item",
			fmt.Sprintf("%d/%d", i+1, len(arr)))
	}
	if len(failures) > 0 {
		return results, "", fmt.Errorf("迭代部分失败（%d/%d 项已完成）：%s。重试 exec_node 只补跑失败项",
			countDone(results), len(arr), strings.Join(failures, "；"))
	}
	return results, fmt.Sprintf("共 %d 项全部完成", len(arr)), nil
}

// nodeCall 零上下文一次性 LLM 调用（硬边界）：不带会话历史，
// 模板变量 = 共享变量(vars) + 项变量(itemVars)，不产生会话事件。
func (t *ExecNodeTool) nodeCall(turn *agent.Turn, nd *node.ChatNode, vars *value.Object, itemVars map[string]any) (string, error) {
	sctx := turn.Context()
	merged := vars.ToMap()
	for k, v := range itemVars {
		merged[k] = v
	}

	system := exec.RenderTemplate(nd.SystemTemplate(), merged)
	user := exec.RenderTemplate(nd.UserTemplate(), merged)
	request := &chat.Request{
		Model:     nd.Model(),
		MaxTokens: 8192,
		Stream:    true,
		System:    system,
		// 节点是一次性任务生成，默认关闭扩展思考：避免简单任务（如缝合/拼接）
		// 触发模型长时间思考拖慢 flow；需要时可用模板/选项自行引导推理
		Thinking: &chat.ThinkingConfig{Type: "disabled"},
		Messages: []chat.Message{chat.NewTextMessage(user)},
	}
	return sctx.ChatComplete(request)
}

// emitProgress 推送 flow_progress 事件（前端步骤进度/作品卡片）。
func (t *ExecNodeTool) emitProgress(sctx *agent.SessionContext, flowId, stepId, phase, output string) {
	if sctx == nil {
		return
	}
	payload := map[string]string{"flowId": flowId, "stepId": stepId, "phase": phase, "output": output}
	data, _ := json.Marshal(payload)
	sctx.AddEvent(chat.NewFlowProgressEvent(string(data)))
}

// ==================== FlowStore 执行核配套方法 ====================
// PrepareExec 校验依赖并返回执行期变量（input + 全部上游产出）。
func (s *FlowStore) PrepareExec(sessionId, stepName string) (*value.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[sessionId]
	if st == nil {
		return nil, fmt.Errorf("当前没有激活的 flow")
	}
	if err := checkDeps(st, stepName); err != nil {
		return nil, err
	}
	vars := value.NewObject()
	vars.AddAll(st.Input)
	vars.AddAll(st.Outputs)
	return vars, nil
}

// PartialResults 返回迭代步骤已有的部分结果与已完成项集合（重跑续跑用）。
func (s *FlowStore) PartialResults(st *FlowState, stepName string, total int) (*value.Array, map[int]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := value.NewArraySize(total)
	doneSet := make(map[int]bool)
	if arr := st.Outputs.Get(stepName); arr != nil && arr.IsArray() {
		arr.AsArray().ForEach(func(i int, v value.Value) bool {
			if i < total && v != nil {
				results.Set(i, v)
			}
			return true
		})
	}
	for i := range st.ItemDone[stepName] {
		if i < total && results.Get(i) != nil {
			doneSet[i] = true
		}
	}
	return results, doneSet
}

// MarkItemDone 登记迭代项完成。
func (s *FlowStore) MarkItemDone(st *FlowState, stepName string, index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.ItemDone[stepName] == nil {
		st.ItemDone[stepName] = make(map[int]bool)
	}
	st.ItemDone[stepName][index] = true
}

// ==================== 常量与辅助 ====================

// maxIterItems 迭代项数上限（防 LLM 喂巨批）。
const maxIterItems = 20

// findStep 按名称查找步骤。
func findStep(wf *exec.Workflow, name string) *exec.Step {
	for _, s := range wf.Steps {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// resolveIterSource 解析迭代源：从执行变量中取数组（支持 "step" 或 "step.field" 路径）；
// 字符串值尝试 JSON 解析为数组（节点产出常为 JSON 文本）。
func resolveIterSource(vars *value.Object, source string) ([]any, error) {
	v, ok := exec.ResolvePath(vars.ToMap(), source)
	if !ok {
		return nil, fmt.Errorf("迭代源 %q 不存在（检查上游步骤是否已执行、input 是否登记）", source)
	}
	switch t := v.(type) {
	case []any:
		return t, nil
	case string:
		var arr []any
		if err := json.Unmarshal([]byte(t), &arr); err != nil {
			return nil, fmt.Errorf("迭代源 %q 不是数组，也无法解析为 JSON 数组", source)
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("迭代源 %q 不是数组", source)
	}
}

// countDone 统计迭代结果中已完成项数。
func countDone(results *value.Array) int {
	n := 0
	results.ForEach(func(_ int, v value.Value) bool {
		if v != nil {
			n++
		}
		return true
	})
	return n
}
