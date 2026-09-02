package server

import (
	"encoding/json"
	"sync"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/api/chat/anthropic"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-agent-sdk/example/flow"
	"github.com/chuccp/go-agent-sdk/example/service"
	"github.com/chuccp/go-agent-sdk/tools"
	"github.com/chuccp/go-agent-sdk/workflow"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/log"
	"github.com/chuccp/go-web-frame/util"
	"github.com/spf13/cast"
	"go.uber.org/zap"
)

type Agent struct {
	core.IRunner
	ctx                *core.Context
	agentManager       *agent.Agent
	lock               sync.RWMutex
	chatSessionService *service.ChatSessionService
	storeFlow          *flow.StoreFlow
}

func (r *Agent) Init(ctx *core.Context) error {
	r.ctx = ctx
	r.agentManager = agent.NewAgent()
	providers, err := core.UnmarshalKeyConfig[[]*Provider](configKey, ctx)
	if err != nil {
		return err
	}
	r.chatSessionService = core.GetService[*service.ChatSessionService](ctx)
	r.storeFlow = core.GetService[*flow.StoreFlow](ctx)

	r.agentManager.AddTools(tools.NewCommandTool(), tools.NewAskUserQuestionTool())

	wf := workflow.NewManager()
	wf.AddWorkflow(r.storeFlow.GetFlow(), r.storeFlow.GetExpandFlow())
	// flow 工具组（v3 剧本式）：activate_flow / exec_node / flow_step_done / flow_status / finish_flow
	activateFlow, execNode, stepDone, flowStatus, finishFlow := workflow.NewFlowTools(wf)
	r.agentManager.AddTools(activateFlow, execNode, stepDone, flowStatus, finishFlow)
	r.agentManager.SetHistoryStore(r.chatSessionService)

	// flow 触发引导已随工具自带（ActivateFlowTool.UsagePrompt，经
	// agent.PromptProvider 机制自动拼进每轮 System），此处只留通用人设
	r.agentManager.SetSystem("你是一个智能助手。")

	for _, provider := range providers {
		key := provider.Name + "_" + provider.Type + "_" + provider.Model
		if util.EqualsAnyIgnoreCase(provider.Type, anthropic.TYPE...) {
			r.agentManager.RegisterChat(anthropic.NewService(key, provider.BaseUrl, provider.ApiKey, provider.Model))
		}
	}
	log.Info("Agent initialized (go-agent-sdk)", zap.Int("providers", len(providers)))
	return nil
}

// Run 实现 core.IRunner 后台任务接口。
// Agent 是被动式服务（由 WebSocket 请求驱动），无后台循环，
// 阻塞直到服务上下文取消，避免返回后框架视为任务退出。
func (r *Agent) Run() error {
	<-r.ctx.Done()
	return nil
}
func (r *Agent) GetSession() *Session {
	return newSession(r.agentManager)
}

// History 分页获取会话历史事件。
// since: 起始 start 位置（返回 Start >= since 的事件），limit: 最大返回条数。
func (r *Agent) History(id uint, since uint64, limit int) ([]*entity.ChatMessage, error) {
	session, ok := r.agentManager.GetSession(cast.ToString(id))
	if !ok {
		return nil, nil
	}
	events, err := session.LoadMessagesAfter(since)
	if err != nil {
		return nil, err
	}
	if len(events) > limit {
		events = events[:limit]
	}
	result := make([]*entity.ChatMessage, 0, len(events))
	for _, ev := range events {
		blocksJSON, _ := json.Marshal(ev.Blocks)
		result = append(result, &entity.ChatMessage{
			Start:   ev.Start,
			Offset:  ev.Offset,
			Content: string(blocksJSON),
		})
	}
	return result, nil
}

func (r *Agent) HandleChat(chat *agent.Client, message *entity.WsChatMessage) error {
	chat.WriteText(message.Message)
	return nil
}

func (r *Agent) HandleStop(chat *agent.Client, message *entity.WsStopMessage) error {
	chat.Stop()
	return nil
}
