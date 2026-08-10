package node

type Node interface {
	GetName() string
	GetId() string
	Exec(context WorkflowContext) error
}
