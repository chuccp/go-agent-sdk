package workflow

import (
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/value"
)

type Executor struct {
	Id       string
	workflow *exec.Workflow
	Config   *exec.Config
}

func (e *Executor) Execute(rootValue *value.Object, config *exec.Config) error {
	executor := exec.NewExecutor(e.Id, rootValue, config, e.workflow)
	return executor.Exec()
}

func NewExecutor(executorId string, workflow *exec.Workflow) *Executor {
	return &Executor{
		Id:       executorId,
		workflow: workflow,
	}
}
