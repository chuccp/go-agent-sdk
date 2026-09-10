package tools

import (
	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// AskUserQuestionTool 让 LLM 在执行过程中向用户提出澄清问题。
// 实现 agent.ToolExecutor 接口：执行时置 user_wait 停止原因后立即返回（不阻塞）。
// user_wait 使会话主循环跳过本轮的 LLM 收尾调用、直接结束本轮；用户的回答作为
// 下一条普通消息进入会话，触发新一轮。
//
// 问题内容不由本工具推送：LLM 的入参已在 tool_use 块中随消息流到达前端，
// 前端从该块的 args.questions 解析并渲染问题卡片。
type AskUserQuestionTool struct{}

// Name 返回工具名称。
func (t *AskUserQuestionTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *AskUserQuestionTool) UsagePrompt() string { return "" }

// NewAskUserQuestionTool 创建用户提问工具。
func NewAskUserQuestionTool() agent.ToolExecutor {
	return &AskUserQuestionTool{}
}

func (t *AskUserQuestionTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "ask_user_question",
		Description: "当需要用户做出选择或澄清需求时，向用户提问。" +
			" 用于以下场景：" +
			" (1) 多个有效方案需要用户选择（如技术栈、架构方案）；" +
			" (2) 需求不明确需要澄清；" +
			" (3) 实现方式有取舍需要用户决策。" +
			" 提出 1-4 个问题，每个问题 2-4 个选项。" +
			" 问题应聚焦、具体，选项应互斥且有明确含义。" +
			" 对于需要视觉对比的选项（如布局、配色），可在选项中提供 preview 字段。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "要问的问题列表（1-4 个）",
					"minItems":    1,
					"maxItems":    4,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"question": map[string]any{
								"type":        "string",
								"description": "完整的问句。例如: 'How should I format the output?'",
							},
							"header": map[string]any{
								"type":        "string",
								"description": "短标签（最多12字符），用于 UI chip/tag。例如: 'Format'",
								"maxLength":   12,
							},
							"options": map[string]any{
								"type":        "array",
								"description": "2-4 个选项",
								"minItems":    2,
								"maxItems":    4,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label": map[string]any{
											"type":        "string",
											"description": "选项标签（简洁，1-5字）。例如: 'Summary'",
										},
										"description": map[string]any{
											"type":        "string",
											"description": "选项说明，解释含义或权衡。例如: 'Brief overview of key points'",
										},
										"preview": map[string]any{
											"type":        "string",
											"description": "可选预览内容（markdown 格式），用于视觉对比场景（如布局选择、配色方案等）。不需要视觉对比时可省略。",
										},
									},
									"required": []string{"label", "description"},
								},
							},
							"multi_select": map[string]any{
								"type":        "boolean",
								"description": "true 表示允许多选（默认 false 表示单选）",
							},
						},
						"required": []string{"question", "header", "options"},
					},
				},
			},
			"required": []string{"questions"},
		},
	}
}

// Execute 实现 agent.ToolExecutor 接口：置 user_wait 停止原因后立即返回，不阻塞
// 等待回答；tool_result 文本作为历史上下文，告知后续轮次的 LLM 已提问、等待用户
// 以普通消息形式回答。
func (t *AskUserQuestionTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	// tool_result 文本作为历史上下文（下一轮 LLM 可见）：陈述已提问并等待回答
	// 标记为 InternalTextType，前端过滤不显示
	writer.FullTextType(
		"已向用户提出问题，等待用户的回答。用户的回答将作为下一条消息到达；收到回答前不要替用户回答。", chat.InternalTextType)

	// 声明暂停：覆盖 runTool 预置的 ToolResult，请求会话主循环结束本轮
	// （不再携带 tool_result 回调 LLM），等待用户的回答作为下一条普通消息触发新一轮
	writer.StopReason(chat.StopReasonUserWait)
}
