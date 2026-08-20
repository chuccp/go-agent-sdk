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

// GetSessionMessages returns all messages for a session ordered by creation time.
func (s *ChatSessionService) GetSessionMessages(sessionId uint) ([]*entity.ChatMessage, error) {
	return s.messageModel.Query().
		Where("session_id = ?", sessionId).
		Order("created_at asc").
		All()
}

// LoadHistory loads the chat history for a session from the database,
// converting stored rows back to SDK chat.Message format.
// Implements agent.HistoryStore interface.
func (s *ChatSessionService) LoadHistory(sessionID string) ([]chat.Message, error) {
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

	messages := make([]chat.Message, 0, len(rows))
	for _, row := range rows {
		msg := chat.Message{
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
	}
	return messages, nil
}

// AppendMessages persists new messages for a session to the database (incremental insert).
// Implements chat.HistoryStore interface.
func (s *ChatSessionService) AppendMessages(sessionID string, messages []chat.Message) error {
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
