package entity

import (
	"encoding/json"

	"github.com/spf13/cast"
)

// WebSocket message type constants.
const (
	ChatType   = "chat"
	CreateType = "create"
	PingType   = "ping"
	PongType   = "pong"
	StopType   = "stop"
)

// Message is the common interface shared by all incoming WebSocket messages.
// Each concrete message type carries only the fields relevant to itself,
// and is identified by Type().
type Message interface {
	Type() string
}

// WsChatMessage 客户端发送的聊天文本消息。
type WsChatMessage struct {
	Message  string `json:"message"`
	Thinking string `json:"thinking,omitempty"` // 思考等级：off / low / medium / high
}

func (m *WsChatMessage) Type() string { return ChatType }

// WsCreateMessage 客户端请求创建/接入会话。
type WsCreateMessage struct {
	SessionId uint `json:"session_id"`
	Start     uint `json:"start"`
}

func (m *WsCreateMessage) Type() string { return CreateType }

// GetSessionId 返回字符串形式的会话 ID。
func (m *WsCreateMessage) GetSessionId() string {
	return cast.ToString(m.SessionId)
}

// WsPingMessage 心跳请求。
type WsPingMessage struct{}

func (m *WsPingMessage) Type() string { return PingType }

// WsPongMessage 心跳响应。
type WsPongMessage struct{}

func (m *WsPongMessage) Type() string { return PongType }

// WsStopMessage 请求停止当前会话的生成。
type WsStopMessage struct{}

func (m *WsStopMessage) Type() string { return StopType }

// messageEnvelope 仅用于在解析时读取 type 判别字段。
type messageEnvelope struct {
	Type string `json:"type"`
}

// ParseMessage 根据 JSON 中的 type 字段，将原始报文解析为对应的具体消息类型。
// 未知的 type 返回 (nil, nil)，由调用方决定如何处理。
func ParseMessage(data []byte) (Message, error) {
	var env messageEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	var msg Message
	switch env.Type {
	case ChatType:
		msg = &WsChatMessage{}
	case CreateType:
		msg = &WsCreateMessage{}
	case PingType:
		msg = &WsPingMessage{}
	case PongType:
		msg = &WsPongMessage{}
	case StopType:
		msg = &WsStopMessage{}
	default:
		return nil, nil
	}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	return msg, nil
}
