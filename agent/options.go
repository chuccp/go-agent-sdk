package agent

// Options holds LLM request parameters that are applied to every Messages sent
// by sessions created from this ChatManager.
type Options struct {
	Model         string
	MaxTokens     int
	Temperature   *float64
	TopP          *float64
	TopK          *int
	StopSequences []string
	Stream        bool
}

// defaultOptions returns the default configuration.
func defaultOptions() *Options {
	return &Options{
		Stream: true,
	}
}

// Option is a functional option for configuring ChatManager.
type Option func(*Options)

// WithModel sets the model name for LLM requests.
func WithModel(model string) Option {
	return func(o *Options) { o.Model = model }
}

// WithMaxTokens sets the max_tokens for LLM requests.
func WithMaxTokens(maxTokens int) Option {
	return func(o *Options) { o.MaxTokens = maxTokens }
}

// WithTemperature sets the sampling temperature (0,1].
func WithTemperature(temp float64) Option {
	return func(o *Options) { o.Temperature = &temp }
}

// WithTopP sets the nucleus sampling parameter.
func WithTopP(topP float64) Option {
	return func(o *Options) { o.TopP = &topP }
}

// WithTopK sets the top-k sampling parameter.
func WithTopK(topK int) Option {
	return func(o *Options) { o.TopK = &topK }
}

// WithStopSequences sets the stop sequences.
func WithStopSequences(seqs ...string) Option {
	return func(o *Options) { o.StopSequences = seqs }
}

// WithStream enables or disables streaming mode (default true).
func WithStream(stream bool) Option {
	return func(o *Options) { o.Stream = stream }
}
