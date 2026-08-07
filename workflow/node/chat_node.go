package node

import "github.com/chuccp/go-agent-sdk/chat"

type ChatNode struct {
	BaseNode
}

type ChatNodeBuilder struct {
	chatService chat.ChatService
}
