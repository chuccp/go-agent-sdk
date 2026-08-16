package chat

import (
	"errors"
	"testing"
)

// drainBlocks 取回 BlockStream 中的全部内容 block（不含 usage/stop_reason 元数据块）。
func drainBlocks(t *testing.T, stream *BlockStream) []Block {
	t.Helper()
	return stream.ReadBlocks()
}

// ── Write：Stream 项 → Block 组装 ──

func TestBlockStream_Write_TextStream(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&Start{})
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "Hello "})
	stream.Write(&Delta{Content: "World"})

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "Hello World" {
		t.Errorf("expected 'Hello World', got %v", blocks[0])
	}
}

func TestBlockStream_Write_ThinkingStream(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&ThinkingBlockStart{})
	stream.Write(&Delta{Content: "let me think..."})

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*ThinkingBlock)
	if !ok || tb.Thinking != "let me think..." {
		t.Errorf("expected 'let me think...', got %v", blocks[0])
	}
}

func TestBlockStream_Write_EmptyThinkingSkipped(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&ThinkingBlockStart{})
	// 无增量：空 thinking block 应被跳过
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "text"})

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (empty thinking skipped), got %d", len(blocks))
	}
	if blocks[0].Type() != BlockTypeText {
		t.Errorf("expected text block, got %s", blocks[0].Type())
	}
}

func TestBlockStream_Write_ToolUseStream(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "my_tool"})
	stream.Write(&Delta{Content: `{"cmd":"ls"}`})

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool_use block, got %d", len(blocks))
	}
	tub, ok := blocks[0].(*ToolUseBlock)
	if !ok {
		t.Fatalf("expected *ToolUseBlock, got %T", blocks[0])
	}
	if tub.ID != "tu_1" || tub.Name != "my_tool" {
		t.Errorf("id/name mismatch: %s/%s", tub.ID, tub.Name)
	}
	if tub.Input == nil || tub.Input.GetString("cmd") != "ls" {
		t.Errorf("input JSON not parsed: %v", tub.Input)
	}
}

func TestBlockStream_Write_MultipleBlocks(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&ThinkingBlockStart{})
	stream.Write(&Delta{Content: "hmm"})
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "answer"})
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "tool"})
	stream.Write(&Delta{Content: `{}`})

	blocks := drainBlocks(t, stream)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0].Type() != BlockTypeThinking ||
		blocks[1].Type() != BlockTypeText ||
		blocks[2].Type() != BlockTypeToolUse {
		t.Errorf("block types mismatch: %v", blocks)
	}
}

// ── StopReason / Usage ──

func TestBlockStream_StopReasonAndUsage(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.StopReason(StopReasonToolUse)
	stream.Usage(&Usage{InputTokens: 10, OutputTokens: 20})

	if stream.GetStopReason() != StopReasonToolUse {
		t.Errorf("expected tool_use, got %s", stream.GetStopReason())
	}
	if stream.GetUsage().InputTokens != 10 || stream.GetUsage().OutputTokens != 20 {
		t.Errorf("usage mismatch: %+v", stream.GetUsage())
	}

	// 重复上报覆盖（Anthropic message_start/message_delta 两次上报 usage）
	stream.Usage(&Usage{InputTokens: 10, OutputTokens: 35})
	if stream.GetUsage().OutputTokens != 35 {
		t.Errorf("usage should be overwritten, got %+v", stream.GetUsage())
	}

	// 元数据 block 不进入内容列表，stop_reason 经 GetStopReason 取回
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "hi"})
	blocks := stream.ReadBlocks()
	if stream.GetStopReason() != StopReasonToolUse {
		t.Errorf("expected tool_use, got %s", stream.GetStopReason())
	}
	if len(blocks) != 1 || blocks[0].Type() != BlockTypeText {
		t.Errorf("metadata blocks should not leak into content: %v", blocks)
	}
}

// TestBlockStream_MetadataDefaults 验证未上报元数据时的默认值：
// stop_reason 默认 end_turn，usage 默认 nil。
func TestBlockStream_MetadataDefaults(t *testing.T) {
	stream := NewBlockStream(nil)
	if stream.GetStopReason() != StopReasonEndTurn {
		t.Errorf("expected default end_turn, got %s", stream.GetStopReason())
	}
	if stream.GetUsage() != nil {
		t.Errorf("expected nil usage when not reported, got %+v", stream.GetUsage())
	}
	if stream.GetStopReason() != StopReasonEndTurn {
		t.Errorf("expected default end_turn, got %s", stream.GetStopReason())
	}
}

// ── 工具路径：WriteBlock 文本拼接 / WriteEvent 流式回显 / WriteErrorText 错误文本 ──

func TestBlockStream_WriteBlock_TextCoalescing(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.WriteBlock(NewTextBlock("hello "))
	stream.WriteBlock(NewTextBlock("world"))

	blocks := stream.ReadBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced block, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "hello world" {
		t.Errorf("expected 'hello world', got %v", blocks[0])
	}
}

func TestBlockStream_WriteEvent_EmitsChunkAndCollects(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.WriteEvent("line1\n")
	stream.WriteEvent("line2\n")

	// 每段流式输出都实时推送了 chunk 事件
	if len(recv.events) != 2 {
		t.Fatalf("expected 2 chunk events, got %d", len(recv.events))
	}
	for i, want := range []string{"line1\n", "line2\n"} {
		if recv.events[i].EventType != EventTypeChunk || recv.events[i].Content != want {
			t.Errorf("event[%d] expected chunk %q, got type=%s content=%q",
				i, want, recv.events[i].EventType, recv.events[i].Content)
		}
	}

	// 同时进入收集列表（连续文本拼接为一块）
	blocks := stream.ReadBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced block, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "line1\nline2\n" {
		t.Errorf("expected 'line1\\nline2\\n', got %v", blocks[0])
	}
}

func TestBlockStream_WriteEvent_EmptyIgnored(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.WriteEvent("")

	if len(recv.events) != 0 {
		t.Errorf("empty content should not emit event, got %d", len(recv.events))
	}
	if blocks := stream.ReadBlocks(); len(blocks) != 0 {
		t.Errorf("empty content should not collect block, got %d", len(blocks))
	}
}

func TestBlockStream_WriteEvent_NilReceiver(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.WriteEvent("no receiver") // 不应 panic

	if blocks := stream.ReadBlocks(); len(blocks) != 1 {
		t.Fatalf("expected 1 block even without receiver, got %d", len(blocks))
	}
}

// TestBlockStream_WriteErrorText 验证错误以普通文本写入（仅回传给模型）：
// 与正文拼接（参与连续 TextBlock 合并）；nil 错误忽略。
func TestBlockStream_WriteErrorText(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.WriteBlock(NewTextBlock("partial: "))
	stream.WriteErrorText(errors.New("boom"))
	stream.WriteErrorText(nil) // nil 忽略

	blocks := stream.ReadBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced text block, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "partial: boom" {
		t.Errorf("expected coalesced 'partial: boom', got %v", blocks[0])
	}
}

// ── GetBlock / GetFirstBlock：按类型取回 ──

func TestBlockStream_GetBlock_ByType(t *testing.T) {
	stream := NewBlockStream(nil)
	// LLM 路径：文本 + tool_use 组装
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "answer"})
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "tool"})
	stream.Write(&Delta{Content: `{}`})
	// 另上报元数据（GetBlock 可按类型取回，但 ReadBlocks 不含）
	stream.StopReason(StopReasonToolUse)
	stream.Usage(&Usage{InputTokens: 1, OutputTokens: 2})

	texts := stream.GetBlock(BlockTypeText)
	if len(texts) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(texts))
	}
	if tb, ok := texts[0].(*TextBlock); !ok || tb.Text != "answer" {
		t.Errorf("expected text 'answer', got %v", texts[0])
	}
	if toolUses := stream.GetBlock(BlockTypeToolUse); len(toolUses) != 1 {
		t.Errorf("expected 1 tool_use block, got %d", len(toolUses))
	}
	// 元数据块可按类型取回（与 GetUsage/GetStopReason 同源）
	if got := stream.GetBlock(BlockTypeUsage); len(got) != 1 {
		t.Errorf("expected 1 usage block, got %v", got)
	}
	if got := stream.GetBlock(BlockTypeStopReason); len(got) != 1 {
		t.Errorf("expected 1 stop_reason block, got %v", got)
	}
	// 但 ReadBlocks 内容列表不含元数据
	for _, b := range stream.ReadBlocks() {
		if b.Type() == BlockTypeUsage || b.Type() == BlockTypeStopReason {
			t.Errorf("metadata should not enter ReadBlocks: %v", b)
		}
	}
	// 无匹配类型返回空切片
	if got := stream.GetBlock(BlockTypeImage); len(got) != 0 {
		t.Errorf("expected empty for image, got %v", got)
	}
}

func TestBlockStream_GetFirstBlock(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.WriteBlock(NewTextBlock("first"))
	stream.WriteBlock(NewToolUseBlock("tu_1", "tool", nil))
	stream.WriteBlock(NewTextBlock("second"))

	first := stream.GetFirstBlock(BlockTypeText)
	if tb, ok := first.(*TextBlock); !ok || tb.Text != "first" {
		t.Errorf("expected first text block, got %v", first)
	}
	tu := stream.GetFirstBlock(BlockTypeToolUse)
	if tub, ok := tu.(*ToolUseBlock); !ok || tub.ID != "tu_1" {
		t.Errorf("expected tool_use tu_1, got %v", tu)
	}
	if stream.GetFirstBlock(BlockTypeImage) != nil {
		t.Error("expected nil for absent type")
	}
}

// ── Close：幂等 / flush 未完成 block ──

func TestBlockStream_Close_Idempotent(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "x"})

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(blocks))
	}
}

func TestBlockStream_Close_FlushesActiveBlock(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "pending text"})
	// 无下一个 BlockStart，Close 应 flush 当前组装中的 block

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block from flush, got %d", len(blocks))
	}
}

// ── ReadBlock：空流 ──

func TestBlockStream_ReadBlock_Empty(t *testing.T) {
	stream := NewBlockStream(nil)
	blocks := stream.ReadBlocks()
	if len(blocks) != 0 {
		t.Errorf("expected empty blocks, got %v", blocks)
	}
}

// ── EventReceiver：增量事件外发 ──

type testReceiver struct {
	events []*ClientEvent
}

func (r *testReceiver) AddEvent(evt *ClientEvent) {
	r.events = append(r.events, evt)
}

func TestBlockStream_EmitsChunkEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "hello"})

	if len(recv.events) == 0 {
		t.Fatal("expected at least 1 event emitted")
	}
	chunk := recv.events[0]
	if chunk.EventType != EventTypeChunk || chunk.Content != "hello" {
		t.Errorf("expected chunk 'hello', got type=%s content=%q", chunk.EventType, chunk.Content)
	}
}

func TestBlockStream_EmitsThinkingEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.Write(&ThinkingBlockStart{})
	stream.Write(&Delta{Content: "hmm"})

	if len(recv.events) == 0 {
		t.Fatal("expected thinking event")
	}
	evt := recv.events[0]
	if evt.EventType != EventTypeThinking || evt.Content != "hmm" {
		t.Errorf("expected thinking 'hmm', got type=%s content=%q", evt.EventType, evt.Content)
	}
}

func TestBlockStream_ToolUseDeltaNotEmitted(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "tool"})
	stream.Write(&Delta{Content: `{"a":1}`})

	if len(recv.events) != 0 {
		t.Errorf("tool_use delta should not emit client events, got %d", len(recv.events))
	}
}
