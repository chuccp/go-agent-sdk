package workflow

import (
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/value"
)

type Executor struct {
	Id       string
	workflow *Workflow
	Config   *exec.Config
}

func (e *Executor) Execute(rootValue *value.Object) {

}

func NewExecutor(id string, workflow *Workflow) *Executor {
	return &Executor{
		Id:       id,
		workflow: workflow,
	}
}
