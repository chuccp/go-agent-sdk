package workflow

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/workflow/exec"
)

type Manager struct {
	lock      *sync.RWMutex
	workflows []*exec.Workflow
}

func (m *Manager) AddWorkflow(workflows ...*exec.Workflow) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.workflows = append(m.workflows, workflows...)
}
func (m *Manager) Workflows() []*exec.Workflow {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return m.workflows
}
func NewManager() *Manager {
	return &Manager{
		lock:      new(sync.RWMutex),
		workflows: make([]*exec.Workflow, 0),
	}
}
