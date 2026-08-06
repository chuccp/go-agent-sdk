package agent

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
)

// streamResponse 消费 SSE 流，返回所有 content block 和 stop_reason。
// 同时在消费过程中通过 AddEvent 向外广播文本增量。
func (c *SessionContext) streamResponse(resp *chat.Response) (blocks chat.Blocks, stopReason chat.StopReason, err error) {
	var collector blockCollector

	for evt := resp.ReadEvent(); evt != nil; evt = resp.ReadEvent() {
		switch evt.Type() {
		case chat.EventTypeContentBlockStart:
			e := evt.(*chat.ContentBlockStartEvent)
			var id, name string
			if tu, ok := e.ContentBlock.(*chat.ToolUseBlock); ok {
				id = tu.ID
				name = tu.Name
			}
			collector.start(e.ContentBlock.Type(), id, name)

		case chat.EventTypeContentBlockDelta:
			e := evt.(*chat.ContentBlockDeltaEvent)
			switch e.Delta.Type {
			case chat.DeltaTypeText:
				collector.appendText(e.Delta.Text)
				c.AddEvent(chat.NewChunkEvent(e.Delta.Text, c.sessionId))
			case chat.DeltaTypeThinking:
				collector.appendThinking(e.Delta.Thinking)
				c.AddEvent(chat.NewThinkingEvent(e.Delta.Thinking, c.sessionId))
			case chat.DeltaTypeInputJSON:
				collector.appendJSON(e.Delta.PartialJSON)
			}

		case chat.EventTypeContentBlockStop:
			collector.flush()

		case chat.EventTypeMessageDelta:
			e := evt.(*chat.MessageDeltaEvent)
			stopReason = e.StopReason

		case chat.EventTypeError:
			e := evt.(*chat.ErrorEvent)
			return collector.take(), stopReason, e.Err

		case chat.EventTypeMessageStop:
			return collector.take(), stopReason, nil
		}
	}

	// 流异常中断（ReadEvent 返回 nil 但未收到 MessageStop）
	return collector.take(), stopReason, nil
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

// blockBuilder 在流式接收过程中累积构建一个 content block。
type blockBuilder struct {
	blockType chat.ContentType
	text      strings.Builder
	thinking  strings.Builder
	rawJSON   strings.Builder
	id        string
	name      string
}

// finalize 完成当前 block 的构建，返回具体的 Block 实现。
func (b *blockBuilder) finalize() chat.Block {
	switch b.blockType {
	case chat.ContentTypeText:
		return chat.NewTextBlock(b.text.String())
	case chat.ContentTypeThinking:
		return chat.NewThinkingBlock(b.thinking.String())
	case chat.ContentTypeToolUse:
		var input any
		if b.rawJSON.Len() > 0 {
			if err := json.Unmarshal([]byte(b.rawJSON.String()), &input); err != nil {
				log.Printf("tool_use JSON 解析失败: %v, raw=%s", err, b.rawJSON.String())
			}
		}
		return chat.NewToolUseBlock(b.id, b.name, input)
	default:
		return chat.NewTextBlock(b.text.String())
	}
}

// blockCollector 管理流式 content block 的累积。
type blockCollector struct {
	current *blockBuilder
	blocks  chat.Blocks
}

// start 开始构建一个新的 content block（自动 flush 上一个）。
func (c *blockCollector) start(blockType chat.ContentType, id, name string) {
	c.flush()
	c.current = &blockBuilder{blockType: blockType, id: id, name: name}
}

// flush 完成当前 block 并将其加入列表。
func (c *blockCollector) flush() {
	if c.current != nil {
		b := c.current.finalize()
		c.current = nil
		// 跳过空的 thinking block（避免产生 {"type":"thinking"} 脏数据）
		if tb, ok := b.(*chat.ThinkingBlock); ok && tb.Thinking == "" {
			return
		}
		c.blocks = append(c.blocks, b)
	}
}

// appendText 向当前 block 追加文本增量。
func (c *blockCollector) appendText(text string) {
	if c.current != nil {
		c.current.text.WriteString(text)
	}
}

// appendThinking 向当前 block 追加思考链增量。
func (c *blockCollector) appendThinking(thinking string) {
	if c.current != nil {
		c.current.thinking.WriteString(thinking)
	}
}

// appendJSON 向当前 block 追加 input_json_delta 片段。
func (c *blockCollector) appendJSON(fragment string) {
	if c.current != nil {
		c.current.rawJSON.WriteString(fragment)
	}
}

// take 返回所有已累积的 block（先 flush 当前未完成的）。
func (c *blockCollector) take() chat.Blocks {
	c.flush()
	return c.blocks
}
