package rest

import (
	"github.com/chuccp/go-agent-sdk/example/server"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/log"
	"github.com/chuccp/go-web-frame/web"
)

// FlowView workflow 的前端视图，仅暴露 id 和 name。
type FlowView struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

// Flow 注册 workflow 浏览相关的 REST 路由。
type Flow struct {
	agent *server.Agent
}

// Init 注册 flow 相关路由。
func (c *Flow) Init(ctx *core.Context) error {
	c.agent = core.GetRunner[*server.Agent](ctx)
	ctx.Get("/api/flows", c.listFlows)
	log.Info("Flow REST routes registered")
	return nil
}

// listFlows 返回所有已注册的 workflow（仅 id 和 name）。
func (c *Flow) listFlows(request *web.Request) (any, error) {
	workflows := c.agent.Workflows()
	views := make([]*FlowView, 0, len(workflows))
	for _, workflow := range workflows {
		views = append(views, &FlowView{
			Id:   workflow.Id,
			Name: workflow.Name,
		})
	}
	return web.Data(views), nil
}
