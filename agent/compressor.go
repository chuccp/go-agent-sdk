package agent

import "github.com/chuccp/go-agent-sdk/chat"

// Compressor 上下文压缩策略接口。
// buildRequest 在组装消息列表前调用 Compress。
//
// 实现方式：
//   - 标记 message.IsCompressor = true 表示该消息已被压缩/消费
//   - 返回 *chat.Message 作为摘要消息（注入请求最前面），无需摘要则返回 nil
//
// 实现约束：
//   - 剩余 IsCompressor==false 的消息必须 user/assistant 交替
//   - 首条 IsCompressor==false 的 Role 必须是 RoleUser
//   - tool_use 和 tool_result 成对保留或成对丢弃
type Compressor interface {
	Compress(ctx LoopContext, messages []*chat.Message) *chat.Message
}

// CompressorStore 压缩器持久化接口，由主程序实现。
type CompressorStore interface {
	// SaveSummary 保存指定会话的摘要文本。
	SaveSummary(sessionID string, summary string) error

	// LoadSummary 加载指定会话的摘要文本。空字符串 = 无历史摘要。
	LoadSummary(sessionID string) (string, error)

	// UpdateSummary 更新指定会话的摘要文本。
	UpdateSummary(sessionID string, summary string) error

	// MarkCompressed 将指定消息标记为已压缩（持久化 IsCompressor=true）。
	MarkCompressed(sessionID string, messages []*chat.Message) error
}

// CompressorManager 统一的压缩器管理器，组合压缩策略与持久化能力。
// SessionContext 持有此结构体，压缩器实现通过 ctx.GetCompressorStore() 获取持久化能力。
type CompressorManager struct {
	compressor Compressor
	store      CompressorStore
}

// NewCompressorManager 创建压缩器管理器。
func NewCompressorManager(compressor Compressor, store CompressorStore) *CompressorManager {
	return &CompressorManager{
		compressor: compressor,
		store:      store,
	}
}

// Compress 执行压缩，委托给内部的 Compressor 实现。
func (m *CompressorManager) Compress(ctx LoopContext, messages []*chat.Message) *chat.Message {
	if m == nil || m.compressor == nil {
		return nil
	}
	return m.compressor.Compress(ctx, messages)
}

// GetStore 返回压缩器持久化实现。
func (m *CompressorManager) GetStore() CompressorStore {
	if m == nil {
		return nil
	}
	return m.store
}
