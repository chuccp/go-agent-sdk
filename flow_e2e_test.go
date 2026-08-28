package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow"
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
	nodeReqs    []*chat.Messages
	mainSystems []string // 每轮主循环调用收到的 System
}

func (f *flowFakeProvider) script() []chat.Blocks {
	toolUseJSON := func(id, name string, input string) chat.Blocks {
		tu := chat.NewToolUseBlock(id, name)
		tu.Input, _ = value.NewObjectFromJson(json.RawMessage(input))
		return chat.Blocks{tu}
	}
	return []chat.Blocks{
		// 轮1: activate_flow（原话已含主题+受众，零提问直通）
		toolUseJSON("t1", "activate_flow", `{"flow_id":"story003","input":{"topic":"太空","audience":"儿童"}}`),
		// 轮2: exec_node("story")（confirm 已 DoneWhen 自动完成）
		toolUseJSON("t2", "exec_node", `{"step_id":"story"}`),
		// 轮3: flow_step_done("deliver")
		toolUseJSON("t3", "flow_step_done", `{"step_id":"deliver"}`),
		// 轮4: finish_flow(complete)
		toolUseJSON("t4", "finish_flow", `{"action":"complete"}`),
		// 轮5: 收尾文本（end_turn，主循环结束）
		chat.Blocks{chat.NewFullTextBlock("故事已经写好啦！")},
	}
}

func (f *flowFakeProvider) ID() string        { return "flow-fake" }
func (f *flowFakeProvider) ChatWithStream(_ context.Context, req *chat.Messages, w *chat.BlockStream) error {
	if len(req.Tools) == 0 {
		// exec_node 的零上下文节点调用：应只有 1 条 messages、无工具
		f.nodeCalls++
		f.nodeReqs = append(f.nodeReqs, req)
		emitText(w, "很久以前，在遥远的星系……")
		return nil
	}
	f.mainCalls++
	f.mainSystems = append(f.mainSystems, req.Config.GetSystemPrompt())
	blocks := f.script()[f.mainCalls-1]
	stop := chat.StopReasonToolUse
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			stop = chat.StopReasonEndTurn
			w.BlockTextStart()
			w.Delta(tb.Text)
			continue
		}
		if tu, ok := b.(*chat.ToolUseBlock); ok {
			// 模拟真实 LLM：start 只带 id/name，入参经 Delta 流式下发
			w.BlockToolUseStart(tu.ID, tu.Name)
			inputJSON, _ := json.Marshal(tu.Input)
			w.Delta(string(inputJSON))
			continue
		}
	}
	w.StopReason(stop)
	return nil
}

// fakeStoryNode 供 guards 测试使用的简单节点 LLM。
type fakeStoryNode struct {
	last *chat.Messages
}

func (f *fakeStoryNode) ID() string        { return "story-fake" }
func (f *fakeStoryNode) ChatWithStream(_ context.Context, req *chat.Messages, w *chat.BlockStream) error {
	f.last = req
	emitText(w, "这是一个关于海洋的故事。")
	return nil
}

// emitText 以简化流项写出一段完整文本（end_turn）。
func emitText(w *chat.BlockStream, text string) {
	w.BlockTextStart()
	w.Delta(text)
	w.StopReason(chat.StopReasonEndTurn)
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

// execToolText 在统一 BlockStream 上执行工具并收集输出文本（错误已以文本写入）。
func execToolText(t *testing.T, exec agent.ToolExecutor, turn *agent.Turn) string {
	t.Helper()
	w := chat.NewBlockStream(nil)
	exec.Execute(turn, chat.NewToolResultBlockStream(w, "exec"))
	var sb strings.Builder
	blocks := w.ReadBlocks()
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// collectUntilDone 读事件直到 done block。
func collectUntilDone(t *testing.T, client *agent.Client) []*agent.Event {
	t.Helper()
	ch := make(chan []*agent.Event, 1)
	go func() {
		var events []*agent.Event
		for {
			batch := client.ReadEvents()
			if len(batch) == 0 {
				ch <- events
				return
			}
			for _, evt := range batch {
				events = append(events, evt)
				if _, ok := evt.Blocks[0].(*chat.DoneBlock); ok {
					ch <- events
					return
				}
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

// hasBlockType 检查事件列表中是否存在指定 block 类型。
func hasBlockType(events []*agent.Event, target chat.Block) bool {
	for _, e := range events {
		switch target.(type) {
		case *chat.TextBlock:
			if _, ok := e.Blocks[0].(*chat.TextBlock); ok {
				return true
			}
		case *chat.DoneBlock:
			if _, ok := e.Blocks[0].(*chat.DoneBlock); ok {
				return true
			}
		case *chat.ToolExecutionBlock:
			if _, ok := e.Blocks[0].(*chat.ToolExecutionBlock); ok {
				return true
			}
		case *chat.ToolUseBlock:
			if _, ok := e.Blocks[0].(*chat.ToolUseBlock); ok {
				return true
			}
		}
	}
	return false
}

// ==================== 端到端：主 LLM 按剧本完整走完 story003 ====================

func TestFlowEndToEnd(t *testing.T) {
	manager := agent.NewAgent()
	mainLLM := &flowFakeProvider{}
	manager.RegisterChat(mainLLM)
	wf := workflow.NewManager()
	wf.AddWorkflow(newStoryFlow())

	activate, execNode, stepDone, status, finish := workflow.NewFlowTools(wf)
	manager.AddTools(activate, execNode, stepDone, status, finish)

	client, err := manager.GetClient("flow-e2e", 0)
	if err != nil {
		t.Fatal(err)
	}
	client.WriteText("给我 5 岁孩子写个太空故事")
	events := collectUntilDone(t, client)

	// ① flow_progress block 已推送（start/done 等）
	if !hasBlockType(events, &chat.TextBlock{}) {
		t.Error("未收到 text block")
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
	if nodeReq.Config.GetSystemPrompt() != "你是一位故事创作者" {
		t.Errorf("节点 system 模板渲染错误: %q", nodeReq.Config.GetSystemPrompt())
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
	statusTurn := agent.NewTurnWithContext(manager.SessionContext("flow-e2e"), nil)
	out := execToolText(t, status, statusTurn)
	if !strings.Contains(out, "无激活") {
		t.Errorf("finish 后应无激活 flow: %s", out)
	}
}

// ==================== 护栏：未激活/跳步/漏步 ====================

func TestFlowGuards(t *testing.T) {
	manager := agent.NewAgent()
	storyLLM := &fakeStoryNode{}
	manager.RegisterChat(storyLLM)
	wf := workflow.NewManager()
	wf.AddWorkflow(newStoryFlow())

	activate, execNode, _, _, finish := workflow.NewFlowTools(wf)
	sctx := manager.SessionContext("flow-guards")
	turn := func(args map[string]any) *agent.Turn {
		return agent.NewTurnWithContext(sctx, value.NewObjectFromMap(args))
	}
	run := func(exec agent.ToolExecutor, args map[string]any) string {
		return execToolText(t, exec, turn(args))
	}

	// ① 未激活时 exec_node 被拒（错误写入输出文本）
	out := run(execNode, map[string]any{"step_id": "story"})
	if !strings.Contains(out, "activate_flow") {
		t.Errorf("未激活 exec 应报错: %s", out)
	}

	// ② 激活（不登记 audience）→ confirm 未完成 → exec story 被依赖校验拒绝
	out = run(activate, map[string]any{
		"flow_id": "story003", "input": map[string]any{"topic": "太空"},
	})
	if !strings.Contains(out, "【执行守则】") || !strings.Contains(out, "【步骤】") {
		t.Errorf("首次激活应返回完整卡片: %s", out)
	}
	out = run(execNode, map[string]any{"step_id": "story"})
	if !strings.Contains(out, "前置步骤") {
		t.Errorf("跳步应被依赖校验拒绝: %s", out)
	}

	// ③ 补录 audience → DoneWhen 自动完成 confirm → exec 成功（零上下文调用）
	out = run(activate, map[string]any{
		"flow_id": "story003", "input": map[string]any{"audience": "儿童"},
	})
	if !strings.Contains(out, "已补录") {
		t.Errorf("幂等更新应返回补录确认: %s", out)
	}
	out = run(execNode, map[string]any{"step_id": "story"})
	if !strings.Contains(out, "执行完成") || !strings.Contains(out, "【进度】") {
		t.Errorf("exec 结果应含摘要与进度脚标: %s", out)
	}
	if storyLLM.last == nil || !strings.Contains(toString(storyLLM.last.Messages[0].Content), "主题：太空") {
		t.Error("节点调用模板未正确渲染上游 input")
	}

	// ④ deliver 未完成 → finish(complete) 被验收拒绝（错误写入输出文本）
	out = run(finish, map[string]any{"action": "complete"})
	if !strings.Contains(out, "未完成") {
		t.Errorf("漏步 finish 应被拒: %s", out)
	}

	// ⑤ abandon 成功清理
	out = run(finish, map[string]any{"action": "abandon"})
	if !strings.Contains(out, "已放弃") {
		t.Errorf("abandon 应成功: %s", out)
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
