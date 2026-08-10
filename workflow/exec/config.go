package exec

import "github.com/chuccp/go-agent-sdk/workflow/value"

type Config struct {
	parameter *value.Object
}

func NewConfig() *Config {
	return &Config{
		parameter: value.NewObject(),
	}
}
