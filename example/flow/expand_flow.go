package flow

import (
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

// GetExpandFlow 故事扩写 flow（迭代示例）：
// split 分段 → expand 对 segments 逐项扩写（{{item}} + {{prev}} 滑动窗口）→ merge 缝合。
// 演示 Iterate / PrevWindow / index 级断点续跑 / 上游重跑失效下游。
func (s *StoreFlow) GetExpandFlow() *exec.Workflow {
	splitNode := node.NewChatNodeBuilder("split").
		SystemTemplate("你是故事结构分析师").
		UserTemplate("把下面的故事梗概分成 3 个扩写段落，只输出 JSON 数组（不要任何其他文字），" +
			"元素格式 {\"title\": 段落标题, \"summary\": 该段要写的内容概要}：\n{{story}}").
		Deliver(node.DeliverEvent).
		Build()

	expandNode := node.NewChatNodeBuilder("expand").
		SystemTemplate("你是故事扩写作者，文风细腻有画面感").
		UserTemplate("整体分段：{{split}}\n前文衔接：{{prev}}\n" +
			"请把这一段扩写成 60 字以内的细腻段落（只输出段落本身）：\n" +
			"标题：{{item.title}}\n概要：{{item.summary}}").
		Build()

	mergeNode := node.NewChatNodeBuilder("merge").
		SystemTemplate("你是故事编辑").
		UserTemplate("把下面各段扩写内容按顺序缝合成一篇连贯的短文，" +
			"保持原文、只补充必要的过渡，输出全文：\n{{expand}}").
		Deliver(node.DeliverContext).
		Build()

	return exec.NewBuilder("expand001", "故事扩写").
		Description("把故事梗概分段后逐段扩写，最后缝合成完整故事（迭代示例）").
		InputSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"story": map[string]any{"type": "string", "description": "故事梗概"},
			},
			"required": []string{"story"},
		}).
		Steps(
			exec.Exec("split", splitNode),
			exec.Exec("expand", expandNode).
				Iterate("split").   // 迭代源：split 步骤的输出（JSON 数组）
				PrevWindow(120),    // {{prev}} 只取上一段尾部 120 字
			exec.Exec("merge", mergeNode),
			exec.Talk("deliver", "交付", `
把扩写全文呈现给用户（全文已随事件展示），询问是否要调整某一段；
调整单段 → activate_flow 补录要求后重新 exec_node("expand")（只补跑变化项）`),
		).
		Build()
}
