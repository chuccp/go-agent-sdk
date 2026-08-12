package exec

import (
	"github.com/chuccp/go-agent-sdk/value"
)

type Config struct {
	parameter *value.Object
}

func NewConfig() *Config {
	return &Config{
		parameter: value.NewObject(),
	}
}
