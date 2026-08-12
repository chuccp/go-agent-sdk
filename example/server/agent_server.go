package server

import (
	"encoding/json"
	"sync"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/example/api/chat/anthropic"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-agent-sdk/example/flow"
	"github.com/chuccp/go-agent-sdk/example/service"
	"github.com/chuccp/go-agent-sdk/tools"
	"github.com/chuccp/go-agent-sdk/workflow/exec"
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
	// flow 工具组（v3 剧本式）：activate_flow / exec_node / flow_step_done / flow_status / finish_flow
	activateFlow, execNode, stepDone, flowStatus, finishFlow := tools.NewFlowTools(r.agentManager.WorkflowManager())
	r.agentManager.AddTools(activateFlow, execNode, stepDone, flowStatus, finishFlow)
	r.agentManager.SetHistoryStore(r.chatSessionService)

	r.agentManager.AddWorkflows(r.storeFlow.GetFlow(), r.storeFlow.GetExpandFlow())
	// flow 触发引导已随工具自带（ActivateFlowTool.UsagePrompt，经
	// agent.PromptProvider 机制自动拼进每轮 System），此处只留通用人设
	r.agentManager.SetSystem("你是一个智能助手。")

	for _, provider := range providers {
		key := provider.Name + "_" + provider.Type + "_" + provider.Model
		if util.EqualsAnyIgnoreCase(provider.Type, anthropic.TYPE...) {
			r.agentManager.RegisterChat(key, anthropic.NewService(&anthropic.Config{
				BaseURL: provider.BaseUrl,
				APIKey:  provider.ApiKey,
				Model:   provider.Model,
			}), provider.Default)
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

// Workflows 返回所有已注册的 workflow。
func (r *Agent) Workflows() []*exec.Workflow {
	return r.agentManager.Workflows()
}
func (r *Agent) History(id uint) ([]*entity.ChatMessage, error) {
	messages, err := r.agentManager.History(cast.ToString(id))
	if err != nil {
		return nil, err
	}
	result := make([]*entity.ChatMessage, 0, len(messages))
	for _, m := range messages {
		contentJSON, _ := json.Marshal(m.Content)
		result = append(result, &entity.ChatMessage{
			Role:    string(m.Role),
			Content: string(contentJSON),
			Start:   m.Start,
			Offset:  m.Offset,
		})
	}
	return result, nil
}

func (r *Agent) HandleChat(chat *agent.Client, message *entity.WsChatMessage) error {
	if err := chat.SendText(message.Message); err != nil {
		log.Warn("HandleChat: send failed", zap.Error(err))
		return err
	}
	return nil
}

func (r *Agent) HandleStop(chat *agent.Client, message *entity.WsStopMessage) error {
	chat.Stop()
	return nil
}
