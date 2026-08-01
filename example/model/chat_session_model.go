package model

import (
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/db"
	fwModel "github.com/chuccp/go-web-frame/model"
)

// ChatSessionModel chat session model
type ChatSessionModel struct {
	*fwModel.EntryModel[*entity.ChatSession, uint]
	ctx *core.Context
}

func (m *ChatSessionModel) Init(d *db.DB, ctx *core.Context) error {
	m.ctx = ctx
	m.EntryModel = fwModel.NewEntryModel[*entity.ChatSession, uint](d, "t_chat_session")
	return m.CreateTable()
}

func (m *ChatSessionModel) ReNew(d *db.DB, c *core.Context) core.IModel {
	return &ChatSessionModel{
		EntryModel: fwModel.NewEntryModel[*entity.ChatSession, uint](d, m.GetTableName()),
		ctx:        c,
	}
}

// ChatMessageModel chat message model
type ChatMessageModel struct {
	*fwModel.EntryModel[*entity.ChatMessage, uint]
	ctx *core.Context
}

func (m *ChatMessageModel) Init(d *db.DB, ctx *core.Context) error {
	m.ctx = ctx
	m.EntryModel = fwModel.NewEntryModel[*entity.ChatMessage, uint](d, "t_chat_message")
	return m.CreateTable()
}

func (m *ChatMessageModel) ReNew(d *db.DB, c *core.Context) core.IModel {
	return &ChatMessageModel{
		ctx:        c,
		EntryModel: fwModel.NewEntryModel[*entity.ChatMessage, uint](d, m.GetTableName()),
	}
}
