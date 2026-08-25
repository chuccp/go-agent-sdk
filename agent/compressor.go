package agent

import "github.com/chuccp/go-agent-sdk/chat"

// Compressor 上下文压缩策略接口。
type Compressor interface {
	Compress(ctx LoopContext, messages []*chat.Message) *chat.Message
	Filter(ctx LoopContext, messages *chat.Message) bool
}

type CompressorManager struct {
	compressor Compressor
}

// NewCompressorManager 创建压缩器管理器。
func NewCompressorManager(compressor Compressor) *CompressorManager {
	return &CompressorManager{
		compressor: compressor,
	}
}

// Compress 执行压缩，委托给内部的 Compressor 实现。
func (m *CompressorManager) Compress(ctx LoopContext, messages []*chat.Message) *chat.Message {
	if m == nil || m.compressor == nil {
		return nil
	}
	return m.compressor.Compress(ctx, messages)
}
func (m *CompressorManager) Filter(ctx LoopContext, messages *chat.Message) bool {
	return true
}
