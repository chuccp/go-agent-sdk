package exec

import (
	"github.com/chuccp/go-agent-sdk/workflow/node"
	"github.com/chuccp/go-agent-sdk/workflow/value"
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
