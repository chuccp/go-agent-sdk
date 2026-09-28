package exec

import (
	"github.com/chuccp/go-agent-sdk/jsonx"
)

type Config struct {
	parameter *jsonx.Object
}

func NewConfig() *Config {
	return &Config{
		parameter: jsonx.NewObject(),
	}
}
