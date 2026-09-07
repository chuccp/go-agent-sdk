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
	"github.com/chuccp/go-agent-sdk/util"
	"github.com/chuccp/go-web-frame/core"
	"github.com/chuccp/go-web-frame/log"
	"github.com/chuccp/go-web-frame/web"
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
	// Chat actions（发送消息 / 停止生成 / 设置思考程度）
	ctx.Post("/api/chat/sessions/:id/messages", c.sendMessage)
	ctx.Post("/api/chat/sessions/:id/stop", c.stopGeneration)
	ctx.Put("/api/chat/sessions/:id/thinking", c.setThinking)
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
	c.agent.DeleteSession(id)
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

// sendMessage 接收前端发送的聊天消息，转发给 agent 处理。
func (c *Chat) sendMessage(request *web.Request) (any, error) {
	id := request.ParamUint("id")
	jsonObj, err := request.Json()
	if err != nil {
		return nil, err
	}
	msg := &entity.WsChatMessage{
		Message: jsonObj.GetString("message"),
	}
	if err := c.agent.HandleChat(id, msg); err != nil {
		return nil, err
	}
	return web.Ok("sent"), nil
}

// stopGeneration 请求停止当前会话的生成。
func (c *Chat) stopGeneration(request *web.Request) (any, error) {
	id := request.ParamUint("id")
	if err := c.agent.HandleStop(id, &entity.WsStopMessage{}); err != nil {
		return nil, err
	}
	return web.Ok("stopped"), nil
}

// setThinking 设置会话的思考程度。
func (c *Chat) setThinking(request *web.Request) (any, error) {
	id := request.ParamUint("id")
	jsonObj, err := request.Json()
	if err != nil {
		return nil, err
	}
	level := jsonObj.GetString("level")
	if level == "" {
		level = "off"
	}
	if err := c.agent.HandleThinking(id, level); err != nil {
		return nil, err
	}
	return web.Ok("thinking set to " + level), nil
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
	options := make([]chat.Option, 0)
	level := request.Query("level")
	if util.IsNotBlank(level) {
		options = append(options, chat.WithThinking(chat.ThinkingLevel(level)))
	}
	session := c.agent.GetAgent().GetOrCreateSession(strconv.Itoa(int(sessionId)), agent.WithChatOption(options...))
	client := session.CreateClient(webSocket.Request().Ctx(), start)
	defer client.Close()
	for {
		events, err := client.ReadEvents()
		if err != nil {
			writeError(stream, err)
			break
		}
		if events == nil {
			log.Info("[RELAY] ReadEvent returned nil, exiting")
			break
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
				break
			}
		}
	}
	return nil
}

// writeError 向前端发送错误事件
func writeError(stream *web.WebSocketStream, err error) {
	data, _ := json.Marshal(agent.NewEvent(0, 0, chat.NewErrorBlock(err.Error())))
	_ = stream.WriteText(context.Background(), data)
}
