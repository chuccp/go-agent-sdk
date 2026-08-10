package exec

import (
	"github.com/chuccp/go-agent-sdk/workflow/value"
)

type Executor struct {
	workflow  *Workflow
	rootValue *value.Object
	config    *Config
	execId    string
}

func (e *Executor) Exec() error {
	context := &Context{
		executorId: e.execId,
		config:     e.config,
		nodes:      e.workflow.nodes,
		rootValue:  e.rootValue,
	}
	for _, n := range e.workflow.nodes {
		err := n.Exec(context)
		if err != nil {
			return err
		}
	}
	return nil
}
func NewExecutor(executorId string, rootValue *value.Object, config *Config, workflow *Workflow) *Executor {
	return &Executor{
		execId:    executorId,
		rootValue: rootValue,
		workflow:  workflow,
		config:    config,
	}
}
