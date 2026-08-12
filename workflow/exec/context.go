package exec

import (
	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

type Context struct {
	executorId string
	config     *Config
	rootValue  *value.Object
	nodes      []node.Node
}

func (c *Context) ExecutorId() string {
	return c.executorId
}
