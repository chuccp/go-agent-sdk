package flow

import (
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
	"github.com/chuccp/go-web-frame/core"
)

type StoreFlow struct {
	ctx *core.Context
}

func (s *StoreFlow) Init(ctx *core.Context) error {
	s.ctx = ctx
	return nil
}

// GetFlow 故事生成 flow（v3 剧本式）：
// 主 LLM 按卡片剧本编排，执行步骤经 exec_node 零上下文执行。
func (s *StoreFlow) GetFlow() *exec.Workflow {
	storyNode := node.NewChatNodeBuilder("story").
		SystemTemplate("你是一位富有想象力的故事创作者").
		UserTemplate("主题：{{topic}}\n受众：{{audience}}\n附加要求：{{note}}\n请创作一个约 800 字的故事。").
		Deliver(node.DeliverContext).
		Build()

	return exec.NewBuilder("story003", "故事生成").
		Description("根据主题生成定制故事：先确认受众，再创作，最后交付").
		InputSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"topic":    map[string]any{"type": "string", "description": "故事主题"},
				"audience": map[string]any{"type": "string", "description": "受众：儿童/成人"},
				"note":     map[string]any{"type": "string", "description": "附加要求（可选）"},
			},
			"required": []string{"topic"},
		}).
		Steps(
			exec.Talk("confirm", "确认受众与要求", `
目标：明确故事的受众（儿童/成人），收集附加要求
指引：
- 用户消息已表明受众（如"给孩子"）→ 直接登记，不问
- 否则用 ask_user_question 提问，相关问题可合并一次问
- 用户回答中的附加要求（如"加一条龙"）一并登记`).
				DoneWhen("audience"),
			exec.Exec("story", storyNode),
			exec.Talk("deliver", "交付", `
把故事呈现给用户（全文已随事件展示），询问是否需要润色；
用户提出修改 → 用 activate_flow 补录要求后重新 exec_node("story")`),
		).
		Build()
}
