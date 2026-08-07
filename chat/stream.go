package chat

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/chuccp/go-agent-sdk/util"
)

// -------- StreamWriter / EventReceiver --------

// EventReceiver 事件接收方：流式过程中产生的客户端推送事件（文本/思考链增量等）
// 通过它向外发送，典型实现是 SessionContext（传入其 AddEvent 接收）。
type EventReceiver interface {
	AddEvent(event *ClientEvent)
}

// StreamWriter 是流式内容的生产方接口（类似 http.ResponseWriter）。
// 生产方（LLM provider / 工具）通过它写入协议事件或内容块，
// Block 的拼接与组合由 BlockStream 内部完成，写入方无需关心。
type StreamWriter interface {
	Write(event Event) error
	WriteBlock(block Block) error
	WriteError(err error)
	Close()
}

// -------- BlockStream --------

// BlockStream 是一条 Block 流管道：内部维护一条 Block 队列。
// 生产方写入协议事件（Write）或内容块（WriteBlock），
// BlockStream 负责将增量拼接、组合为完整的 Block；
// 消费方循环调用 ReadBlock() 读取，nil 表示流结束。
// 既用于 LLM 流式响应，也用于工具执行输出的收集。
type BlockStream struct {
	blocks     *util.Queue[Block]
	receiver   EventReceiver
	assembler  blockAssembler  // 协议事件（start/delta/stop）→ 完整 Block
	pending    strings.Builder // 连续 TextBlock 的拼接缓冲
	hasPending bool
	stopReason StopReason
	err        error
	closed     bool
	sessionId  string
}

// NewBlockStream 创建一条 Block 流。receiver 为事件接收方（如 SessionContext），nil 表示不外发事件。
func NewBlockStream(sessionId string, receiver EventReceiver) *BlockStream {
	return &BlockStream{
		blocks:    util.NewQueue[Block](),
		receiver:  receiver,
		sessionId: sessionId,
	}
}

// Write 写入一个协议层事件：增量事件在内部组装为 Block 入队，
// 文本/思考链增量同时通过 receiver 向外推送。
func (r *BlockStream) Write(event Event) error {
	switch e := event.(type) {
	case *ContentBlockStartEvent:
		var id, name string
		if tu, ok := e.ContentBlock.(*ToolUseBlock); ok {
			id = tu.ID
			name = tu.Name
		}
		r.assembler.start(e.ContentBlock.Type(), id, name)

	case *ContentBlockDeltaEvent:
		switch e.Delta.Type {
		case DeltaTypeText:
			r.assembler.appendText(e.Delta.Text)
			r.emit(NewChunkEvent(e.Delta.Text, ""))
		case DeltaTypeThinking:
			r.assembler.appendThinking(e.Delta.Thinking)
			r.emit(NewThinkingEvent(e.Delta.Thinking, ""))
		case DeltaTypeInputJSON:
			r.assembler.appendJSON(e.Delta.PartialJSON)
		}

	case *ContentBlockStopEvent:
		if b := r.assembler.flush(); b != nil {
			return r.blocks.Offer(b)
		}

	case *MessageDeltaEvent:
		r.stopReason = e.StopReason

	case *ErrorEvent:
		r.WriteError(e.Err)
	}
	return nil
}

// WriteBlock 写入一个内容块：连续的 TextBlock 会被拼接为一个，
// 遇到其他类型（或 Close）时输出拼接结果，其余类型直接入队。
func (r *BlockStream) WriteBlock(block Block) error {
	if tb, ok := block.(*TextBlock); ok {
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
func (r *BlockStream) ReadBlock() Block {
	evt, ok := r.blocks.Dequeue()
	if !ok {
		return nil
	}
	return evt
}

// StopReason 返回模型停止生成的原因（流结束后有效）。
func (r *BlockStream) StopReason() StopReason { return r.stopReason }

// Err 返回流处理过程中发生的错误（流结束后检查）。
func (r *BlockStream) Err() error { return r.err }

// flushPending 将累积的文本拼接结果作为一个 TextBlock 入队。
func (r *BlockStream) flushPending() error {
	if !r.hasPending {
		return nil
	}
	r.hasPending = false
	b := &TextBlock{Text: r.pending.String()}
	r.pending.Reset()
	return r.blocks.Offer(b)
}

// emit 通过 receiver 向外推送客户端事件（SessionId 由 BlockStream 补齐）。
func (r *BlockStream) emit(evt *ClientEvent) {
	if r.receiver != nil {
		evt.SessionId = r.sessionId
		r.receiver.AddEvent(evt)
	}
}

// Collect 消费所有 Block 并聚合成文本和工具调用结果。
// 这是一个便捷方法，适用于不需要逐块处理的简单场景。
func (r *BlockStream) Collect() (text string, toolCalls Blocks) {
	for b := r.ReadBlock(); b != nil; b = r.ReadBlock() {
		switch v := b.(type) {
		case *TextBlock:
			text += v.Text
		case *ToolUseBlock:
			toolCalls = append(toolCalls, v)
		}
	}
	return
}

// -------- Block 组装器（协议事件 → 完整 Block） --------

// blockAssembler 在流式接收过程中累积构建一个 content block。
type blockAssembler struct {
	blockType ContentType
	text      strings.Builder
	thinking  strings.Builder
	rawJSON   strings.Builder
	id        string
	name      string
	active    bool
}

// start 开始构建一个新的 content block（自动 flush 上一个）。
func (a *blockAssembler) start(blockType ContentType, id, name string) {
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
func (a *blockAssembler) flush() Block {
	if !a.active {
		return nil
	}
	a.active = false
	defer a.text.Reset()
	defer a.thinking.Reset()
	defer a.rawJSON.Reset()

	switch a.blockType {
	case ContentTypeText:
		return NewTextBlock(a.text.String())
	case ContentTypeThinking:
		// 跳过空的 thinking block（避免产生 {"type":"thinking"} 脏数据）
		if a.thinking.Len() == 0 {
			return nil
		}
		return NewThinkingBlock(a.thinking.String())
	case ContentTypeToolUse:
		var input any
		if a.rawJSON.Len() > 0 {
			if err := json.Unmarshal([]byte(a.rawJSON.String()), &input); err != nil {
				log.Printf("tool_use JSON 解析失败: %v, raw=%s", err, a.rawJSON.String())
			}
		}
		return NewToolUseBlock(a.id, a.name, input)
	default:
		return NewTextBlock(a.text.String())
	}
}
