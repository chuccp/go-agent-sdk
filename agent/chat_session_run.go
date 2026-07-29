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
	block   chat.ContentBlock
	rawJSON strings.Builder // tool_use 类型的 input_json_delta 累积
}

// finalize 完成当前 block 的构建：解析 tool_use 的 JSON 入参，返回完整 block。
func (b *blockBuilder) finalize() chat.ContentBlock {
	if b.block.Type == chat.ContentTypeToolUse && b.rawJSON.Len() > 0 {
		var input any
		if err := json.Unmarshal([]byte(b.rawJSON.String()), &input); err != nil {
			log.Printf("tool_use JSON 解析失败: %v, raw=%s", err, b.rawJSON.String())
		}
		b.block.Input = input
	}
	return b.block
}

// blockCollector 管理流式 content block 的累积：持有当前正在构建的 block 和已完成的 block 列表。
type blockCollector struct {
	current *blockBuilder
	blocks  []chat.ContentBlock
}

// start 开始构建一个新的 content block（自动 flush 上一个）。
func (c *blockCollector) start(block chat.ContentBlock) {
	c.flush()
	c.current = &blockBuilder{block: block}
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
		c.current.block.Text += text
	}
}

// appendJSON 向当前 block 追加 input_json_delta 片段。
func (c *blockCollector) appendJSON(fragment string) {
	if c.current != nil {
		c.current.rawJSON.WriteString(fragment)
	}
}

// take 返回所有已累积的 block（先 flush 当前未完成的）。
func (c *blockCollector) take() []chat.ContentBlock {
	c.flush()
	return c.blocks
}

// streamResponse 消费 SSE 流，返回所有 content block 和 stop_reason。
// 同时在消费过程中通过 addEvent 向外广播文本增量。
func (s *chatSession) streamResponse(resp *chat.Response) (blocks []chat.ContentBlock, stopReason chat.StopReason, err error) {
	var collector blockCollector

	for evt := resp.ReadEvent(); evt != nil; evt = resp.ReadEvent() {
		switch e := evt.(type) {
		case *chat.ContentBlockStartEvent:
			collector.start(chat.ContentBlock{
				Type: e.ContentBlock.Type,
				ID:   e.ContentBlock.ID,
				Name: e.ContentBlock.Name,
			})

		case *chat.ContentBlockDeltaEvent:
			switch e.Delta.Type {
			case "text_delta":
				collector.appendText(e.Delta.Text)
				s.addEvent(NewChunkEvent(e.Delta.Text, s.id))
			case "input_json_delta":
				collector.appendJSON(e.Delta.PartialJSON)
			}

		case *chat.ContentBlockStopEvent:
			collector.flush()

		case *chat.MessageDeltaEvent:
			stopReason = e.StopReason

		case *chat.ErrorEvent:
			evt := NewErrorEvent(e.Error()); evt.Done = true; s.addEvent(evt)
			return collector.take(), stopReason, e.Err

		case *chat.MessageStopEvent:
			return collector.take(), stopReason, nil
		}
	}

	// 流异常中断（ReadEvent 返回 nil 但未收到 MessageStop）
	return collector.take(), stopReason, nil
}

// executeTools 执行 tool_use blocks 中的工具，返回 tool_result blocks。
func (s *chatSession) executeTools(blocks []chat.ContentBlock) []chat.ContentBlock {
	toolResults := make([]chat.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != chat.ContentTypeToolUse {
			continue
		}
		exec, ok := s.toolExecutors[block.Name]
		if !ok {
			toolResults = append(toolResults, chat.ContentBlock{
				Type:      chat.ContentTypeToolResult,
				ToolUseID: block.ID,
				Content:   []chat.ContentBlock{{Type: chat.ContentTypeText, Text: fmt.Sprintf("未知工具: %s", block.Name)}},
			})
			continue
		}

		args, _ := block.Input.(map[string]any)
		output, execErr := exec.Execute(args)

		s.addEvent(NewChunkEvent(fmt.Sprintf("\n🔧 执行命令: %v\n%s\n", args, output), s.id))

		resultText := output
		if execErr != nil {
			resultText = fmt.Sprintf("错误: %v", execErr)
		}
		toolResults = append(toolResults, chat.ContentBlock{
			Type:      chat.ContentTypeToolResult,
			ToolUseID: block.ID,
			Content:   []chat.ContentBlock{{Type: chat.ContentTypeText, Text: resultText}},
		})
	}
	return toolResults
}

// textBlocks 从 blocks 中提取纯文本类型的 block。
func textBlocks(blocks []chat.ContentBlock) []chat.ContentBlock {
	result := make([]chat.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == chat.ContentTypeText && block.Text != "" {
			result = append(result, block)
		}
	}
	return result
}

func (s *chatSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isRun {
		if s.cancel != nil {
			s.cancel()
		}
	}
}

func (s *chatSession) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.isRun = false
		s.cancel = nil
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		default:

		}
		messages := s.build()
		if messages == nil {
			return
		}
		provider := s.provider
		if provider == "" {
			provider = s.unifiedChatService.DefaultProvider()
		}
		resp, err := s.unifiedChatService.ChatWithStream(ctx, provider, messages)
		if err != nil {
			evt := NewErrorEvent(err.Error()); evt.Done = true; s.addEvent(evt)
			return
		}

		blocks, stopReason, err := s.streamResponse(resp)
		if err != nil {
			return
		}

		switch stopReason {
		case chat.StopReasonToolUse:
			s.history = append(s.history, chat.Message{
				Role:    chat.RoleAssistant,
				Content: blocks,
			})

			toolResults := s.executeTools(blocks)
			s.history = append(s.history, chat.Message{
				Role:    chat.RoleUser,
				Content: toolResults,
			})

			continue

		default: // end_turn
			s.history = append(s.history, chat.Message{
				Role:    chat.RoleAssistant,
				Content: textBlocks(blocks),
			})
			s.addEvent(NewDoneEvent(s.id))
			return
		}
	}
}
