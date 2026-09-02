package service

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-agent-sdk/example/model"
	"github.com/chuccp/go-web-frame/core"
)

// ChatSessionService provides business logic for chat session and message CRUD.
// It wraps the model layer and is consumed by REST handlers via core.GetService.
// Implements agent.MessageStore interface for persistent message storage.
type ChatSessionService struct {
	context      *core.Context
	sessionModel *model.ChatSessionModel
	messageModel *model.ChatMessageModel
}

// Init implements core.IService. It resolves models from the core context.
func (s *ChatSessionService) Init(ctx *core.Context) error {
	s.context = ctx
	s.sessionModel = core.GetModel[*model.ChatSessionModel](ctx)
	s.messageModel = core.GetModel[*model.ChatMessageModel](ctx)
	return nil
}

// ListSessions returns all chat sessions ordered by most recently updated.
func (s *ChatSessionService) ListSessions() ([]*entity.ChatSession, error) {
	return s.sessionModel.Query().
		Order("updated_at desc").
		All()
}

// CreateSession creates a new chat session with the given title.
func (s *ChatSessionService) CreateSession(ctx context.Context, title string) (*entity.ChatSession, error) {
	session := &entity.ChatSession{Title: title}
	if err := s.sessionModel.WithContext(ctx).Save(session); err != nil {
		return nil, err
	}
	return session, nil
}

// DeleteSession deletes a session and all its messages.
func (s *ChatSessionService) DeleteSession(ctx context.Context, id uint) error {
	// Delete all messages in this session first
	if err := s.messageModel.WithContext(ctx).
		Delete().Where("session_id = ?", id).Delete(); err != nil {
		return err
	}
	// Delete the session itself
	return s.sessionModel.WithContext(ctx).DeleteByPK(id)
}

// ── agent.MessageStore 接口实现 ──

// LoadAfter 读取 Start+Offset > since 的原始消息，按 Start 升序，最多 limit 条。
// 实现 agent.MessageStore 接口。
func (s *ChatSessionService) LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error) {
	id, err := strconv.ParseUint(sessionID, 10, 64)
	if err != nil {
		return nil, nil // invalid sessionID, treat as new session
	}

	rows, err := s.messageModel.Query().
		Where("session_id = ?", uint(id)).
		Order("created_at asc").
		All()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	messages := make([]*chat.Message, 0, len(rows))
	for _, row := range rows {
		if row.Start+row.Offset <= since {
			continue
		}
		msg := &chat.Message{
			Role:   chat.Role(row.Role),
			Start:  row.Start,
			Offset: row.Offset,
		}
		if row.Content != "" {
			var blocks chat.Blocks
			if err := json.Unmarshal([]byte(row.Content), &blocks); err == nil {
				msg.Content = blocks
			} else {
				// fallback: plain text stored in legacy format
				msg.Content = chat.Blocks{chat.NewFullTextBlock(row.Content)}
			}
		}
		messages = append(messages, msg)
		if len(messages) >= limit {
			break
		}
	}
	return messages, nil
}

// Append 增量追加本批次新产生的消息。
// 实现 agent.MessageStore 接口。
func (s *ChatSessionService) Append(sessionID string, messages []*chat.Message) error {
	id, err := strconv.ParseUint(sessionID, 10, 64)
	if err != nil {
		return nil
	}
	sid := uint(id)

	for _, msg := range messages {
		contentJSON, _ := json.Marshal(msg.Content)
		row := &entity.ChatMessage{
			Start:     msg.Start,
			Offset:    msg.Offset,
			SessionId: sid,
			Role:      string(msg.Role),
			Content:   string(contentJSON),
		}
		if err := s.messageModel.Save(row); err != nil {
			return err
		}
	}
	return nil
}

// LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩。
// 实现 agent.MessageStore 接口。当前不支持压缩，始终返回 nil。
func (s *ChatSessionService) LoadSummary(sessionID string) (*chat.Message, error) {
	return nil, nil
}

// SaveSummary 保存压缩摘要。
// 实现 agent.MessageStore 接口。当前不支持压缩，空实现。
func (s *ChatSessionService) SaveSummary(sessionID string, summary *chat.Message) error {
	return nil
}
