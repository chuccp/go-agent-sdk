package flow

import (
	"github.com/chuccp/go-agent-sdk/workflow/exec"
	"github.com/chuccp/go-agent-sdk/workflow/node"
	"github.com/chuccp/go-web-frame/core"
)

type StoreFlow struct {
	ctx *core.Context
}

func (s *StoreFlow) Init(ctx *core.Context) error {
	s.ctx = ctx
	return nil
}
func (s *StoreFlow) GetFlow() *exec.Workflow {
	storyNode := node.NewChatNodeBuilder("story").Build()
	workflow := exec.NewBuilder("story003", "故事生成").Nodes(storyNode).Build()
	return workflow
}
