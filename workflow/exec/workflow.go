package exec

import "github.com/chuccp/go-agent-sdk/workflow/node"

type Workflow struct {
	nodes []node.Node
	Id    string
	Name  string
}

func (w *Workflow) Exec(context *Context) error {
	for _, node := range w.nodes {
		err := node.Exec(context)
		if err != nil {
			return err
		}
	}
	return nil
}

type Builder struct {
	workflow *Workflow
}

func (b *Builder) Nodes(node ...node.Node) {
	b.workflow.nodes = append(b.workflow.nodes, node...)
}
func (b *Builder) Build() *Workflow {
	return b.workflow
}
func NewBuilder(Id string, Name string) *Builder {
	return &Builder{
		workflow: &Workflow{
			Id:    Id,
			Name:  Name,
			nodes: make([]node.Node, 0),
		},
	}
}
