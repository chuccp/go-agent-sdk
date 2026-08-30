package chat

import "github.com/chuccp/go-agent-sdk/value"

// ThinkingLevel 控制模型扩展思考（extended thinking）的强度级别。
type ThinkingLevel string

const (
	ThinkingOff    ThinkingLevel = "off"    // 关闭扩展思考
	ThinkingLow    ThinkingLevel = "low"    // 低级别思考（budget 8192 tokens）
	ThinkingMedium ThinkingLevel = "medium" // 中级别思考（budget 16384 tokens）
	ThinkingHigh   ThinkingLevel = "high"   // 高级别思考（budget 32768 tokens）
)

type ConfigKey string

const (
	IDConfigKey               ConfigKey = "id"
	ModelConfigKey            ConfigKey = "model"
	MaxOutputTokensConfigKey  ConfigKey = "max_output_tokens"
	MaxContextTokensConfigKey ConfigKey = "max_context_tokens"
	KeepRecentTokensConfigKey ConfigKey = "keep_recent_tokens"
	ThinkingConfigKey         ConfigKey = "thinking"
	SystemPromptConfigKey     ConfigKey = "system_prompt"
	BaseURLConfigKey          ConfigKey = "baseUrl"
	APIKEYConfigKey           ConfigKey = "apikey"
)

type Config struct {
	object *value.Object
}

func (m *Config) ForEach(fn func(key string, value value.Value) bool) {
	m.object.ForEach(fn)
}

func (m *Config) Merge(configs ...*Config) {
	if configs != nil {
		for _, configItem := range configs {
			configItem.ForEach(func(key string, value value.Value) bool {
				m.object.PutAny(key, value)
				return true
			})
		}

	}
}

func (m *Config) Option(opt ...Option) {
	for _, o := range opt {
		o(m)
	}
}

func (m *Config) Set(key ConfigKey, value any) {
	m.object.PutAny(string(key), value)
}

func (m *Config) GetSystemPrompt() string {
	return m.object.GetString(string(SystemPromptConfigKey))
}
func (m *Config) SetSystemPrompt(systemPrompt string) {
	m.object.PutAny(string(SystemPromptConfigKey), systemPrompt)
}
func (m *Config) GetID() string {
	return m.object.GetString(string(IDConfigKey))
}
func (m *Config) GetModel() string {
	return m.object.GetString(string(ModelConfigKey))
}
func (m *Config) GetMaxTokens() int {
	return m.object.GetInt(string(MaxOutputTokensConfigKey))
}
func (m *Config) GetThinking() ThinkingLevel {
	return ThinkingLevel(m.object.GetString(string(ThinkingConfigKey)))
}
func Combine(Configs ...*Config) *Config {
	config := DefaultConfig()
	for _, cfg := range Configs {
		config.Merge(cfg)
	}
	return config
}
func DefaultConfig() *Config {
	return &Config{
		object: value.NewObject(),
	}
}

type Option func(*Config)

// WithModel 设置 LLM 请求的模型名称。
func WithModel(model string) Option {
	return func(o *Config) { o.Set(ModelConfigKey, model) }
}

func WithId(id string) Option {
	return func(o *Config) { o.Set(IDConfigKey, id) }
}

// WithSystemPrompt 设置 LLM 请求的系统提示。
func WithSystemPrompt(systemPrompt string) Option {
	return func(o *Config) {
		o.Set(SystemPromptConfigKey, systemPrompt)
	}
}

// WithMaxTokens 设置最大生成 token 数。
func WithMaxTokens(maxTokens int) Option {
	return func(o *Config) {
		o.Set(MaxOutputTokensConfigKey, maxTokens)
	}
}

// WithThinking 设置扩展思考级别（off / low / medium / high）。
// 默认不设置（不发送 thinking 字段，由模型提供方决定）。
func WithThinking(level ThinkingLevel) Option {
	return func(o *Config) {
		o.Set(ThinkingConfigKey, level)
	}
}
