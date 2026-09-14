package agent

import "github.com/chuccp/go-agent-sdk/chat"

// Compressor 上下文压缩策略接口。
type Compressor interface {
	Compress(context Context, messages []*chat.Message) *chat.Message
}

type Summary interface {
	// LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩（等价于分界点 0）。
	// 返回值需与 SaveSummary 写入的一致。
	LoadSummary(sessionID string) (*chat.Message, error)

	// SaveSummary 保存压缩摘要（记录分界点），不删除任何历史消息。
	// summary.Start 即分界点：Start < summary.Start 的旧消息在上下文中由摘要取代。
	SaveSummary(sessionID string, summary *chat.Message) error
}

type CompressorOptions struct {
	compressor       Compressor
	maxContextLength int
	compressRatio    float64
}

type CompressorOption func(*CompressorOptions)

func WithCompressor(compressor Compressor) CompressorOption {
	return func(o *CompressorOptions) {
		o.compressor = compressor
	}
}
func WithMaxContextLength(maxContextLength int) CompressorOption {
	return func(o *CompressorOptions) {
		o.maxContextLength = maxContextLength
	}
}
func WithCompressRatio(compressRatio float64) CompressorOption {
	return func(o *CompressorOptions) {
		o.compressRatio = compressRatio
	}
}

type CompressorManager struct {
	compressor       Compressor
	summary          Summary
	contextLength    int
	maxContextLength int
	compressRatio    float64
	summaryMessage   *chat.Message
	sessionId        string
}

// NewCompressorManager 创建压缩器管理器。
func NewCompressorManager(sessionId string, compressorOptions *CompressorOptions, summary Summary) *CompressorManager {
	return &CompressorManager{
		sessionId:        sessionId,
		compressor:       compressorOptions.compressor,
		contextLength:    0,
		maxContextLength: compressorOptions.maxContextLength,
		compressRatio:    compressorOptions.compressRatio,
		summary:          summary,
	}
}

// SetLimit 设置最大上下文与触发压缩的比例：上下文长度达到
// maxContextLength*compressRatio 时才需要压缩，提前介入以留出余量，
// 避免压完立刻又被撑满。两项任一 <= 0 表示不压缩。
func (m *CompressorManager) SetLimit(maxContextLength int, compressRatio float64) {
	m.maxContextLength = maxContextLength
	m.compressRatio = compressRatio
}

// shouldCompress 报告当前上下文是否已达到触发压缩的水位。
func (m *CompressorManager) shouldCompress() bool {
	if m == nil || m.maxContextLength <= 0 || m.compressRatio <= 0 {
		return false
	}
	return float64(m.contextLength) >= float64(m.maxContextLength)*m.compressRatio
}
func (m *CompressorManager) UpdateUsage(usage *chat.Usage) {
	if usage != nil {
		if usage.OutputTokens > 0 && usage.InputTokens > 0 {
			m.contextLength = usage.OutputTokens + usage.InputTokens + usage.CacheInputTokens
		}
	}

}

func (m *CompressorManager) loadSummary() (*chat.Message, error) {
	if m.summaryMessage != nil {
		return m.summaryMessage, nil
	}
	if m.summary == nil {
		m.summaryMessage = &chat.Message{
			Start: 0,
		}
		return m.summaryMessage, nil
	}
	if m.summary != nil {
		summaryMessage, err := m.summary.LoadSummary(m.sessionId)
		if err != nil {
			return nil, err
		}
		if summaryMessage == nil {
			m.summaryMessage = &chat.Message{
				Start: 0,
			}
		} else {
			m.summaryMessage = summaryMessage
		}
	}
	return m.summaryMessage, nil
}

// Compress 执行压缩，委托给内部的 Compressor 实现。
func (m *CompressorManager) compress(context Context, messages []*chat.Message) []*chat.Message {
	if m == nil || m.compressor == nil {
		return nil
	}
	msg := m.compressor.Compress(context, messages)
	if msg != nil {
		return messages
	}

	return messages
}
