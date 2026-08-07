package node

import "github.com/chuccp/go-agent-sdk/workflow/exec"

type Node interface {
	GetName() string
	Exec(context *exec.Context) error
}
