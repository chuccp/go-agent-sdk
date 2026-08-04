package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
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

// AskUserQuestionResponse 是工具返回给 LLM 的答案结构。
// 对齐文档中的响应格式: { questions, answers, response? }
type AskUserQuestionResponse struct {
	Questions []Question        `json:"questions"`
	Answers   map[string]string `json:"answers"`
	Response  string            `json:"response,omitempty"` // 用户自由格式回复（非问题特定的）
}

// AnswerHandler 是处理用户问题的回调。
type AnswerHandler func(questions []Question) (answers map[string]string, response string, err error)

// AskUserQuestionTool 让 LLM 在执行过程中向用户提出澄清问题。
// 实现 ToolExecutor 接口。需要注入 AnswerHandler 来处理实际用户交互。
type AskUserQuestionTool struct {
}

// NewAskUserQuestionTool 创建用户提问工具。
// handler 负责将问题呈现给用户并收集答案。
// 传 nil 表示无交互环境，工具会返回错误。
func NewAskUserQuestionTool() ToolExecutor {
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

func (t *AskUserQuestionTool) Execute(args map[string]any) (string, error) {
	//if t.handler == nil {
	//	return "", fmt.Errorf("ask_user_question: 当前环境不支持交互式提问（AnswerHandler 未设置）")
	//}

	//questions, err := parseQuestions(args)
	//if err != nil {
	//	return "", err
	//}

	//answers, response, err := t.handler(questions)
	//if err != nil {
	//	return "", fmt.Errorf("获取用户答案失败: %w", err)
	//}

	//return formatResponse(questions, answers, response)
	return "", nil
}

// parseQuestions 从 LLM 传入的 args 中解析问题列表。
func parseQuestions(args map[string]any) ([]Question, error) {
	raw, ok := args["questions"]
	if !ok {
		return nil, fmt.Errorf("缺少 questions 参数")
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("questions 必须是数组")
	}

	if len(arr) == 0 {
		return nil, fmt.Errorf("至少需要 1 个问题")
	}
	if len(arr) > 4 {
		return nil, fmt.Errorf("最多支持 4 个问题，收到 %d 个", len(arr))
	}

	questions := make([]Question, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions[%d] 必须是对象", i)
		}

		q := Question{
			Question:    getString(obj, "question"),
			Header:      getString(obj, "header"),
			MultiSelect: getBool(obj, "multi_select"),
		}

		if q.Question == "" {
			return nil, fmt.Errorf("questions[%d].question 不能为空", i)
		}

		opts, err := parseOptions(obj, i)
		if err != nil {
			return nil, err
		}
		q.Options = opts

		questions = append(questions, q)
	}

	return questions, nil
}

// parseOptions 解析问题的选项列表。
func parseOptions(obj map[string]any, qi int) ([]Option, error) {
	raw, ok := obj["options"]
	if !ok {
		return nil, fmt.Errorf("questions[%d].options 缺失", qi)
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("questions[%d].options 必须是数组", qi)
	}

	if len(arr) < 2 {
		return nil, fmt.Errorf("questions[%d].options 至少需要 2 个选项", qi)
	}
	if len(arr) > 4 {
		return nil, fmt.Errorf("questions[%d].options 最多支持 4 个选项", qi)
	}

	opts := make([]Option, 0, len(arr))
	for j, item := range arr {
		optObj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions[%d].options[%d] 必须是对象", qi, j)
		}

		opt := Option{
			Label:       getString(optObj, "label"),
			Description: getString(optObj, "description"),
			Preview:     getString(optObj, "preview"),
		}
		if opt.Label == "" {
			return nil, fmt.Errorf("questions[%d].options[%d].label 不能为空", qi, j)
		}

		opts = append(opts, opt)
	}

	return opts, nil
}

// formatResponse 将问题和答案按文档格式序列化为 JSON 返回给 LLM。
// 格式: { "questions": [...], "answers": { "q": "a" }, "response": "..." }
func formatResponse(questions []Question, answers map[string]string, response string) (string, error) {
	if len(answers) == 0 && response == "" {
		return "", fmt.Errorf("用户未回答任何问题")
	}

	resp := AskUserQuestionResponse{
		Questions: questions,
		Answers:   answers,
		Response:  response,
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("序列化答案失败: %w", err)
	}

	// 同时返回人类可读和 JSON（LLM 可以用 JSON 精确解析）
	var sb strings.Builder
	if response != "" {
		sb.WriteString(fmt.Sprintf("用户回复: %s\n", response))
	}
	for _, q := range questions {
		if ans, ok := answers[q.Question]; ok && ans != "" {
			sb.WriteString(fmt.Sprintf("- %s → %s\n", q.Question, ans))
		}
	}
	sb.WriteString(fmt.Sprintf("\n%s", string(b)))

	return sb.String(), nil
}

// ==================== 辅助函数 ====================

func getString(obj map[string]any, key string) string {
	if v, ok := obj[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getBool(obj map[string]any, key string) bool {
	if v, ok := obj[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
