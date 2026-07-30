package agent

// Options 保存 LLM 请求参数，应用于该 ChatManager 创建的所有会话。
type Options struct {
	Model         string
	MaxTokens     int
	Temperature   *float64
	TopP          *float64
	TopK          *int
	StopSequences []string
	Stream        bool
}

// defaultOptions 返回默认配置。
func defaultOptions() *Options {
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

// WithMaxTokens 设置最大生成 token 数。
func WithMaxTokens(maxTokens int) Option {
	return func(o *Options) { o.MaxTokens = maxTokens }
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
