package exec

import "github.com/chuccp/go-agent-sdk/workflow/node"

type Workflow struct {
	nodes       []node.Node
	Steps       []*Step
	Id          string
	Name        string
	Description string
	InputSchema map[string]any
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

func (b *Builder) Nodes(node ...node.Node) *Builder {
	b.workflow.nodes = append(b.workflow.nodes, node...)
	return b
}

// Steps 添加剧本步骤（Talk/Exec）。
func (b *Builder) Steps(steps ...*Step) *Builder {
	b.workflow.Steps = append(b.workflow.Steps, steps...)
	return b
}

// Description 设置 flow 用途描述（写进工具 definition 与卡片）。
func (b *Builder) Description(description string) *Builder {
	b.workflow.Description = description
	return b
}

// InputSchema 设置 flow 入参的 JSON Schema（写进工具 definition）。
func (b *Builder) InputSchema(schema map[string]any) *Builder {
	b.workflow.InputSchema = schema
	return b
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
			Steps: make([]*Step, 0),
		},
	}
}
