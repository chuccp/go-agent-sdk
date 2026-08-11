package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/tools"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

// ==================== fake providers ====================

// flowFakeProvider 扮演主 LLM：按剧本依次发出工具调用，驱动 story003 走完全程。
// 通过 req.Tools 是否为空区分主循环调用（有工具列表）与 exec_node 的
// 零上下文节点调用（无工具列表），分别计数验证。
type flowFakeProvider struct {
	mainCalls   int
	nodeCalls   int
	nodeReqs    []*chat.Request
	mainSystems []string // 每轮主循环调用收到的 System
}

func (f *flowFakeProvider) script() []chat.Blocks {
	toolUse := func(id, name string, input map[string]any) chat.Blocks {
		return chat.Blocks{chat.NewToolUseBlock(id, name, input)}
	}
	return []chat.Blocks{
		// 轮1: activate_flow（原话已含主题+受众，零提问直通）
		toolUse("t1", "activate_flow", map[string]any{
			"flow_id": "story003",
			"input":   map[string]any{"topic": "太空", "audience": "儿童"},
		}),
		// 轮2: exec_node("story")（confirm 已 DoneWhen 自动完成）
		toolUse("t2", "exec_node", map[string]any{"step_id": "story"}),
		// 轮3: flow_step_done("deliver")
		toolUse("t3", "flow_step_done", map[string]any{"step_id": "deliver"}),
		// 轮4: finish_flow(complete)
		toolUse("t4", "finish_flow", map[string]any{"action": "complete"}),
		// 轮5: 收尾文本（end_turn，主循环结束）
		chat.Blocks{chat.NewTextBlock("故事已经写好啦！")},
	}
}

func (f *flowFakeProvider) ChatWithStream(_ context.Context, req *chat.Request, w chat.StreamWriter) error {
	if len(req.Tools) == 0 {
		// exec_node 的零上下文节点调用：应只有 1 条 messages、无工具
		f.nodeCalls++
		f.nodeReqs = append(f.nodeReqs, req)
		emitText(w, "很久以前，在遥远的星系……")
		return nil
	}
	f.mainCalls++
	f.mainSystems = append(f.mainSystems, req.System)
	blocks := f.script()[f.mainCalls-1]
	stop := chat.StopReasonToolUse
	for i, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			stop = chat.StopReasonEndTurn
			w.Write(&chat.ContentBlockStartEvent{Index: i, ContentBlock: chat.NewTextBlock("")})
			w.Write(&chat.ContentBlockDeltaEvent{Index: i, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: tb.Text}})
			w.Write(&chat.ContentBlockStopEvent{Index: i})
			continue
		}
		if tu, ok := b.(*chat.ToolUseBlock); ok {
			// 模拟真实 LLM：start 只带 id/name，入参经 input_json_delta 流式下发
			w.Write(&chat.ContentBlockStartEvent{Index: i, ContentBlock: chat.NewToolUseBlock(tu.ID, tu.Name, nil)})
			inputJSON, _ := json.Marshal(tu.Input)
			w.Write(&chat.ContentBlockDeltaEvent{Index: i, Delta: chat.ContentDelta{Type: chat.DeltaTypeInputJSON, PartialJSON: string(inputJSON)}})
			w.Write(&chat.ContentBlockStopEvent{Index: i})
			continue
		}
		w.Write(&chat.ContentBlockStartEvent{Index: i, ContentBlock: b})
		w.Write(&chat.ContentBlockStopEvent{Index: i})
	}
	w.Write(&chat.MessageDeltaEvent{StopReason: stop})
	w.Write(&chat.MessageStopEvent{})
	return nil
}

// fakeStoryNode 供 guards 测试使用的简单节点 LLM。
type fakeStoryNode struct {
	last *chat.Request
}

func (f *fakeStoryNode) ChatWithStream(_ context.Context, req *chat.Request, w chat.StreamWriter) error {
	f.last = req
	emitText(w, "这是一个关于海洋的故事。")
	return nil
}

// emitText 以协议事件写出一段完整文本（end_turn）。
func emitText(w chat.StreamWriter, text string) {
	w.Write(&chat.MessageStartEvent{ID: "m", Model: "fake", Role: "assistant"})
	w.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewTextBlock("")})
	w.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: text}})
	w.Write(&chat.ContentBlockStopEvent{Index: 0})
	w.Write(&chat.MessageDeltaEvent{StopReason: chat.StopReasonEndTurn})
	w.Write(&chat.MessageStopEvent{})
}

// ==================== flow 定义（与 example 的 story003 同构） ====================

func newStoryFlow() *exec.Workflow {
	storyNode := node.NewChatNodeBuilder("story").
		SystemTemplate("你是一位故事创作者").
		UserTemplate("主题：{{topic}}，受众：{{audience}}，写一个故事").
		Deliver(node.DeliverEvent).
		Build()
	return exec.NewBuilder("story003", "故事生成").
		Description("根据主题生成定制故事").
		Steps(
			exec.Talk("confirm", "确认受众", "确认受众").DoneWhen("audience"),
			exec.Exec("story", storyNode),
			exec.Talk("deliver", "交付", "呈现故事"),
		).
		Build()
}

// ==================== 辅助 ====================

// textCollector 收集工具输出文本。
type textCollector struct{ text strings.Builder }

func (c *textCollector) Write(_ chat.Event) error { return nil }
func (c *textCollector) WriteBlock(b chat.Block) error {
	if tb, ok := b.(*chat.TextBlock); ok {
		c.text.WriteString(tb.Text)
	}
	return nil
}
func (c *textCollector) WriteError(_ error) {}
func (c *textCollector) Close()             {}
func (c *textCollector) Reset()             { c.text.Reset() }
func (c *textCollector) String() string     { return c.text.String() }

// collectUntilDone 读事件直到 done。
func collectUntilDone(t *testing.T, client *agent.Client) []*chat.ClientEvent {
	t.Helper()
	ch := make(chan []*chat.ClientEvent, 1)
	go func() {
		var events []*chat.ClientEvent
		for {
			evt := client.ReadEvent()
			if evt == nil {
				ch <- events
				return
			}
			events = append(events, evt)
			if evt.EventType == chat.EventTypeDone {
				ch <- events
				return
			}
		}
	}()
	select {
	case events := <-ch:
		return events
	case <-time.After(15 * time.Second):
		t.Fatal("15s 超时未收到 done")
		return nil
	}
}

func hasEvent(events []*chat.ClientEvent, eventType string) bool {
	for _, e := range events {
		if e.EventType == eventType {
			return true
		}
	}
	return false
}

// ==================== 端到端：主 LLM 按剧本完整走完 story003 ====================

func TestFlowEndToEnd(t *testing.T) {
	manager := agent.NewManager()
	mainLLM := &flowFakeProvider{}
	manager.RegisterChat("fake", mainLLM, true)
	manager.AddWorkflows(newStoryFlow())

	activate, execNode, stepDone, status, finish := tools.NewFlowTools(manager.WorkflowManager())
	manager.AddTools(activate, execNode, stepDone, status, finish)

	client, err := manager.GetClient("flow-e2e", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SendText("给我 5 岁孩子写个太空故事"); err != nil {
		t.Fatal(err)
	}
	events := collectUntilDone(t, client)

	// ① flow_progress 事件已推送（start/done 等）
	if !hasEvent(events, chat.EventTypeFlowProgress) {
		t.Error("未收到 flow_progress 事件")
	}
	// ② 主 LLM 共 5 轮：4 轮工具调用 + 1 轮收尾文本
	if mainLLM.mainCalls != 5 {
		t.Errorf("mainCalls = %d, want 5", mainLLM.mainCalls)
	}
	// ②′ 工具引导词随 PromptProvider 拼进每轮 System（宿主零硬编码）
	if len(mainLLM.mainSystems) == 0 ||
		!strings.Contains(mainLLM.mainSystems[0], "flow 使用引导") ||
		!strings.Contains(mainLLM.mainSystems[0], "story003") {
		t.Errorf("System 未拼接工具引导词: %q", mainLLM.mainSystems[0])
	}
	// ③ 零上下文硬边界：节点调用无工具、无历史，仅 1 条 messages，模板已渲染
	if mainLLM.nodeCalls != 1 {
		t.Fatalf("节点调用次数 = %d, want 1", mainLLM.nodeCalls)
	}
	nodeReq := mainLLM.nodeReqs[0]
	if len(nodeReq.Messages) != 1 {
		t.Errorf("节点调用 messages = %d, want 1（零上下文）", len(nodeReq.Messages))
	}
	if len(nodeReq.Tools) != 0 {
		t.Errorf("节点调用不应携带工具")
	}
	if nodeReq.System != "你是一位故事创作者" {
		t.Errorf("节点 system 模板渲染错误: %q", nodeReq.System)
	}
	userText := ""
	for _, b := range nodeReq.Messages[0].Content {
		if tb, ok := b.(*chat.TextBlock); ok {
			userText += tb.Text
		}
	}
	if !strings.Contains(userText, "主题：太空") || !strings.Contains(userText, "受众：儿童") {
		t.Errorf("节点 user 模板渲染错误: %q", userText)
	}
	// ④ finish 已清理：flow_status 报告无激活 flow
	w := &textCollector{}
	statusTurn := agent.NewTurnWithContext(manager.SessionContext("flow-e2e"), nil)
	if err := status.Execute(statusTurn, w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "无激活") {
		t.Errorf("finish 后应无激活 flow: %s", w.String())
	}
}

// ==================== 护栏：未激活/跳步/漏步 ====================

func TestFlowGuards(t *testing.T) {
	manager := agent.NewManager()
	storyLLM := &fakeStoryNode{}
	manager.RegisterChat("fake", storyLLM, true)
	manager.AddWorkflows(newStoryFlow())

	activate, execNode, _, _, finish := tools.NewFlowTools(manager.WorkflowManager())
	sctx := manager.SessionContext("flow-guards")
	turn := func(args map[string]any) *agent.Turn {
		return agent.NewTurnWithContext(sctx, args)
	}
	w := &textCollector{}

	// ① 未激活时 exec_node 被拒
	err := execNode.Execute(turn(map[string]any{"step_id": "story"}), w)
	if err == nil || !strings.Contains(err.Error(), "activate_flow") {
		t.Errorf("未激活 exec 应报错: %v", err)
	}

	// ② 激活（不登记 audience）→ confirm 未完成 → exec story 被依赖校验拒绝
	w.Reset()
	if err := activate.Execute(turn(map[string]any{
		"flow_id": "story003", "input": map[string]any{"topic": "太空"},
	}), w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "【执行守则】") || !strings.Contains(w.String(), "【步骤】") {
		t.Errorf("首次激活应返回完整卡片: %s", w.String())
	}
	w.Reset()
	err = execNode.Execute(turn(map[string]any{"step_id": "story"}), w)
	if err == nil || !strings.Contains(err.Error(), "前置步骤") {
		t.Errorf("跳步应被依赖校验拒绝: %v", err)
	}

	// ③ 补录 audience → DoneWhen 自动完成 confirm → exec 成功（零上下文调用）
	w.Reset()
	if err := activate.Execute(turn(map[string]any{
		"flow_id": "story003", "input": map[string]any{"audience": "儿童"},
	}), w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "已补录") {
		t.Errorf("幂等更新应返回补录确认: %s", w.String())
	}
	w.Reset()
	if err := execNode.Execute(turn(map[string]any{"step_id": "story"}), w); err != nil {
		t.Fatalf("依赖满足后 exec 应成功: %v", err)
	}
	if !strings.Contains(w.String(), "执行完成") || !strings.Contains(w.String(), "【进度】") {
		t.Errorf("exec 结果应含摘要与进度脚标: %s", w.String())
	}
	if storyLLM.last == nil || !strings.Contains(toString(storyLLM.last.Messages[0].Content), "主题：太空") {
		t.Error("节点调用模板未正确渲染上游 input")
	}

	// ④ deliver 未完成 → finish(complete) 被验收拒绝
	w.Reset()
	errs := finish.Execute(turn(map[string]any{"action": "complete"}), w)
	if errs == nil || !strings.Contains(errs.Error(), "未完成") {
		t.Errorf("漏步 finish 应被拒: %v", errs)
	}

	// ⑤ abandon 成功清理
	w.Reset()
	if err := finish.Execute(turn(map[string]any{"action": "abandon"}), w); err != nil {
		t.Errorf("abandon 应成功: %v", err)
	}
}

// toString 提取 blocks 中的文本。
func toString(blocks chat.Blocks) string {
	var sb strings.Builder
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}
