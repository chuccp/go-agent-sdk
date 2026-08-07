package node

type Node interface {
	GetName() string
}
type BaseNode struct {
	Name string
}
