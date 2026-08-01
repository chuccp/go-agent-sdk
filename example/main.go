package main

import (
	"github.com/chuccp/go-agent-sdk/example/model"
	"github.com/chuccp/go-agent-sdk/example/rest"
	"github.com/chuccp/go-agent-sdk/example/server"
	"github.com/chuccp/go-agent-sdk/example/service"
	wf "github.com/chuccp/go-web-frame"
	"github.com/chuccp/go-web-frame/config"
	"github.com/chuccp/go-web-frame/log"
	"go.uber.org/zap"
)

func main() {
	loadConfig, err := config.LoadConfig("./application.yml")
	if err != nil {
		log.Panic("loadConfig", zap.String("err", err.Error()))
		return
	}
	builder := wf.NewBuilder(loadConfig)
	builder.Service(&service.ChatSessionService{})
	builder.Service(&server.Agent{})
	builder.Rest(&rest.Chat{})
	builder.Model(&model.ChatMessageModel{}, &model.ChatSessionModel{})
	frame := builder.Build()
	err = frame.Start()
	if err != nil {
		log.Panic("frame.Start", zap.String("err", err.Error()))
		return
	}
}
