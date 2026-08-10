package workflow

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
	"github.com/chuccp/go-agent-sdk/workflow/value"
)

func TestNode(t *testing.T) {

	chat := node.NewChatNodeBuilder("chat").Build()
	workflow := exec.Of(chat)
	executor := NewExecutor("111", workflow)
	err := executor.Execute(value.NewObject(), exec.NewConfig())
	if err != nil {
		t.Error(err)
		return
	}

}
