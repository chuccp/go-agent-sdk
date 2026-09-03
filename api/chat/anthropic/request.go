package anthropic

import "github.com/chuccp/go-agent-sdk/chat"

// ThinkingConfig 控制模型的扩展思考（extended thinking）行为。
// 与 Anthropic Messages API 的 thinking 字段对齐。
type ThinkingConfig struct {
	Type         string `json:"type"                    `  // "enabled" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"  ` // 思考链最大 token 预算（仅 enabled 时有效）
}

// SystemBlock 是 system 提示的内容块。Anthropic 要求 system 携带缓存断点时
// 必须用内容块数组（而非字符串）表达。
type SystemBlock struct {
	Type         string             `json:"type"` // 固定 "text"
	Text         string             `json:"text"`
	CacheControl *chat.CacheControl `json:"cache_control,omitempty"`
}

// Request 是发给 LLM Messages API 的完整请求体。
type Request struct {
	Model     string          `json:"model"`      // 模型 ID，如 "claude-opus-4-8"
	MaxTokens int             `json:"max_tokens"` // 最大生成 token 数
	Messages  []*chat.Message `json:"messages"`   // 对话历史（user/assistant 交替）
	// 可选字段
	System        []SystemBlock       `json:"system,omitempty"`         // 系统提示（带缓存断点的内容块）
	Tools         []chat.ToolFunction `json:"tools,omitempty"`          // 可用工具列表
	Thinking      *ThinkingConfig     `json:"thinking,omitempty"`       // 扩展思考配置
	Stream        bool                `json:"stream,omitempty"`         // 是否流式返回
	Temperature   *float64            `json:"temperature,omitempty"`    // 采样温度 (0,1]
	TopP          *float64            `json:"top_p,omitempty"`          // nucleus 采样
	TopK          *int                `json:"top_k,omitempty"`          // top-k 采样
	StopSequences []string            `json:"stop_sequences,omitempty"` // 停止序列
	Metadata      map[string]any      `json:"metadata,omitempty"`       // 自定义元数据（不透传给模型）
}

// thinkingBudget 各级别思考对应的 token 预算。
var thinkingBudget = map[chat.ThinkingLevel]int{
	chat.ThinkingLow:    8192,
	chat.ThinkingMedium: 16384,
	chat.ThinkingHigh:   32768,
}

// defaultMaxTokens 未显式配置 max_tokens 时的默认值（Anthropic 要求该字段必填）。
const defaultMaxTokens = 4096

// toThinkingConfig 将 chat.ThinkingLevel 转换为协议层的 ThinkingConfig。
// 返回 nil 表示未配置（不发送 thinking 字段）。
func toThinkingConfig(level chat.ThinkingLevel) *ThinkingConfig {
	if level == "" {
		return nil
	}
	if level == chat.ThinkingOff {
		return &ThinkingConfig{Type: "disabled"}
	}
	budget, ok := thinkingBudget[level]
	if !ok {
		return nil
	}
	return &ThinkingConfig{Type: "enabled", BudgetTokens: budget}
}

// NewRequest 根据对话历史与请求配置组装发给 Messages API 的完整请求体。
func NewRequest(chatMessages *chat.Messages, config *chat.Config) *Request {
	request := &Request{
		// 服务端始终以 SSE 流式解析响应，故固定开启 stream。
		Stream: true,
	}
	if config != nil {
		request.Model = config.GetModel()
		request.MaxTokens = config.GetMaxTokens()
		request.System = newSystemBlocks(config.GetSystemPrompt())
		request.Thinking = toThinkingConfig(config.GetThinking())
	}
	if request.MaxTokens == 0 {
		request.MaxTokens = defaultMaxTokens
	}
	if chatMessages != nil {
		request.Messages = make([]*chat.Message, 0, len(chatMessages.Messages))
		for i := range chatMessages.Messages {
			request.Messages = append(request.Messages, &chatMessages.Messages[i])
		}
		if len(chatMessages.Tools) > 0 {
			request.Tools = make([]chat.ToolFunction, len(chatMessages.Tools))
			copy(request.Tools, chatMessages.Tools)
			for i := range request.Tools {
				request.Tools[i].CacheControl = &chat.CacheControl{Type: "ephemeral"}
			}
		}
	}
	return request
}

// newSystemBlocks 把系统提示组装为带缓存断点的 system 内容块；空提示返回 nil。
func newSystemBlocks(systemPrompt string) []SystemBlock {
	if systemPrompt == "" {
		return nil
	}
	return []SystemBlock{{
		Type:         "text",
		Text:         systemPrompt,
		CacheControl: &chat.CacheControl{Type: "ephemeral"},
	}}
}
