package entity

import "time"

// ChatSummary 会话压缩摘要：每个会话一条，Start 为压缩分界点。
// Start 之前的历史消息在 LLM 上下文中由摘要内容取代，历史消息本身不删除。
type ChatSummary struct {
	Id        uint      `gorm:"primaryKey" json:"id"`
	SessionId uint      `gorm:"uniqueIndex" json:"session_id"`
	Start     uint64    `json:"start"`
	Role      string    `gorm:"size:32" json:"role"`
	Content   string    `gorm:"type:text" json:"content"` // JSON 序列化的 chat.Blocks
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ChatSummary) TableName() string {
	return "chat_summaries"
}
