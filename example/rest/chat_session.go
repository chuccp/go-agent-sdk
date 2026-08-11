package rest

import (
	"context"
	"encoding/json"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-agent-sdk/example/server"
	"github.com/chuccp/go-agent-sdk/example/service"
	sdkutil "github.com/chuccp/go-agent-sdk/util"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/log"
	"github.com/chuccp/go-web-frame/web"
	"github.com/coder/websocket"
	"go.uber.org/zap"
)

const maxActiveConns = 100

// connState tracks per-WebSocket-connection state: the active chat client
// and a cancel function to stop the event relay goroutine.
type connState struct {
	client *agent.Client
	cancel context.CancelFunc
}

// Chat registers WebSocket and REST API routes for the web chat.
// It handles all WebSocket I/O (connect, read, write, disconnect) and
// delegates to the go-agent-sdk Agent for the actual LLM work.
type Chat struct {
	context            *core.Context
	agent              *server.Agent
	chatSessionService *service.ChatSessionService
}

// Init registers all chat-related routes on the web framework context.
func (c *Chat) Init(ctx *core.Context) error {
	c.context = ctx
	c.agent = core.GetRunner[*server.Agent](ctx)
	c.chatSessionService = core.GetService[*service.ChatSessionService](ctx)
	// Session CRUD
	ctx.Get("/api/chat/sessions", c.listSessions)
	ctx.Post("/api/chat/sessions", c.createSession)
	ctx.Delete("/api/chat/sessions/:id", c.deleteSession)
	ctx.Get("/api/chat/sessions/:id/messages", c.getSessionMessages)
	ctx.WebSocket("/ws/chat", c.HandleWebSocket)
	log.Info("Chat REST routes registered (go-agent-sdk)", zap.String("ws", "/ws/chat"))
	return nil
}

// ── Session REST handlers ─────────────────────────────────────────────

// listSessions returns all chat sessions ordered by most recently updated.
func (c *Chat) listSessions(request *web.Request) (any, error) {
	sessions, err := c.chatSessionService.ListSessions()
	if err != nil {
		return nil, err
	}
	return web.Data(sessions), nil
}

// createSession creates a new chat session with an optional title.
func (c *Chat) createSession(request *web.Request) (any, error) {
	title := "New Chat"
	if jsonObj, err := request.Json(); err == nil {
		if t := jsonObj.GetString("title"); t != "" {
			title = t
		}
	}

	session, err := c.chatSessionService.CreateSession(request.Ctx(), title)
	if err != nil {
		return nil, err
	}
	return web.Data(session), nil
}

// deleteSession deletes a session and all its messages.
func (c *Chat) deleteSession(request *web.Request) (any, error) {
	id := request.ParamUint("id")
	if err := c.chatSessionService.DeleteSession(request.Ctx(), id); err != nil {
		return nil, err
	}
	return web.Ok("deleted"), nil
}

// getSessionMessages returns all messages for a session ordered by creation time.
func (c *Chat) getSessionMessages(request *web.Request) (any, error) {
	sessionId := request.ParamUint("id")
	messages, err := c.agent.History(sessionId)
	if err != nil {
		return nil, err
	}
	return web.Data(messages), nil
}

// ── WebSocket handler ──────────────────────────────────────────────────

// HandleWebSocket is the entry point for web WebSocket connections.
// Each connection gets a unique session ID; all chat messages within
// one connection share the same conversation context.
func (c *Chat) HandleWebSocket(webSocket *web.WebSocket) error {
	stream, err := webSocket.OpenStream(web.WithOriginPatterns("*"))
	if err != nil {
		return err
	}
	defer stream.Close()
	stream.Conn().SetReadLimit(10 * 1024 * 1024)

	session := c.agent.GetSession()
	defer session.Release()

	sdkutil.Go(func() {
		var lastSeq uint
		for {
			event := session.ReadEvent()
			if event == nil {
				log.Info("[RELAY] ReadEvent returned nil, exiting")
				return
			}
			// 跳过重复事件
			if event.Seq != 0 && event.Seq <= lastSeq {
				log.Info("[RELAY] skipping duplicate", zap.Uint("seq", event.Seq), zap.Uint("lastSeq", lastSeq), zap.String("type", event.EventType))
				continue
			}
			lastSeq = event.Seq
			log.Info("[RELAY] sending event", zap.Uint("seq", event.Seq), zap.String("type", event.EventType))
			if event.EventType == "done" {
				log.Info("[RELAY] sending DONE event", zap.Uint("seq", event.Seq))
			}
			data, err := json.Marshal(event)
			if err != nil {
				writeError(stream, err)
				continue
			}
			if err := stream.WriteText(stream.Context(), data); err != nil {
				log.Debug("WebSocket write ended", zap.Error(err))
				return
			}
		}
	})

	for {
		messageType, message, err := stream.Read(stream.Context())
		if err != nil {
			log.Debug("WebSocket read ended", zap.Error(err))
			break
		}
		switch messageType {
		case websocket.MessageText:
			msg, err := entity.ParseMessage(message)
			if err != nil {
				return err
			}
			switch m := msg.(type) {
			case *entity.WsChatMessage:
				if err := session.HandleChat(m); err != nil {
					writeError(stream, err)
				}
			case *entity.WsCreateMessage:
				if err := session.CreateChat(m); err != nil {
					writeError(stream, err)
				} else {
					// 回执：通知前端会话已就绪，可以开始发送消息
					writeCreated(stream, m.SessionId)
				}
			case *entity.WsStopMessage:
				if err := session.HandleStop(); err != nil {
					writeError(stream, err)
				}
			}
		case websocket.MessageBinary:

		}

		log.Debug("WebSocket read", zap.String("type", string(messageType)), zap.Any("message", message))
	}
	return nil
}

// writeError 向前端发送错误事件
func writeError(stream *web.WebSocketStream, err error) {
	data, _ := json.Marshal(chat.NewErrorEvent(err.Error()))
	_ = stream.WriteText(context.Background(), data)
}

// writeCreated 向前端发送会话就绪确认消息
func writeCreated(stream *web.WebSocketStream, sessionId uint) {
	data, _ := json.Marshal(entity.NewCreatedMessage(sessionId))
	_ = stream.WriteText(context.Background(), data)
}
