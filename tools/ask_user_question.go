package tools

import (
	"encoding/json"
	"fmt"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

type Question struct {
	Question    string   `json:"question"`               // 完整问题文本
	Header      string   `json:"header"`                 // 短标签（≤12 字符）
	Options     []Option `json:"options"`                // 2-4 个选项
	MultiSelect bool     `json:"multi_select,omitempty"` // 是否允许多选
}

// Option 是问题的一个选项。
type Option struct {
	Label       string `json:"label"`             // 选项标签
	Description string `json:"description"`       // 选项说明
	Preview     string `json:"preview,omitempty"` // 可选预览内容（markdown/html），用于视觉对比
}

const AskUserBlockType chat.BlockType = "ask_user" // LLM 向用户提问，需要用户交互后继续

type AskUserBlock struct {
	chat.Block
	Text string `json:"text"`
}

func (a *AskUserBlock) ForContext() bool {
	return false
}
func (a *AskUserBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: string(AskUserBlockType), Text: a.Text})
}
func NewAskUserBlock(Text string) *AskUserBlock {
	return &AskUserBlock{
		Text: Text,
	}
}

// NewAskUserEvent 创建一个用户提问事件，content 为问题列表的 JSON。
//func NewAskUserEvent(content string) *chat.ClientEvent {
//	return &chat.ClientEvent{EventSource: chat.SourceAI, EventType: EventTypeAskUser, Content: content}
//}

// AskUserQuestionTool 让 LLM 在执行过程中向用户提出澄清问题。
// 实现 agent.ToolExecutor 接口：执行时向前端推送 ask_user 事件并置 user_wait
// 停止原因后立即返回（不阻塞）。user_wait 使会话主循环跳过本轮的 LLM 收尾调用、
// 直接结束本轮；用户的回答作为下一条普通消息进入会话，触发新一轮。
//
// 推送的事件由工具自身配置：默认事件构造器为 NewAskUserEvent，
// 可通过 WithAskUserEventType / WithAskUserEventFactory 按实例定制。
type AskUserQuestionTool struct {
	//newEvent func(content string) *chat.ClientEvent // 事件构造器，content 为问题列表 JSON
}

// Name 返回工具名称。
func (t *AskUserQuestionTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *AskUserQuestionTool) UsagePrompt() string { return "" }

// NewAskUserQuestionTool 创建用户提问工具，可选配置推送事件的类型或构造器。
func NewAskUserQuestionTool() agent.ToolExecutor {
	t := &AskUserQuestionTool{}
	return t
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

// Execute 实现 agent.ToolExecutor 接口：向前端推送问题事件（content 为问题列表 JSON）
// 并置 user_wait 停止原因后立即返回，不阻塞等待回答；tool_result 文本作为历史上下文，
// 告知后续轮次的 LLM 已提问、等待用户以普通消息形式回答；错误经 WriteErrorText 以文本写入。
func (t *AskUserQuestionTool) Execute(turn *agent.Turn, writer *chat.BlockStream) {
	ctx := turn.Context()

	questions, err := parseQuestions(turn.Args())
	if err != nil {
		writer.ErrorText(err)
		return
	}

	// 1. 向前端推送问题事件（content 为问题列表 JSON）
	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		writer.ErrorText(fmt.Errorf("序列化问题失败: %w", err))
		return
	}
	ctx.AddBlock(NewAskUserBlock(string(questionsJSON)))

	// 2. 声明暂停：覆盖 runTool 预置的 ToolResult，请求会话主循环结束本轮
	//    （不再携带 tool_result 回调 LLM），等待用户的回答作为下一条普通消息触发新一轮
	writer.StopReason(chat.StopReasonUserWait)

	// 3. tool_result 文本作为历史上下文（下一轮 LLM 可见）：陈述已提问并等待回答
	writer.Block(chat.NewFullTextBlock(
		"已向用户提出问题，等待用户的回答。用户的回答将作为下一条消息到达；收到回答前不要替用户回答。"))
}

// parseQuestions 从 LLM 传入的 args 中解析问题列表。
func parseQuestions(args *value.Object) ([]Question, error) {
	if !args.HasKey("questions") {
		return nil, fmt.Errorf("缺少 questions 参数")
	}
	arr := args.GetArray("questions")
	if arr == nil {
		return nil, fmt.Errorf("questions 必须是数组")
	}

	n := arr.Len()
	if n == 0 {
		return nil, fmt.Errorf("至少需要 1 个问题")
	}
	if n > 4 {
		return nil, fmt.Errorf("最多支持 4 个问题，收到 %d 个", n)
	}

	questions := make([]Question, 0, n)
	var parseErr error
	arr.ForEach(func(i int, v value.Value) bool {
		if !v.IsObject() {
			parseErr = fmt.Errorf("questions[%d] 必须是对象", i)
			return false
		}
		obj := v.AsObject()

		q := Question{
			Question:    obj.GetString("question"),
			Header:      obj.GetString("header"),
			MultiSelect: obj.GetBool("multi_select"),
		}

		if q.Question == "" {
			parseErr = fmt.Errorf("questions[%d].question 不能为空", i)
			return false
		}

		opts, err := parseOptions(obj, i)
		if err != nil {
			parseErr = err
			return false
		}
		q.Options = opts

		questions = append(questions, q)
		return true
	})
	if parseErr != nil {
		return nil, parseErr
	}

	return questions, nil
}

// parseOptions 解析问题的选项列表。
func parseOptions(obj *value.Object, qi int) ([]Option, error) {
	if !obj.HasKey("options") {
		return nil, fmt.Errorf("questions[%d].options 缺失", qi)
	}
	arr := obj.GetArray("options")
	if arr == nil {
		return nil, fmt.Errorf("questions[%d].options 必须是数组", qi)
	}

	n := arr.Len()
	if n < 2 {
		return nil, fmt.Errorf("questions[%d].options 至少需要 2 个选项", qi)
	}
	if n > 4 {
		return nil, fmt.Errorf("questions[%d].options 最多支持 4 个选项", qi)
	}

	opts := make([]Option, 0, n)
	var parseErr error
	arr.ForEach(func(j int, v value.Value) bool {
		if !v.IsObject() {
			parseErr = fmt.Errorf("questions[%d].options[%d] 必须是对象", qi, j)
			return false
		}
		optObj := v.AsObject()

		opt := Option{
			Label:       optObj.GetString("label"),
			Description: optObj.GetString("description"),
			Preview:     optObj.GetString("preview"),
		}
		if opt.Label == "" {
			parseErr = fmt.Errorf("questions[%d].options[%d].label 不能为空", qi, j)
			return false
		}

		opts = append(opts, opt)
		return true
	})
	if parseErr != nil {
		return nil, parseErr
	}

	return opts, nil
}
