package agent

import "github.com/chuccp/go-agent-sdk/chat"

// HistoryStore 聊天记录持久化接口，由主程序实现。
// SDK 在创建会话时调用 LoadHistory 恢复历史，在每轮对话结束后调用 SaveHistory 保存。
type HistoryStore interface {
	// LoadHistory 加载指定会话的历史消息。
	// 返回空切片表示新会话，无历史记录。
	LoadHistory(sessionID string) ([]chat.Message, error)

	// SaveHistory 保存指定会话的完整历史消息。
	// 每次调用传入的是当前会话的完整 history（全量覆盖）。
	SaveHistory(sessionID string, messages []chat.Message) error
}
