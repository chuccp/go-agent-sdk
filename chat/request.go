package chat

// ThinkingConfig 控制模型的扩展思考（extended thinking）行为。
// 与 Anthropic Messages API 的 thinking 字段对齐。
type ThinkingConfig struct {
	Type         string `json:"type"                    `  // "enabled" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"  ` // 思考链最大 token 预算（仅 enabled 时有效）
}

// Request 是发给 LLM Messages API 的完整请求体。
type Request struct {
	Model     string    `json:"model"`      // 模型 SessionId，如 "claude-opus-4-8"
	MaxTokens int       `json:"max_tokens"` // 最大生成 token 数
	Messages  []Message `json:"messages"`   // 对话历史（user/assistant 交替）
	// 可选字段
	System        string          `json:"system,omitempty"`         // 系统提示（独立于 messages）
	Tools         []ToolFunction  `json:"tools,omitempty"`          // 可用工具列表
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`       // 扩展思考配置
	Stream        bool            `json:"stream,omitempty"`         // 是否流式返回
	Temperature   *float64        `json:"temperature,omitempty"`    // 采样温度 (0,1]
	TopP          *float64        `json:"top_p,omitempty"`          // nucleus 采样
	TopK          *int            `json:"top_k,omitempty"`          // top-k 采样
	StopSequences []string        `json:"stop_sequences,omitempty"` // 停止序列
	Metadata      map[string]any  `json:"metadata,omitempty"`       // 自定义元数据（不透传给模型）
}
