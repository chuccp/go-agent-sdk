package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

// ==================== 迭代 flow 定义（与 example expand001 同构） ====================

func newExpandFlow() *exec.Workflow {
	splitNode := node.NewChatNodeBuilder("split").
		SystemTemplate("你是故事结构分析师").
		UserTemplate("把梗概分成 3 段，输出 JSON 数组：{{story}}").
		Build()
	expandNode := node.NewChatNodeBuilder("expand").
		SystemTemplate("你是故事扩写作者").
		UserTemplate("整体分段：{{split}}\n前文衔接：{{prev}}\n扩写本段：{{item.title}}（{{item.summary}}）").
		Build()
	mergeNode := node.NewChatNodeBuilder("merge").
		SystemTemplate("你是故事编辑").
		UserTemplate("缝合各段：{{expand}}").
		Build()
	return exec.NewBuilder("expand001", "故事扩写").
		Steps(
			exec.Exec("split", splitNode),
			exec.Exec("expand", expandNode).Iterate("split").PrevWindow(120),
			exec.Exec("merge", mergeNode),
			exec.Talk("deliver", "交付", "呈现全文"),
		).
		Build()
}

// ==================== fake provider：主循环走剧本，节点按 System 分发 ====================

const splitJSON = `[{"title":"起","summary":"s1"},{"title":"承","summary":"s2"},{"title":"合","summary":"s3"}]`

type iterFakeProvider struct {
	mainCalls   int
	splitCalls  int
	expandCalls int
	mergeCalls  int
	expandUsers []string // 每次 expand 调用渲染后的 user 文本
	mergeUser   string
}

func (f *iterFakeProvider) script() []chat.Blocks {
	toolUseJSON := func(id, name string, input string) chat.Blocks {
		tu := chat.NewToolUseBlock(id, name)
		tu.Input, _ = value.NewObjectFromJson(json.RawMessage(input))
		return chat.Blocks{tu}
	}
	return []chat.Blocks{
		toolUseJSON("t1", "activate_flow", `{"flow_id":"expand001","input":{"story":"小狐狸看月亮"}}`),
		toolUseJSON("t2", "exec_node", `{"step_id":"split"}`),
		toolUseJSON("t3", "exec_node", `{"step_id":"expand"}`),
		toolUseJSON("t4", "exec_node", `{"step_id":"merge"}`),
		toolUseJSON("t5", "flow_step_done", `{"step_id":"deliver"}`),
		toolUseJSON("t6", "finish_flow", `{"action":"complete"}`),
		chat.Blocks{chat.NewFullTextBlock("扩写完成！")},
	}
}

func (f *iterFakeProvider) ID() string { return "iter-fake" }
func (f *iterFakeProvider) ChatWithStream(_ context.Context, req *chat.Messages, w *chat.BlockStream) error {
	if len(req.Tools) == 0 {
		// 零上下文节点调用：按 System 模板识别节点
		userText := ""
		for _, b := range req.Messages[0].Content {
			if tb, ok := b.(*chat.TextBlock); ok {
				userText += tb.Text
			}
		}
		switch {
		case strings.Contains(req.Config.GetSystemPrompt(), "结构分析师"):
			f.splitCalls++
			emitText(w, splitJSON)
		case strings.Contains(req.Config.GetSystemPrompt(), "扩写作者"):
			f.expandCalls++
			f.expandUsers = append(f.expandUsers, userText)
			emitText(w, fmt.Sprintf("扩写段落%d的内容", f.expandCalls))
		case strings.Contains(req.Config.GetSystemPrompt(), "故事编辑"):
			f.mergeCalls++
			f.mergeUser = userText
			emitText(w, "缝合后的全文")
		default:
			emitText(w, "未知节点")
		}
		return nil
	}
	f.mainCalls++
	blocks := f.script()[f.mainCalls-1]
	stop := chat.StopReasonToolUse
	for _, b := range blocks {
		if tu, ok := b.(*chat.ToolUseBlock); ok {
			w.BlockToolUseStart(tu.ID, tu.Name)
			w.Delta(mustJSON(tu.Input))
			continue
		}
		if tb, ok := b.(*chat.TextBlock); ok {
			stop = chat.StopReasonEndTurn
			w.BlockTextStart()
			w.Delta(tb.Text)
			continue
		}
	}
	w.StopReason(stop)
	return nil
}

// ==================== 辅助 ====================

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// ==================== 测试 ====================

// TestFlowIteration 迭代链路端到端：split 产出 JSON 数组 → expand 逐项执行
// （{{item}} 渲染 + {{prev}} 滑动窗口 + item 进度事件）→ merge 消费聚合数组。
func TestFlowIteration(t *testing.T) {
	config := agent.NewConfig()
	llm := &iterFakeProvider{}
	config.RegisterChat(llm)
	wf := workflow.NewManager()
	wf.AddWorkflow(newExpandFlow())

	activate, execNode, stepDone, _, finish := workflow.NewFlowTools(wf)
	config.AddTools(activate, execNode, stepDone, finish)

	manager := config.CreateAgent(context.Background())
	client := manager.GetOrCreateSession("flow-iter").CreateClient(context.Background(), 0)
	client.WriteText("把「小狐狸看月亮」扩写成故事")
	events := collectUntilDone(t, client)

	// ① 节点调用次数：split 1 次、expand 3 次（逐项）、merge 1 次
	if llm.splitCalls != 1 || llm.expandCalls != 3 || llm.mergeCalls != 1 {
		t.Fatalf("调用次数 split=%d expand=%d merge=%d, want 1/3/1",
			llm.splitCalls, llm.expandCalls, llm.mergeCalls)
	}
	// ② {{item.title}}/{{item.summary}} 渲染正确
	if !strings.Contains(llm.expandUsers[0], "扩写本段：起（s1）") {
		t.Errorf("第1项 item 渲染错误: %q", llm.expandUsers[0])
	}
	if !strings.Contains(llm.expandUsers[2], "扩写本段：合（s3）") {
		t.Errorf("第3项 item 渲染错误: %q", llm.expandUsers[2])
	}
	// ③ {{prev}} 滑动窗口：第1项无 prev（占位符保留），第2项携带第1项输出
	if !strings.Contains(llm.expandUsers[0], "{{prev}}") {
		t.Errorf("第1项不应有 prev 值: %q", llm.expandUsers[0])
	}
	if !strings.Contains(llm.expandUsers[1], "前文衔接：扩写段落1的内容") {
		t.Errorf("第2项 prev 应为第1项输出: %q", llm.expandUsers[1])
	}
	if !strings.Contains(llm.expandUsers[2], "前文衔接：扩写段落2的内容") {
		t.Errorf("第3项 prev 应为第2项输出: %q", llm.expandUsers[2])
	}
	// ④ merge 消费聚合数组（JSON 化的 3 段产出）
	if !strings.Contains(llm.mergeUser, "扩写段落1的内容") ||
		!strings.Contains(llm.mergeUser, "扩写段落3的内容") {
		t.Errorf("merge 未拿到聚合数组: %q", llm.mergeUser)
	}
	// ⑤ 逐项进度事件：3 条 phase=item（通过 TextBlock 的 FlowProgressType 标记）
	itemEvents := 0
	for _, e := range events {
		if tb, ok := e.Blocks[0].(*chat.TextBlock); ok && tb.TextType == chat.FlowProgressType && strings.Contains(tb.Text, `"phase":"item"`) {
			itemEvents++
		}
	}
	if itemEvents != 3 {
		t.Errorf("item 进度事件 = %d, want 3", itemEvents)
	}
}
