package agent

import (
	"strings"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
)

// BlockStream 是工具输出专用的写入管道：内部维护一个 Block 列表。
// 工具通过 WriteBlock 写入内容块（连续 TextBlock 会被拼接为一个），
// 也可通过 WriteEvent 逐段推送流式输出（如长耗时命令的实时回显）；
// 调用方先 Close（flush 未完成的拼接），再通过 ReadBlocks() 一次性取回全部 Block。
// LLM 流式输出使用独享的 chat.StreamWriter（仅用于接收大模型的值），两者不共用。
type BlockStream struct {
	mu         sync.Mutex // 保护写入与 flush（工具流式输出可能来自多个协程）
	blocks     []chat.Block
	pending    strings.Builder // 连续 TextBlock 的拼接缓冲
	hasPending bool
	err        error
	closed     bool
	receiver   chat.EventReceiver
}

// NewBlockStream 创建工具输出管道。receiver 为事件接收方（如 SessionContext），
// nil 表示不外发事件（WriteEvent 仅收集内容）。
func NewBlockStream(receiver chat.EventReceiver) *BlockStream {
	return &BlockStream{
		receiver: receiver,
		blocks:   make([]chat.Block, 0),
	}
}

// WriteEvent 推送一段流式输出：经 receiver 向外发送 chunk 事件（实时回显），
// 同时作为文本块收集，Close 后随 ReadBlocks 进入 tool_result。
// 兼容长耗时命令等不是一次性出结果、而是流式输出的场景。
func (r *BlockStream) WriteEvent(content string) {
	if content == "" {
		return
	}
	if r.receiver != nil {
		r.receiver.AddEvent(chat.NewChunkEvent(content))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeBlockLocked(chat.NewTextBlock(content))
}

// WriteBlock 写入一个内容块：连续的 TextBlock 会被拼接为一个，
// 遇到其他类型（或 Close）时输出拼接结果，其余类型直接收集。
func (r *BlockStream) WriteBlock(block chat.Block) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeBlockLocked(block)
}

// writeBlockLocked 要求调用方持有 mu。
func (r *BlockStream) writeBlockLocked(block chat.Block) error {
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
	r.mu.Lock()
	defer r.mu.Unlock()
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

// flushPending 将累积的文本拼接结果作为一个 TextBlock 收集。要求调用方持有 mu。
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
