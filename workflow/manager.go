package workflow

import "github.com/chuccp/go-agent-sdk/workflow/exec"

type Manager struct {
	workflows []*exec.Workflow
}

func (m *Manager) AddWorkflow(workflows ...*exec.Workflow) {
	m.workflows = append(m.workflows, workflows...)
}
func NewManager() *Manager {
	return &Manager{
		workflows: make([]*exec.Workflow, 0),
	}
}
