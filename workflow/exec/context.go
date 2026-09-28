package exec

import (
	"github.com/chuccp/go-agent-sdk/jsonx"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

type Context struct {
	executorId string
	config     *Config
	rootValue  *jsonx.Object
	nodes      []node.Node
}

func (c *Context) ExecutorId() string {
	return c.executorId
}
