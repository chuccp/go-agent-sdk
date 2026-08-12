package agent

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// -------- BlockStream --------

// BlockStream 是一条 Block 流管道：内部维护一条 Block 队列。
// 生产方写入协议事件（Write）或内容块（WriteBlock），
// BlockStream 负责将增量拼接、组合为完整的 Block；
// 消费方循环调用 ReadBlock() 读取，nil 表示流结束。
// 既用于 LLM 流式响应，也用于工具执行输出的收集。
type BlockStream struct {
	blocks     *util.Queue[chat.Block]
	receiver   chat.EventReceiver
	assembler  blockAssembler  // 协议事件（start/delta/stop）→ 完整 Block
	pending    strings.Builder // 连续 TextBlock 的拼接缓冲
	hasPending bool
	stopReason chat.StopReason
	err        error
	closed     bool
}

// NewBlockStream 创建一条 Block 流。receiver 为事件接收方（如 SessionContext），nil 表示不外发事件。
func NewBlockStream(receiver chat.EventReceiver) *BlockStream {
	return &BlockStream{
		blocks:   util.NewQueue[chat.Block](),
		receiver: receiver,
	}
}

// Write 写入一个协议层事件：增量事件在内部组装为 Block 入队，
// 文本/思考链增量同时通过 receiver 向外推送。
func (r *BlockStream) Write(event chat.Event) error {
	switch e := event.(type) {
	case *chat.ContentBlockStartEvent:
		var id, name string
		if tu, ok := e.ContentBlock.(*chat.ToolUseBlock); ok {
			id = tu.ID
			name = tu.Name
		}
		r.assembler.start(e.ContentBlock.Type(), id, name)

	case *chat.ContentBlockDeltaEvent:
		switch e.Delta.Type {
		case chat.DeltaTypeText:
			r.assembler.appendText(e.Delta.Text)
			r.emit(chat.NewChunkEvent(e.Delta.Text))
		case chat.DeltaTypeThinking:
			r.assembler.appendThinking(e.Delta.Thinking)
			r.emit(chat.NewThinkingEvent(e.Delta.Thinking))
		case chat.DeltaTypeInputJSON:
			r.assembler.appendJSON(e.Delta.PartialJSON)
		}

	case *chat.ContentBlockStopEvent:
		if b := r.assembler.flush(); b != nil {
			return r.blocks.Offer(b)
		}

	case *chat.MessageDeltaEvent:
		r.stopReason = e.StopReason

	case *chat.ErrorEvent:
		r.WriteError(e.Err)
	}
	return nil
}

// WriteBlock 写入一个内容块：连续的 TextBlock 会被拼接为一个，
// 遇到其他类型（或 Close）时输出拼接结果，其余类型直接入队。
func (r *BlockStream) WriteBlock(block chat.Block) error {
	if tb, ok := block.(*chat.TextBlock); ok {
		r.pending.WriteString(tb.Text)
		r.hasPending = true
		return nil
	}
	if err := r.flushPending(); err != nil {
		return err
	}
	return r.blocks.Offer(block)
}

// WriteError 记录错误并结束流，ReadBlock 消费完剩余内容后返回 nil。
func (r *BlockStream) WriteError(err error) {
	r.err = err
	r.Close()
}

// Close 关闭流：输出未完成的拼接内容后关闭队列。幂等，多次调用安全。
func (r *BlockStream) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.flushPending()
	r.blocks.Close()
}

// ReadBlock 读取下一个已组合完成的 Block，nil 表示流结束。
func (r *BlockStream) ReadBlock() chat.Block {
	evt, ok := r.blocks.Dequeue()
	if !ok {
		return nil
	}
	return evt
}

// StopReason 返回模型停止生成的原因（流结束后有效）。
func (r *BlockStream) StopReason() chat.StopReason { return r.stopReason }

// Err 返回流处理过程中发生的错误（流结束后检查）。
func (r *BlockStream) Err() error { return r.err }

// flushPending 将累积的文本拼接结果作为一个 TextBlock 入队。
func (r *BlockStream) flushPending() error {
	if !r.hasPending {
		return nil
	}
	r.hasPending = false
	b := chat.NewTextBlock(r.pending.String())
	r.pending.Reset()
	return r.blocks.Offer(b)
}

// emit 通过 receiver 向外推送客户端事件（SessionId 由 BlockStream 补齐）。
func (r *BlockStream) emit(evt *chat.ClientEvent) {
	if r.receiver != nil {
		r.receiver.AddEvent(evt)
	}
}

// Collect 消费所有 Block 并聚合成文本和工具调用结果。
// 这是一个便捷方法，适用于不需要逐块处理的简单场景。
func (r *BlockStream) Collect() (text string, toolCalls chat.Blocks) {
	for b := r.ReadBlock(); b != nil; b = r.ReadBlock() {
		switch v := b.(type) {
		case *chat.TextBlock:
			text += v.Text
		case *chat.ToolUseBlock:
			toolCalls = append(toolCalls, v)
		}
	}
	return
}

// -------- Block 组装器（协议事件 → 完整 Block） --------

// blockAssembler 在流式接收过程中累积构建一个 content block。
type blockAssembler struct {
	blockType chat.ContentType
	text      strings.Builder
	thinking  strings.Builder
	rawJSON   strings.Builder
	id        string
	name      string
	active    bool
}

// start 开始构建一个新的 content block（自动 flush 上一个）。
func (a *blockAssembler) start(blockType chat.ContentType, id, name string) {
	a.flush()
	a.blockType = blockType
	a.id = id
	a.name = name
	a.active = true
}

// appendText 向当前 block 追加文本增量。
func (a *blockAssembler) appendText(text string) {
	if a.active {
		a.text.WriteString(text)
	}
}

// appendThinking 向当前 block 追加思考链增量。
func (a *blockAssembler) appendThinking(thinking string) {
	if a.active {
		a.thinking.WriteString(thinking)
	}
}

// appendJSON 向当前 block 追加 input_json_delta 片段。
func (a *blockAssembler) appendJSON(fragment string) {
	if a.active {
		a.rawJSON.WriteString(fragment)
	}
}

// flush 完成当前 block 并返回具体的 Block 实现（无活动 block 时返回 nil）。
func (a *blockAssembler) flush() chat.Block {
	if !a.active {
		return nil
	}
	a.active = false
	defer a.text.Reset()
	defer a.thinking.Reset()
	defer a.rawJSON.Reset()

	switch a.blockType {
	case chat.ContentTypeText:
		return chat.NewTextBlock(a.text.String())
	case chat.ContentTypeThinking:
		// 跳过空的 thinking block（避免产生 {"type":"thinking"} 脏数据）
		if a.thinking.Len() == 0 {
			return nil
		}
		return chat.NewThinkingBlock(a.thinking.String())
	case chat.ContentTypeToolUse:
		var input any
		if a.rawJSON.Len() > 0 {
			if err := json.Unmarshal([]byte(a.rawJSON.String()), &input); err != nil {
				log.Printf("tool_use JSON 解析失败: %v, raw=%s", err, a.rawJSON.String())
			}
		}
		return chat.NewToolUseBlock(a.id, a.name, input)
	default:
		return chat.NewTextBlock(a.text.String())
	}
}

// -------- streamResponse / withoutThinking --------

// streamResponse 消费流式响应，返回 BlockStream 已组装完成的所有 content block 和 stop_reason。
// Block 的拼接与组合由 BlockStream 内部完成；文本/思考链增量已在写入时
// 通过 EventSink（AddEvent）向外广播，此处只负责收集结果。
func (c *SessionContext) streamResponse(stream *BlockStream) (blocks chat.Blocks, stopReason chat.StopReason, err error) {
	for b := stream.ReadBlock(); b != nil; b = stream.ReadBlock() {
		blocks = append(blocks, b)
	}
	return blocks, stream.StopReason(), stream.Err()
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
