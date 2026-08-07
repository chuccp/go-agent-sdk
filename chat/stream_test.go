package chat

import (
	"errors"
	"testing"
)

// ── BlockStream: WriteBlock 文本拼接 ──

func TestBlockStream_WriteBlock_TextCoalescing(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(NewTextBlock("hello "))
	stream.WriteBlock(NewTextBlock("world"))
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "hello world" {
		t.Errorf("expected 'hello world', got %v", blocks[0])
	}
}

func TestBlockStream_WriteBlock_NonTextBreaksCoalescing(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(NewTextBlock("hello "))
	stream.WriteBlock(NewThinkingBlock("think"))
	stream.WriteBlock(NewTextBlock("world"))
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	// block[0] = coalesced "hello "
	// block[1] = thinking
	// block[2] = "world"
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "hello " {
		t.Errorf("block[0] expected 'hello ', got %v", blocks[0])
	}
	if blocks[1].Type() != ContentTypeThinking {
		t.Errorf("block[1] expected thinking, got %s", blocks[1])
	}
	if tb, ok := blocks[2].(*TextBlock); !ok || tb.Text != "world" {
		t.Errorf("block[2] expected 'world', got %v", blocks[2])
	}
}

func TestBlockStream_Close_Idempotent(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(NewTextBlock("x"))
	stream.Close()
	stream.Close() // 幂等
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(blocks))
	}
}

func TestBlockStream_Close_FlushesPending(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(NewTextBlock("pending text"))
	// 未显式写非 text block 前，文本在 pending 缓冲里
	stream.Close() // Close 应 flush

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block from pending flush, got %d", len(blocks))
	}
}

// ── BlockStream: Write (protocol events) ──

func TestBlockStream_Write_TextStream(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewTextBlock("")})
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "Hello "}})
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "World"}})
	stream.Write(&ContentBlockStopEvent{Index: 0})
	stream.Write(&MessageStopEvent{})
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", tb.Text)
	}
}

func TestBlockStream_Write_ThinkingStream(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewThinkingBlock("")})
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeThinking, Thinking: "let me think..."}})
	stream.Write(&ContentBlockStopEvent{Index: 0})
	stream.Write(&MessageStopEvent{})
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*ThinkingBlock)
	if !ok || tb.Thinking != "let me think..." {
		t.Errorf("expected 'let me think...', got %v", blocks[0])
	}
}

func TestBlockStream_Write_ToolUseStream(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewToolUseBlock("tu_1", "my_tool", nil)})
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeInputJSON, PartialJSON: `{"cmd":"ls"}`}})
	stream.Write(&ContentBlockStopEvent{Index: 0})
	stream.Write(&MessageStopEvent{})
	stream.Close()

	blocks := drainBlocks(stream)
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
}

func TestBlockStream_Write_StopReason(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewTextBlock("")})
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "ok"}})
	stream.Write(&ContentBlockStopEvent{Index: 0})
	stream.Write(&MessageDeltaEvent{StopReason: StopReasonEndTurn})
	stream.Write(&MessageStopEvent{})
	stream.Close()

	if stream.StopReason() != StopReasonEndTurn {
		t.Errorf("expected end_turn, got %s", stream.StopReason())
	}
}

// ── BlockStream: WriteError ──

func TestBlockStream_WriteError(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(NewTextBlock("before error"))
	stream.WriteError(errors.New("test error"))
	// WriteError 调用 Close，之后不再写入

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (before error), got %d", len(blocks))
	}
	if stream.Err() == nil {
		t.Error("expected Err() to return error")
	}
}

// ── BlockStream: ReadBlock on empty ──

func TestBlockStream_ReadBlock_Empty(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Close()
	if b := stream.ReadBlock(); b != nil {
		t.Errorf("expected nil on empty closed stream, got %v", b)
	}
}

// ── BlockStream: emitter ──

type testReceiver struct {
	events []*ClientEvent
}

func (r *testReceiver) AddEvent(evt *ClientEvent) {
	r.events = append(r.events, evt)
}

func TestBlockStream_EmitsChunkEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream("s1", recv)
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "hello"}})
	stream.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected at least 1 event emitted")
	}
	chunk := recv.events[0]
	if chunk.EventType != EventTypeChunk || chunk.Content != "hello" {
		t.Errorf("expected chunk 'hello', got type=%s content=%q", chunk.EventType, chunk.Content)
	}
	if chunk.SessionId != "s1" {
		t.Errorf("expected sessionId 's1', got %q", chunk.SessionId)
	}
}

func TestBlockStream_EmitsThinkingEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream("s1", recv)
	stream.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeThinking, Thinking: "hmm"}})
	stream.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected thinking event")
	}
	evt := recv.events[0]
	if evt.EventType != EventTypeThinking || evt.Content != "hmm" {
		t.Errorf("expected thinking 'hmm', got type=%s content=%q", evt.EventType, evt.Content)
	}
}

// drainBlocks 消费流中的所有 block。
func drainBlocks(stream *BlockStream) []Block {
	var blocks []Block
	for b := stream.ReadBlock(); b != nil; b = stream.ReadBlock() {
		blocks = append(blocks, b)
	}
	return blocks
}
