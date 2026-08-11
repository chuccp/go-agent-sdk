package exec

import "github.com/chuccp/go-agent-sdk/workflow/node"

// StepKind 步骤类型。
type StepKind string

const (
	// StepTalk 对话步骤：主 LLM 按剧本指引在对话中完成（确认/澄清/交付）。
	StepTalk StepKind = "talk"
	// StepExec 执行步骤：绑定节点，经 exec_node 工具零上下文执行。
	StepExec StepKind = "exec"
)

// Step 是 flow 剧本中的一个步骤。
type Step struct {
	name   string
	kind   StepKind
	title  string
	script string

	// Talk 步骤：声明式完成判定——doneWhen 中的键全部出现在 input 即自动完成
	doneWhen []string

	// Exec 步骤
	chatNode   *node.ChatNode
	iterate    string // 迭代源：先查 input[key]，再查 outputs[key]（支持 "step.field" 路径）
	prevWindow int    // {{prev}} 滑动窗口（尾部字数），0 表示给全量上一项输出
}

// Talk 创建对话步骤（链上继续配置 DoneWhen）。
func Talk(name, title, script string) *Step {
	return &Step{name: name, kind: StepTalk, title: title, script: script}
}

// Exec 创建执行步骤（链上继续配置 Iterate/PrevWindow）。
func Exec(name string, chatNode *node.ChatNode) *Step {
	return &Step{name: name, kind: StepExec, title: name, chatNode: chatNode}
}

// DoneWhen 声明 Talk 步骤的完成条件：这些键全部登记进 input 即自动完成。
func (s *Step) DoneWhen(keys ...string) *Step {
	s.doneWhen = append(s.doneWhen, keys...)
	return s
}

// Iterate 将执行步骤声明为迭代型：对数组源逐项执行节点。
func (s *Step) Iterate(source string) *Step {
	s.iterate = source
	return s
}

// PrevWindow 设置 {{prev}} 滑动窗口大小（取上一项输出的尾部 n 字）。
func (s *Step) PrevWindow(n int) *Step {
	s.prevWindow = n
	return s
}

func (s *Step) Name() string             { return s.name }
func (s *Step) Kind() StepKind           { return s.kind }
func (s *Step) Title() string            { return s.title }
func (s *Step) Script() string           { return s.script }
func (s *Step) DoneWhenKeys() []string   { return s.doneWhen }
func (s *Step) Node() *node.ChatNode     { return s.chatNode }
func (s *Step) IterateSource() string    { return s.iterate }
func (s *Step) PrevWindowSize() int      { return s.prevWindow }
func (s *Step) IsIterating() bool        { return s.iterate != "" }
