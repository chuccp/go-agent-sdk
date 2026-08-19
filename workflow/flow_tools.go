package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
)

// ==================== FlowStore（对齐 TodoStore 的工具层状态） ====================

// FlowState 一个会话的激活 flow 状态：input、各步骤产出、步骤状态。
// 全部由工具层读写，主 LLM 不携带、不汇报（todo 式外部化状态）。
type FlowState struct {
	Workflow *exec.Workflow
	Input    *value.Object
	Outputs  *value.Object // step_id → 节点输出（迭代步骤为结果数组）
	Status   map[string]exec.StepStatus
	ItemDone map[string]map[int]bool // 迭代步骤：已完成项（index 级跳过）
	Reruns   map[string]int          // 步骤重跑计数（上游重跑失效下游用）
}

// FlowStore 按 sessionId 管理激活的 flow（一会话一槽，新激活覆盖）。
type FlowStore struct {
	mu     sync.Mutex
	states map[string]*FlowState
}

func NewFlowStore() *FlowStore {
	return &FlowStore{states: make(map[string]*FlowState)}
}

// Activate 激活（或幂等更新）会话的 flow。已激活且 flowId 相同时合并 input
// （新键追加、同键覆盖）；不同 flowId 时覆盖旧状态。返回状态与"是否新激活"。
func (s *FlowStore) Activate(sessionId, flowId string, wf *exec.Workflow, input *value.Object) (*FlowState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if st, ok := s.states[sessionId]; ok && st.Workflow.Id == flowId {
		st.Input.AddAll(input)
		s.refreshDoneWhen(st)
		return st, false
	}
	if input == nil {
		input = value.NewObject()
	}
	st := &FlowState{
		Workflow: wf,
		Input:    input,
		Outputs:  value.NewObject(),
		Status:   make(map[string]exec.StepStatus),
		ItemDone: make(map[string]map[int]bool),
		Reruns:   make(map[string]int),
	}
	for _, step := range wf.Steps {
		st.Status[step.Name()] = exec.StepPending
	}
	s.refreshDoneWhen(st)
	s.states[sessionId] = st
	return st, true
}

// Get 返回会话的激活状态。
func (s *FlowStore) Get(sessionId string) *FlowState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[sessionId]
}

// Remove 清理会话状态（finish / 会话移除时）。
func (s *FlowStore) Remove(sessionId string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, sessionId)
}

// refreshDoneWhen 声明式完成判定：DoneWhen 键全部存在于 input 的 Talk 步骤自动完成。
// 调用方需持有 s.mu。
func (s *FlowStore) refreshDoneWhen(st *FlowState) {
	for _, step := range st.Workflow.Steps {
		if step.Kind() != exec.StepTalk || len(step.DoneWhenKeys()) == 0 {
			continue
		}
		done := true
		for _, key := range step.DoneWhenKeys() {
			if !st.Input.HasKey(key) {
				done = false
				break
			}
		}
		if done {
			st.Status[step.Name()] = exec.StepCompleted
		} else if st.Status[step.Name()] == exec.StepCompleted && !done {
			st.Status[step.Name()] = exec.StepPending
		}
	}
}

// CardProgress 计算进度：各步骤状态 + 建议下一步（第一个依赖满足的未完成步骤）。
func (s *FlowStore) CardProgress(st *FlowState) *exec.CardProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := make(map[string]exec.StepStatus, len(st.Status))
	for k, v := range st.Status {
		status[k] = v
	}
	return &exec.CardProgress{Status: status, Next: nextStep(st)}
}

// nextStep 建议下一步：按声明顺序找第一个未完成步骤（依赖=之前所有步骤）。
// 调用方需持有 s.mu。
func nextStep(st *FlowState) string {
	for _, step := range st.Workflow.Steps {
		if st.Status[step.Name()] == exec.StepCompleted {
			continue
		}
		if step.Kind() == exec.StepTalk {
			if len(step.DoneWhenKeys()) > 0 {
				return fmt.Sprintf("按剧本完成「%s」，然后用 activate_flow 补录结论", step.Title())
			}
			return fmt.Sprintf("按剧本完成「%s」，然后调用 flow_step_done(%q)", step.Title(), step.Name())
		}
		return fmt.Sprintf("exec_node(%q)", step.Name())
	}
	return ""
}

// CheckDeps 校验步骤之前的所有步骤已完成（同 todo 的 blocked_by 校验）。
// 调用方需持有 s.mu。
func checkDeps(st *FlowState, stepName string) error {
	for _, step := range st.Workflow.Steps {
		if step.Name() == stepName {
			return nil
		}
		if st.Status[step.Name()] != exec.StepCompleted {
			return fmt.Errorf("步骤 %q 的前置步骤 %q 尚未完成，请先完成它", stepName, step.Title())
		}
	}
	return fmt.Errorf("未找到步骤: %s", stepName)
}

// AllDone 是否全部步骤完成（finish 验收）。
func (s *FlowStore) AllDone(st *FlowState) (bool, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var missing []string
	for _, step := range st.Workflow.Steps {
		if st.Status[step.Name()] != exec.StepCompleted {
			missing = append(missing, step.Title())
		}
	}
	return len(missing) == 0, missing
}

// MarkStepDone 标记步骤完成。
func (s *FlowStore) MarkStepDone(sessionId, stepName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.states[sessionId]; st != nil {
		st.Status[stepName] = exec.StepCompleted
	}
}

// SetOutput 登记步骤输出；输出变化时清空下游步骤产出（上游重跑失效）。
// 返回输出是否发生变化。
func (s *FlowStore) SetOutput(sessionId, stepName string, output any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[sessionId]
	if st == nil {
		return false
	}
	old := st.Outputs.Get(stepName)
	existed := old != nil
	changed := !existed || !jsonEqual(old, output)
	st.Outputs.PutAny(stepName, output)
	if changed && existed {
		st.Reruns[stepName]++
		invalidateDownstream(st, stepName)
	}
	return changed
}

// invalidateDownstream 清空指定步骤之后所有步骤的输出与完成状态。
// 调用方需持有 s.mu。
func invalidateDownstream(st *FlowState, stepName string) {
	found := false
	for _, step := range st.Workflow.Steps {
		if found {
			st.Outputs.Delete(step.Name())
			delete(st.ItemDone, step.Name())
			if step.Kind() == exec.StepExec {
				st.Status[step.Name()] = exec.StepPending
			}
		}
		if step.Name() == stepName {
			found = true
		}
	}
}

// ==================== 工具组 ====================

// FlowToolSuite flow 工具组：共享 FlowStore 与 workflow 注册表。
type FlowToolSuite struct {
	store     *FlowStore
	workflows *Manager
}

// NewFlowTools 创建五个 flow 工具：
// activate_flow / exec_node / flow_step_done / flow_status / finish_flow。
func NewFlowTools(workflows *Manager) (activate, execNode, stepDone, status, finish agent.ToolExecutor) {
	suite := &FlowToolSuite{store: NewFlowStore(), workflows: workflows}
	return &ActivateFlowTool{suite}, &ExecNodeTool{suite}, &FlowStepDoneTool{suite}, &FlowStatusTool{suite}, &FinishFlowTool{suite}
}

// findWorkflow 按 id 查找已注册 flow。
func (s *FlowToolSuite) findWorkflow(flowId string) *exec.Workflow {
	for _, wf := range s.workflows.Workflows() {
		if wf.Id == flowId {
			return wf
		}
	}
	return nil
}

// flowIds 所有已注册 flow 的 id（用于 definition 的 enum）。
func (s *FlowToolSuite) flowIds() []string {
	wfs := s.workflows.Workflows()
	ids := make([]string, 0, len(wfs))
	for _, wf := range wfs {
		ids = append(ids, wf.Id)
	}
	return ids
}

// flowListDesc 拼接已注册 flow 清单（写进工具 description，LLM 据此选择）。
func (s *FlowToolSuite) flowListDesc() string {
	wfs := s.workflows.Workflows()
	if len(wfs) == 0 {
		return "（当前无可用 flow）"
	}
	var sb strings.Builder
	for _, wf := range wfs {
		sb.WriteString(fmt.Sprintf("\n- %s: %s", wf.Id, wf.Name))
		if wf.Description != "" {
			sb.WriteString(" —— " + wf.Description)
		}
	}
	return sb.String()
}

// footer 生成进度脚标（附在每次 tool_result 末尾）。
func (s *FlowToolSuite) footer(st *FlowState) string {
	return "\n" + st.Workflow.ProgressFooter(s.store.CardProgress(st))
}

// ==================== ActivateFlowTool ====================

type ActivateFlowTool struct{ suite *FlowToolSuite }

func (t *ActivateFlowTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 agent.PromptProvider：flow 触发引导随工具注入每轮 System，
// 宿主应用无需硬编码全局提示词。
func (t *ActivateFlowTool) UsagePrompt() string {
	return "【flow 使用引导】可用 flow：" + t.suite.flowListDesc() +
		"\n当用户的请求匹配某个 flow 时，应优先调用 activate_flow 激活它，" +
		"并严格按返回的卡片剧本推进；用户原话已包含的信息直接登记，不重复提问。"
}

func (t *ActivateFlowTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "activate_flow",
		Description: "激活一个 flow 并按其剧本执行。幂等：flow 已激活时再次调用会合并 input" +
			"（用于补录对话中获得的新信息：确认结果、附加要求、中途修改）。" +
			" 首次激活返回完整执行卡片（执行守则 + 步骤剧本 + 进度 + 建议下一步），" +
			"之后严格按卡片推进。可用 flow 列表：" + t.suite.flowListDesc(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow_id": map[string]any{
					"type": "string", "enum": t.suite.flowIds(),
					"description": "要激活的 flow ID",
				},
				"input": map[string]any{
					"type":        "object",
					"description": "flow 入参（如 {topic: '太空', audience: '儿童'}）；再次调用时与已有 input 合并",
				},
			},
			"required": []string{"flow_id"},
		},
	}
}

func (t *ActivateFlowTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	args := turn.Args()
	flowId := args.GetString("flow_id")
	if flowId == "" {
		writer.ErrorText(errors.New("缺少 flow_id 参数"))
		return
	}
	wf := t.suite.findWorkflow(flowId)
	if wf == nil {
		writer.ErrorText(fmt.Errorf("未知 flow: %s", flowId))
		return
	}
	input := args.GetObject("input")
	sessionId := sessionIdOf(turn)

	st, fresh := t.suite.store.Activate(sessionId, flowId, wf, input)
	progress := t.suite.store.CardProgress(st)
	if fresh {
		// 首次激活：完整卡片入场（todo 式：经 tool_result 进历史，不碰 System）
		writer.FullText(wf.RenderCard(progress))
		return
	}
	writer.FullText("已补录 input，flow 继续。" + t.suite.footer(st))
}

// ==================== FlowStepDoneTool ====================

type FlowStepDoneTool struct{ suite *FlowToolSuite }

func (t *FlowStepDoneTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *FlowStepDoneTool) UsagePrompt() string { return "" }

func (t *FlowStepDoneTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "flow_step_done",
		Description: "标记一个对话步骤（talk）已完成。仅用于无自动完成条件（DoneWhen）的对话步骤，" +
			"确认已在对话中真实完成后才可调用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"step_id": map[string]any{
					"type":        "string",
					"description": "已完成的对话步骤 ID",
				},
			},
			"required": []string{"step_id"},
		},
	}
}

func (t *FlowStepDoneTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	args := turn.Args()
	stepId := args.GetString("step_id")
	st := t.suite.store.Get(sessionIdOf(turn))
	if st == nil {
		writer.ErrorText(errors.New("当前没有激活的 flow"))
		return
	}
	step := findStep(st.Workflow, stepId)
	if step == nil {
		writer.ErrorText(fmt.Errorf("不存在步骤: %s", stepId))
		return
	}
	if step.Kind() != exec.StepTalk {
		writer.ErrorText(fmt.Errorf("步骤 %q 是执行步骤，请用 exec_node 执行", step.Title()))
		return
	}
	t.suite.store.MarkStepDone(sessionIdOf(turn), stepId)
	writer.FullText(
		fmt.Sprintf("步骤「%s」已标记完成。", step.Title()) + t.suite.footer(st))
}

// ==================== FlowStatusTool ====================

type FlowStatusTool struct{ suite *FlowToolSuite }

func (t *FlowStatusTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *FlowStatusTool) UsagePrompt() string { return "" }

func (t *FlowStatusTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name:        "flow_status",
		Description: "查询当前激活 flow 的全量状态（步骤进度、input、各步骤产出摘要）。不确定进行到哪一步时调用。",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *FlowStatusTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	st := t.suite.store.Get(sessionIdOf(turn))
	if st == nil {
		writer.FullText("（当前无激活的 flow）")
		return
	}
	t.suite.store.mu.Lock()
	defer t.suite.store.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Flow: %s（%s）\n", st.Workflow.Name, st.Workflow.Id))
	sb.WriteString("input: " + jsonBrief(st.Input) + "\n")
	for _, step := range st.Workflow.Steps {
		icon := "○"
		if st.Status[step.Name()] == exec.StepCompleted {
			icon = "✓"
		}
		sb.WriteString(fmt.Sprintf("%s %s（%s）", icon, step.Title(), step.Kind()))
		if out := st.Outputs.Get(step.Name()); out != nil {
			sb.WriteString("，产出: " + summarize(out.String(), 120))
		}
		sb.WriteString("\n")
	}
	if next := nextStep(st); next != "" {
		sb.WriteString("建议下一步: " + next)
	}
	writer.FullText(sb.String())
}

// ==================== FinishFlowTool ====================

type FinishFlowTool struct{ suite *FlowToolSuite }

func (t *FinishFlowTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *FinishFlowTool) UsagePrompt() string { return "" }

func (t *FinishFlowTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "finish_flow",
		Description: "收尾当前激活的 flow。action=complete 验收并清理（有未完成步骤会被拒绝）；" +
			"action=abandon 用户明确放弃时清理。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string", "enum": []string{"complete", "abandon"},
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "一句话总结 flow 成果（可选）",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *FinishFlowTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	args := turn.Args()
	action := args.GetString("action")
	sessionId := sessionIdOf(turn)
	st := t.suite.store.Get(sessionId)
	if st == nil {
		writer.FullText("（当前无激活的 flow，无需收尾）")
		return
	}

	switch action {
	case "abandon":
		t.suite.store.Remove(sessionId)
		writer.FullText("flow 已放弃并清理。")
	case "complete":
		ok, missing := t.suite.store.AllDone(st)
		if !ok {
			writer.ErrorText(fmt.Errorf("尚有步骤未完成: %s，请先完成后再收尾", strings.Join(missing, "、")))
			return
		}
		t.suite.store.Remove(sessionId)
		summary := args.GetString("summary")
		text := "flow 已完成并清理。"
		if summary != "" {
			text += " 总结: " + summary
		}
		writer.FullText(text)
	default:
		writer.ErrorText(fmt.Errorf("未知 action: %s（可选 complete/abandon）", action))
	}
}

// ==================== 辅助函数 ====================

// sessionIdOf 从工具执行上下文取会话 ID。
func sessionIdOf(turn *agent.Turn) string {
	if ctx := turn.Context(); ctx != nil {
		return ctx.ID()
	}
	return ""
}

// jsonEqual 以 JSON 序列化比较两个值是否相等。
func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ja) == string(jb)
}

// jsonBrief 紧凑 JSON（用于状态展示）。
func jsonBrief(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// summarize 生成输出摘要：字符串截断，其余 JSON 化后截断。
func summarize(v any, max int) string {
	var s string
	if str, ok := v.(string); ok {
		s = str
	} else {
		s = jsonBrief(v)
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// tailRunes 取字符串尾部 n 个 rune（{{prev}} 滑动窗口）。
func tailRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
