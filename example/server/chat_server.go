package server

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/example/api/chat/anthropic"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-agent-sdk/example/service"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/log"
	"github.com/chuccp/go-web-frame/util"
	"go.uber.org/zap"
)

type Agent struct {
	core.IRunner
	ctx                *core.Context
	chatManager        *agent.ChatManager
	lock               sync.RWMutex
	chatSessionService *service.ChatSessionService
}

func (r *Agent) Init(ctx *core.Context) error {
	r.ctx = ctx
	r.chatManager = agent.NewChatManager()
	providers, err := core.UnmarshalKeyConfig[[]*Provider](configKey, ctx)
	if err != nil {
		return err
	}
	r.chatSessionService = core.GetService[*service.ChatSessionService](ctx)
	r.chatManager.AddTool(agent.NewCommandTool())
	r.chatManager.SetHistoryStore(r.chatSessionService)
	for _, provider := range providers {
		key := provider.Name + "_" + provider.Type + "_" + provider.Model
		if util.EqualsAnyIgnoreCase(provider.Type, anthropic.TYPE...) {
			r.chatManager.RegisterChat(key, anthropic.NewService(&anthropic.Config{
				BaseURL: provider.BaseUrl,
				APIKey:  provider.ApiKey,
				Model:   provider.Model,
			}), provider.Default)
		}
	}
	log.Info("Agent initialized (go-agent-sdk)", zap.Int("providers", len(providers)))
	return nil
}
func (r *Agent) GetSession() *Session {
	return newSession(r.chatManager)
}

func (r *Agent) HandleChat(chat *agent.ChatClient, message *entity.Message) error {
	if err := chat.SendText(message.Message); err != nil {
		log.Warn("HandleChat: send failed", zap.String("session_id", message.GetSessionId()), zap.Error(err))
		return err
	}
	return nil
}

func (r *Agent) HandleStop(chat *agent.ChatClient, message *entity.Message) error {
	chat.Stop()
	return nil
}
