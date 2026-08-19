package chat

// ThinkingLevel 控制模型扩展思考（extended thinking）的强度级别。
type ThinkingLevel string

const (
	ThinkingOff    ThinkingLevel = "off"    // 关闭扩展思考
	ThinkingLow    ThinkingLevel = "low"    // 低级别思考（budget 8192 tokens）
	ThinkingMedium ThinkingLevel = "medium" // 中级别思考（budget 16384 tokens）
	ThinkingHigh   ThinkingLevel = "high"   // 高级别思考（budget 32768 tokens）
)

// thinkingBudget 各级别对应的 token 预算。
var thinkingBudget = map[ThinkingLevel]int{
	ThinkingLow:    8192,
	ThinkingMedium: 16384,
	ThinkingHigh:   32768,
}

// ToThinkingConfig 将 ThinkingLevel 转换为协议层的 ThinkingConfig。
// 返回 nil 表示未配置（不发送 thinking 字段）。
func (l ThinkingLevel) ToThinkingConfig() *ThinkingConfig {
	if l == "" || l == ThinkingOff {
		if l == ThinkingOff {
			return &ThinkingConfig{Type: "disabled"}
		}
		return nil
	}
	budget, ok := thinkingBudget[l]
	if !ok {
		return nil
	}
	return &ThinkingConfig{Type: "enabled", BudgetTokens: budget}
}

// Options 保存 LLM 请求参数，应用于 ChatManager 创建的所有会话。
type Options struct {
	Model         string
	MaxTokens     int
	MaxContext    int // 最大上下文消息条数，超出时截断早期历史（0 表示不限制）
	Temperature   *float64
	TopP          *float64
	TopK          *int
	StopSequences []string
	Stream        bool
	Thinking      ThinkingLevel // 扩展思考级别：off / low / medium / high
	SystemPrompt  string
}

// DefaultOptions 返回默认配置。
func DefaultOptions() *Options {
	return &Options{
		Stream: true,
	}
}

// Option 是配置 ChatManager 的函数式选项。
type Option func(*Options)

// WithModel 设置 LLM 请求的模型名称。
func WithModel(model string) Option {
	return func(o *Options) { o.Model = model }
}

// WithSystemPrompt 设置 LLM 请求的模型名称。
func WithSystemPrompt(systemPrompt string) Option {
	return func(o *Options) { o.SystemPrompt = systemPrompt }
}

// WithMaxTokens 设置最大生成 token 数。
func WithMaxTokens(maxTokens int) Option {
	return func(o *Options) { o.MaxTokens = maxTokens }
}

// WithMaxContext 设置最大上下文消息条数。
// 当历史记录超过此限制时，仅保留最近的 N 条消息发送给模型（0 表示不限制）。
func WithMaxContext(maxContext int) Option {
	return func(o *Options) { o.MaxContext = maxContext }
}

// WithTemperature 设置采样温度 (0,1]。
func WithTemperature(temp float64) Option {
	return func(o *Options) { o.Temperature = &temp }
}

// WithTopP 设置 nucleus 采样参数。
func WithTopP(topP float64) Option {
	return func(o *Options) { o.TopP = &topP }
}

// WithTopK 设置 top-k 采样参数。
func WithTopK(topK int) Option {
	return func(o *Options) { o.TopK = &topK }
}

// WithStopSequences 设置停止序列。
func WithStopSequences(seqs ...string) Option {
	return func(o *Options) { o.StopSequences = seqs }
}

// WithStream 启用或禁用流式模式（默认 true）。
func WithStream(stream bool) Option {
	return func(o *Options) { o.Stream = stream }
}

// WithThinking 设置扩展思考级别（off / low / medium / high）。
// 默认不设置（不发送 thinking 字段，由模型提供方决定）。
func WithThinking(level ThinkingLevel) Option {
	return func(o *Options) { o.Thinking = level }
}
