package workflow

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/value"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
)

func TestNode(t *testing.T) {
	chatNode := node.NewChatNodeBuilder("chat").Build()
	wf := exec.NewBuilder("test", "测试").Nodes(chatNode).Build()
	executor := NewExecutor("111", wf)
	err := executor.Execute(value.NewObject(), exec.NewConfig())
	if err != nil {
		t.Error(err)
		return
	}
}
