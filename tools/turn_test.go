package tools

import (
	"context"
	"sync"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/value"
)

// testTurn 构造工具单测用的 Turn。
//
// Turn 只能经 agent.NewTurnWithContext 创建，且必须带上会话上下文——工具里的
// turn.Context()/turn.AgentContext() 都从它取。这里给的是真实的 RunContext（生产同款），
// 工具的 ctx 会直接递给 net/http、exec 这类地方，给 nil 就是一记 nil pointer dereference。
// 各用例只读 args、不落地会话状态，所以整个包共用一份上下文。
func testTurn(args *value.Object) *agent.Turn {
	return agent.NewTurnWithContext(testToolContext(), args)
}

// testToolContext 全局一份会话上下文：CreateServer 会起后台清理协程，
// 没必要每个用例各造一个。
var testToolContext = sync.OnceValue(func() *agent.RunContext {
	sctx := agent.NewConfig().CreateServer(context.Background()).SessionContext("tools-test")
	return agent.NewRunContext(sctx, sctx.AgentStore())
})
