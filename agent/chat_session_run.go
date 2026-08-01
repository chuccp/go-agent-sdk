package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
)

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
		c.blocks = append(c.blocks, c.current.finalize())
		c.current = nil
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

// streamResponse 消费 SSE 流，返回所有 content block、stop_reason 和起始事件位置。
// 同时在消费过程中通过 addEvent 向外广播文本增量。
func (s *chatSession) streamResponse(resp *chat.Response) (blocks chat.Blocks, stopReason chat.StopReason, start uint, err error) {
	start = s.events.Position()
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
				s.addEvent(chat.NewChunkEvent(e.Delta.Text, s.id))
			case chat.DeltaTypeThinking:
				collector.appendThinking(e.Delta.Thinking)
				s.addEvent(chat.NewThinkingEvent(e.Delta.Thinking, s.id))
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
			clientEvt := chat.NewErrorEvent(e.Error())
			clientEvt.Done = true
			s.addEvent(clientEvt)
			return collector.take(), stopReason, start, e.Err

		case chat.EventTypeMessageStop:
			return collector.take(), stopReason, start, nil
		}
	}

	// 流异常中断（ReadEvent 返回 nil 但未收到 MessageStop）
	return collector.take(), stopReason, start, nil
}

// executeTools 执行 tool_use blocks 中的工具，将 tool_result 作为 user 消息追加到历史。
func (s *chatSession) executeTools(blocks chat.Blocks) {
	start := s.events.Position()
	toolResults := make(chat.Blocks, 0, len(blocks))
	for _, block := range blocks {
		tu, ok := block.(*chat.ToolUseBlock)
		if !ok {
			continue
		}
		exec, exists := s.toolExecutors[tu.Name]
		if !exists {
			toolResults = append(toolResults, chat.NewToolResultBlock(
				tu.ID,
				chat.Blocks{chat.NewTextBlock(fmt.Sprintf("未知工具: %s", tu.Name))},
			))
			continue
		}

		args, _ := tu.Input.(map[string]any)
		output, execErr := exec.Execute(args)

		s.addEvent(chat.NewToolExecutionEvent(tu.Name, output, s.id))

		resultText := output
		if execErr != nil {
			resultText = fmt.Sprintf("错误: %v", execErr)
		}
		toolResults = append(toolResults, chat.NewToolResultBlock(
			tu.ID,
			chat.Blocks{chat.NewTextBlock(resultText)},
		))
	}
	// tool_result 作为 user 消息入历史
	msg := &chat.Message{Role: chat.RoleUser, Content: toolResults, Start: start}
	s.events.AppendHistory(msg)
	s.events.SetLastHistoryOffset(s.events.Position() - start)
}

func (s *chatSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		if s.cancel != nil {
			s.cancel()
		}
	}
}

func (s *chatSession) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
	}()

	// 记录本轮开始前历史长度，用于计算新增消息数
	historyBefore := s.events.HistoryLen()

	for {
		select {
		case <-ctx.Done():
			s.saveAndReset(historyBefore)
			return
		default:
		}

		messages := s.build()
		if messages == nil {
			return
		}

		provider := s.registry.DefaultProvider()
		resp, err := s.registry.ChatWithStream(ctx, provider, messages)
		if err != nil {
			evt := chat.NewErrorEvent(err.Error())
			evt.Done = true
			s.addEvent(evt)
			s.saveAndReset(historyBefore)
			return
		}

		blocks, stopReason, start, err := s.streamResponse(resp)
		if err != nil {
			s.saveAndReset(historyBefore)
			return
		}

		// assistant 消息入历史（带事件流区间）
		assistantMsg := &chat.Message{Role: chat.RoleAssistant, Content: blocks, Start: start}
		s.events.AppendHistory(assistantMsg)
		s.events.SetLastHistoryOffset(s.events.Position() - start)

		switch stopReason {
		case chat.StopReasonToolUse:
			s.executeTools(blocks)
			continue

		default: // end_turn
			s.addEvent(chat.NewDoneEvent(s.id))
			s.saveAndReset(historyBefore)
			return
		}
	}
}

// saveAndReset 持久化本轮新增消息并清空事件缓冲区。
func (s *chatSession) saveAndReset(historyBefore int) {
	newCount := s.events.HistoryLen() - historyBefore
	if err := s.events.SaveHistory(newCount); err != nil {
		log.Printf("[chatSession] save history failed: %v", err)
	}
	s.events.Reset()
}
