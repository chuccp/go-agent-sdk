package agent

import "github.com/chuccp/go-agent-sdk/chat"

// Compressor 上下文压缩策略接口。
type Compressor interface {
	Compress(messages []*chat.Message) *chat.Message
}
type CompressorStore interface {
	ReadCompress(sessionId string) (string, uint64)
	WriteCompress(sessionId string, compressor string, start uint64)
}

type CompressorManager struct {
	compressor      Compressor
	compressorStore CompressorStore
}

func (m *CompressorManager) CompressorStore(compressorStore CompressorStore) {
	m.compressorStore = compressorStore
}

// NewCompressorManager 创建压缩器管理器。
func NewCompressorManager(compressor Compressor) *CompressorManager {
	return &CompressorManager{
		compressor: compressor,
	}
}

// Compress 执行压缩，委托给内部的 Compressor 实现。
func (m *CompressorManager) Compress(messages []*chat.Message) *chat.Message {
	if m == nil || m.compressor == nil {
		return nil
	}
	return m.compressor.Compress(messages)
}
