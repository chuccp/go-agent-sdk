package chat

import (
	"errors"
	"testing"
)

// drainBlocks 消费 StreamWriter 中的所有 block（要求无错误结束）。
func drainBlocks(t *testing.T, stream *StreamWriter) []Block {
	t.Helper()
	var blocks []Block
	for {
		b, err := stream.ReadBlock()
		if b == nil {
			if err != nil {
				t.Fatalf("ReadBlock error: %v", err)
			}
			break
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// ── Write：Stream 项 → Block 组装 ──

func TestStreamWriter_Write_TextStream(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Write(&Start{})
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "Hello "})
	stream.Write(&Delta{Content: "World"})
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "Hello World" {
		t.Errorf("expected 'Hello World', got %v", blocks[0])
	}
}

func TestStreamWriter_Write_ThinkingStream(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Write(&ThinkingBlockStart{})
	stream.Write(&Delta{Content: "let me think..."})
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*ThinkingBlock)
	if !ok || tb.Thinking != "let me think..." {
		t.Errorf("expected 'let me think...', got %v", blocks[0])
	}
}

func TestStreamWriter_Write_EmptyThinkingSkipped(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Write(&ThinkingBlockStart{})
	// 无增量：空 thinking block 应被跳过
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "text"})
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (empty thinking skipped), got %d", len(blocks))
	}
	if blocks[0].Type() != ContentTypeText {
		t.Errorf("expected text block, got %s", blocks[0].Type())
	}
}

func TestStreamWriter_Write_ToolUseStream(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "my_tool"})
	stream.Write(&Delta{Content: `{"cmd":"ls"}`})
	stream.Close()

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
	input, ok := tub.Input.(map[string]any)
	if !ok || input["cmd"] != "ls" {
		t.Errorf("input JSON not parsed: %v", tub.Input)
	}
}

func TestStreamWriter_Write_MultipleBlocks(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Write(&ThinkingBlockStart{})
	stream.Write(&Delta{Content: "hmm"})
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "answer"})
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "tool"})
	stream.Write(&Delta{Content: `{}`})
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0].Type() != ContentTypeThinking ||
		blocks[1].Type() != ContentTypeText ||
		blocks[2].Type() != ContentTypeToolUse {
		t.Errorf("block types mismatch: %v", blocks)
	}
}

// ── StopReason / Usage ──

func TestStreamWriter_StopReasonAndUsage(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.StopReason(StopReasonToolUse)
	stream.Usage(&Usage{InputTokens: 10, OutputTokens: 20})
	stream.Close()

	if stream.GetStopReason() != StopReasonToolUse {
		t.Errorf("expected tool_use, got %s", stream.GetStopReason())
	}
	if stream.GetUsage().InputTokens != 10 || stream.GetUsage().OutputTokens != 20 {
		t.Errorf("usage mismatch: %+v", stream.GetUsage())
	}
}

// ── WriteBlock / WriteError ──

func TestStreamWriter_WriteBlock_Direct(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.WriteBlock(NewTextBlock("hello"))
	stream.WriteBlock(NewToolResultBlock("tu_1", "done"))
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestStreamWriter_WriteError(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.WriteBlock(NewTextBlock("before error"))
	stream.WriteError(errors.New("test error"))
	// WriteError 调用 Close，之后不再写入

	var blocks []Block
	for {
		b, _ := stream.ReadBlock()
		if b == nil {
			break
		}
		blocks = append(blocks, b)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (before error), got %d", len(blocks))
	}
	if stream.Err() == nil {
		t.Error("expected Err() to return error")
	}
}

// ── Close：幂等 / flush 未完成 block ──

func TestStreamWriter_Close_Idempotent(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.WriteBlock(NewTextBlock("x"))
	stream.Close()
	stream.Close() // 幂等
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(blocks))
	}
}

func TestStreamWriter_Close_FlushesActiveBlock(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "pending text"})
	// 无下一个 BlockStart，Close 应 flush 当前组装中的 block
	stream.Close()

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block from flush, got %d", len(blocks))
	}
}

// ── ReadBlock：空流 ──

func TestStreamWriter_ReadBlock_Empty(t *testing.T) {
	stream := NewStreamWriter(nil)
	stream.Close()
	b, err := stream.ReadBlock()
	if b != nil || err != nil {
		t.Errorf("expected (nil, nil) on empty closed stream, got %v, %v", b, err)
	}
}

// ── EventReceiver：增量事件外发 ──

type testReceiver struct {
	events []*ClientEvent
}

func (r *testReceiver) AddEvent(evt *ClientEvent) {
	r.events = append(r.events, evt)
}

func TestStreamWriter_EmitsChunkEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewStreamWriter(recv)
	stream.Write(&TextBlockStart{})
	stream.Write(&Delta{Content: "hello"})
	stream.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected at least 1 event emitted")
	}
	chunk := recv.events[0]
	if chunk.EventType != EventTypeChunk || chunk.Content != "hello" {
		t.Errorf("expected chunk 'hello', got type=%s content=%q", chunk.EventType, chunk.Content)
	}
}

func TestStreamWriter_EmitsThinkingEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewStreamWriter(recv)
	stream.Write(&ThinkingBlockStart{})
	stream.Write(&Delta{Content: "hmm"})
	stream.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected thinking event")
	}
	evt := recv.events[0]
	if evt.EventType != EventTypeThinking || evt.Content != "hmm" {
		t.Errorf("expected thinking 'hmm', got type=%s content=%q", evt.EventType, evt.Content)
	}
}

func TestStreamWriter_ToolUseDeltaNotEmitted(t *testing.T) {
	recv := &testReceiver{}
	stream := NewStreamWriter(recv)
	stream.Write(&ToolUseBlockStart{Id: "tu_1", Name: "tool"})
	stream.Write(&Delta{Content: `{"a":1}`})
	stream.Close()

	if len(recv.events) != 0 {
		t.Errorf("tool_use delta should not emit client events, got %d", len(recv.events))
	}
}
