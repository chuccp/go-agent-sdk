package model

import (
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/db"
	fwModel "github.com/chuccp/go-web-frame/model"
)

// ChatSummaryModel chat summary model
type ChatSummaryModel struct {
	*fwModel.EntryModel[*entity.ChatSummary, uint]
	ctx *core.Context
}

func (m *ChatSummaryModel) Init(d *db.DB, ctx *core.Context) error {
	m.ctx = ctx
	m.EntryModel = fwModel.NewEntryModel[*entity.ChatSummary, uint](d, "t_chat_summary")
	return m.CreateTable()
}

func (m *ChatSummaryModel) ReNew(d *db.DB, c *core.Context) core.IModel {
	return &ChatSummaryModel{
		ctx:        c,
		EntryModel: fwModel.NewEntryModel[*entity.ChatSummary, uint](d, m.GetTableName()),
	}
}
