package entity

import "github.com/spf13/cast"

// WebSocket message type constants.
const (
	ChatType   = "chat"
	CreateType = "create"
	PingType   = "ping"
	PongType   = "pong"
	StopType   = "stop"
)

// Message is the incoming WebSocket message structure for the chat protocol.
type Message struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	SessionId uint   `json:"session_id"`
	Start     uint   `json:"start"`
}

func (m *Message) GetSessionId() string {
	return cast.ToString(m.SessionId)
}
