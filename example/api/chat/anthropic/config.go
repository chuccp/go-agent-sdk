package anthropic

var TYPE = []string{"anthropic", "claude"}

type Config struct {
	BaseURL      string `mapstructure:"baseUrl" yaml:"baseUrl" json:"baseUrl"`
	APIKey       string `mapstructure:"apiKey" yaml:"apiKey" json:"apiKey"`
	Model        string `mapstructure:"model" yaml:"model" json:"model"`
	Thinking     bool   `mapstructure:"thinking" yaml:"thinking" json:"thinking"`
	ThinkingBudget int  `mapstructure:"thinkingBudget" yaml:"thinkingBudget" json:"thinkingBudget"`
}
