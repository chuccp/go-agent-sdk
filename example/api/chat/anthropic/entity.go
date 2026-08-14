package anthropic

import "github.com/chuccp/go-agent-sdk/chat"

// sseEvent 表示 Anthropic 流式响应中的一条原始 SSE 事件。
type sseEvent struct {
	Type         string           `json:"type"`
	Index        int              `json:"index"`
	Delta        *sseDelta        `json:"delta"`
	ContentBlock *sseContentBlock `json:"content_block"`
	Message      *sseMessage      `json:"message"`
	Usage        *chat.Usage      `json:"usage"`
}

// sseContentBlock 表示 content_block_start 事件中的内容块元信息。
type sseContentBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sseDelta 表示 content_block_delta / message_delta 事件中的增量。
type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// sseMessage 表示 message_start 事件中的消息元信息。
type sseMessage struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	Usage      chat.Usage      `json:"usage"`
	StopReason chat.StopReason `json:"stop_reason"`
}
