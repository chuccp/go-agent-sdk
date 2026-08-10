package exec

import "github.com/chuccp/go-agent-sdk/workflow/node"

type Workflow struct {
	nodes []node.Node
}

func Of(nodes ...node.Node) *Workflow {
	return &Workflow{nodes: nodes}
}

type Builder struct {
	nodes []node.Node
}

func (b *Builder) AddNode(nodes ...node.Node) {
	b.nodes = append(b.nodes, nodes...)
}
func (b *Builder) Build() *Workflow {
	return &Workflow{
		nodes: b.nodes,
	}
}
func NewBuilder() *Builder {
	return &Builder{
		nodes: make([]node.Node, 0),
	}
}
