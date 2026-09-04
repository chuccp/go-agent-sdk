package rest

import (
	"context"
	"encoding/json"
	"strconv"

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
	ctx.WebSocket("/ws/chat/:id", c.HandleWebSocket)
	log.Info("Chat REST routes registered (go-agent-sdk)", zap.String("ws", "/ws/chat/:id"))
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

// getSessionMessages 通过 agent API 获取会话历史事件（与 WebSocket 推送格式一致）。
// Query params: since (起始 start，默认 0)
func (c *Chat) getSessionMessages(request *web.Request) (any, error) {
	sessionId := request.ParamUint("id")
	var since uint64
	if s := request.Query("since"); s != "" {
		since, _ = strconv.ParseUint(s, 10, 64)
	}
	events, err := c.agent.History(sessionId, since)
	if err != nil {
		return nil, err
	}
	return web.Data(events), nil
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
	request := webSocket.Request()
	sessionId := request.ParamUint("id")
	var start uint64
	if s := request.Query("start"); s != "" {
		start, _ = strconv.ParseUint(s, 10, 64)
	}
	session := c.agent.GetAgent().GetOrCreateSession(strconv.Itoa(int(sessionId)))
	client := session.CreateClient(webSocket.Request().Ctx(), start)
	sdkutil.Go(func() {
		// 事件去重由 Transfer.readEvents（cl.start 递增）与 mergeMessages（block 级别
		// 精确去重）保证。relay 这里不做 lastSeq 单调递增去重——那会跳过 message 里
		// 排序后 start 小于已发事件的 block（如 MessageDeltaBlock）。
		for {
			events, err := client.ReadEvents()
			if err != nil {
				writeError(stream, err)
				return
			}
			if events == nil {
				log.Info("[RELAY] ReadEvent returned nil, exiting")
				return
			}
			for _, event := range events {
				if len(event.Blocks) == 0 {
					continue
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
				client.WriteText(m.Message)
			case *entity.WsStopMessage:
				client.Stop()
			}
		case websocket.MessageBinary:

		}

		log.Debug("WebSocket read", zap.String("type", strconv.Itoa(int(messageType))), zap.Any("message", message))
	}
	return nil
}

// writeError 向前端发送错误事件
func writeError(stream *web.WebSocketStream, err error) {
	data, _ := json.Marshal(agent.NewEvent(0, 0, chat.NewErrorBlock(err.Error())))
	_ = stream.WriteText(context.Background(), data)
}

// blockTypeName 从 Block 推导类型名称，用于日志。
func blockTypeName(b chat.Block) string {
	switch b.(type) {
	case *chat.TextBlock:
		return "text"
	case *chat.ThinkingBlock:
		return "thinking"
	case *chat.ToolUseBlock:
		return "tool_use"
	case *chat.ToolResultBlock:
		return "tool_result"
	case *chat.DoneBlock:
		return "done"
	case *chat.ErrorBlock:
		return "error"
	case *chat.MessageStartBlock:
		return "message_start"
	case *chat.MessageDeltaBlock:
		return "message_delta"
	case *chat.StartBlock:
		return "start"
	case *chat.DeltaBlock:
		return "delta"
	case *chat.UserBlock:
		return "user"

	case *chat.ImageBlock:
		return "image"
	default:
		return "unknown"
	}
}

// writeCreated 向前端发送会话就绪确认消息
func writeCreated(stream *web.WebSocketStream, sessionId uint) {
	data, _ := json.Marshal(entity.NewCreatedMessage(sessionId))
	_ = stream.WriteText(context.Background(), data)
}
