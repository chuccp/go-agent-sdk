package agent

import (
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
)

// BlockStream 是工具输出专用的写入管道：内部维护一个 Block 列表。
// 工具通过 WriteBlock 写入内容块（连续 TextBlock 会被拼接为一个），
// 调用方先 Close（flush 未完成的拼接），再通过 ReadBlocks() 一次性取回全部 Block。
// LLM 流式输出使用独享的 chat.StreamWriter（仅用于接收大模型的值），两者不共用。
type BlockStream struct {
	blocks     []chat.Block
	pending    strings.Builder // 连续 TextBlock 的拼接缓冲
	hasPending bool
	err        error
	closed     bool
}

func NewBlockStream() *BlockStream {
	return &BlockStream{
		blocks: make([]chat.Block, 0),
	}
}

// WriteBlock 写入一个内容块：连续的 TextBlock 会被拼接为一个，
// 遇到其他类型（或 Close）时输出拼接结果，其余类型直接收集。
func (r *BlockStream) WriteBlock(block chat.Block) error {
	if tb, ok := block.(*chat.TextBlock); ok {
		r.pending.WriteString(tb.Text)
		r.hasPending = true
		return nil
	}
	if err := r.flushPending(); err != nil {
		return err
	}
	r.blocks = append(r.blocks, block)
	return nil
}

// WriteError 记录错误，ReadBlocks 返回时携带该错误。
func (r *BlockStream) WriteError(err error) {
	r.err = err
}

// Close 结束写入：输出未完成的拼接内容。幂等，多次调用安全。
func (r *BlockStream) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.flushPending()
}

// ReadBlocks 返回已收集的全部 Block 与错误（调用前应先 Close）。
func (r *BlockStream) ReadBlocks() (chat.Blocks, error) {
	return r.blocks, r.err
}

// Err 返回执行过程中发生的错误。
func (r *BlockStream) Err() error { return r.err }

// flushPending 将累积的文本拼接结果作为一个 TextBlock 收集。
func (r *BlockStream) flushPending() error {
	if !r.hasPending {
		return nil
	}
	r.hasPending = false
	b := chat.NewTextBlock(r.pending.String())
	r.pending.Reset()
	r.blocks = append(r.blocks, b)
	return nil
}

// withoutThinking 从 blocks 中剥离 thinking block。
// 发送给 LLM 的历史不需要思考链：Anthropic 要求历史中的 thinking block 必须携带
// signature 字段原样传回，否则报 400；且空 thinking 会因 omitempty 序列化为
// {"type":"thinking"} 缺少 thinking 字段。思考链仍保留在 history/DB 中供展示，
// 仅不回传给模型。
func withoutThinking(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
		if _, ok := b.(*chat.ThinkingBlock); ok {
			continue
		}
		result = append(result, b)
	}
	return result
}
