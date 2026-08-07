package chat

import (
	"errors"
	"testing"
)

// ── Response: WriteBlock 文本拼接 ──

func TestResponse_WriteBlock_TextCoalescing(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.WriteBlock(NewTextBlock("hello "))
	resp.WriteBlock(NewTextBlock("world"))
	resp.Close()

	blocks := drainBlocks(resp)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "hello world" {
		t.Errorf("expected 'hello world', got %v", blocks[0])
	}
}

func TestResponse_WriteBlock_NonTextBreaksCoalescing(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.WriteBlock(NewTextBlock("hello "))
	resp.WriteBlock(NewThinkingBlock("think"))
	resp.WriteBlock(NewTextBlock("world"))
	resp.Close()

	blocks := drainBlocks(resp)
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
		t.Errorf("block[1] expected thinking, got %s", blocks[1].Type())
	}
	if tb, ok := blocks[2].(*TextBlock); !ok || tb.Text != "world" {
		t.Errorf("block[2] expected 'world', got %v", blocks[2])
	}
}

func TestResponse_Close_Idempotent(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.WriteBlock(NewTextBlock("x"))
	resp.Close()
	resp.Close() // 幂等
	resp.Close()

	blocks := drainBlocks(resp)
	if len(blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(blocks))
	}
}

func TestResponse_Close_FlushesPending(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.WriteBlock(NewTextBlock("pending text"))
	// 未显式写非 text block 前，文本在 pending 缓冲里
	resp.Close() // Close 应 flush

	blocks := drainBlocks(resp)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block from pending flush, got %d", len(blocks))
	}
}

// ── Response: Write (protocol events) ──

func TestResponse_Write_TextStream(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	resp.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewTextBlock("")})
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "Hello "}})
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "World"}})
	resp.Write(&ContentBlockStopEvent{Index: 0})
	resp.Write(&MessageStopEvent{})
	resp.Close()

	blocks := drainBlocks(resp)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", tb.Text)
	}
}

func TestResponse_Write_ThinkingStream(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	resp.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewThinkingBlock("")})
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeThinking, Thinking: "let me think..."}})
	resp.Write(&ContentBlockStopEvent{Index: 0})
	resp.Write(&MessageStopEvent{})
	resp.Close()

	blocks := drainBlocks(resp)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*ThinkingBlock)
	if !ok || tb.Thinking != "let me think..." {
		t.Errorf("expected 'let me think...', got %v", blocks[0])
	}
}

func TestResponse_Write_ToolUseStream(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	resp.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewToolUseBlock("tu_1", "my_tool", nil)})
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeInputJSON, PartialJSON: `{"cmd":"ls"}`}})
	resp.Write(&ContentBlockStopEvent{Index: 0})
	resp.Write(&MessageStopEvent{})
	resp.Close()

	blocks := drainBlocks(resp)
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

func TestResponse_Write_StopReason(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.Write(&MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	resp.Write(&ContentBlockStartEvent{Index: 0, ContentBlock: NewTextBlock("")})
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "ok"}})
	resp.Write(&ContentBlockStopEvent{Index: 0})
	resp.Write(&MessageDeltaEvent{StopReason: StopReasonEndTurn})
	resp.Write(&MessageStopEvent{})
	resp.Close()

	if resp.StopReason() != StopReasonEndTurn {
		t.Errorf("expected end_turn, got %s", resp.StopReason())
	}
}

// ── Response: WriteError ──

func TestResponse_WriteError(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.WriteBlock(NewTextBlock("before error"))
	resp.WriteError(errors.New("test error"))
	// WriteError 调用 Close，之后不再写入

	blocks := drainBlocks(resp)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (before error), got %d", len(blocks))
	}
	if resp.Err() == nil {
		t.Error("expected Err() to return error")
	}
}

// ── Response: ReadBlock on empty ──

func TestResponse_ReadBlock_Empty(t *testing.T) {
	resp := NewResponse("s1", nil)
	resp.Close()
	if b := resp.ReadBlock(); b != nil {
		t.Errorf("expected nil on empty closed response, got %v", b)
	}
}

// ── Response: emitter ──

type testReceiver struct {
	events []*ClientEvent
}

func (r *testReceiver) AddEvent(evt *ClientEvent) {
	r.events = append(r.events, evt)
}

func TestResponse_EmitsChunkEvents(t *testing.T) {
	recv := &testReceiver{}
	resp := NewResponse("s1", recv)
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeText, Text: "hello"}})
	resp.Close()

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

func TestResponse_EmitsThinkingEvents(t *testing.T) {
	recv := &testReceiver{}
	resp := NewResponse("s1", recv)
	resp.Write(&ContentBlockDeltaEvent{Index: 0, Delta: ContentDelta{Type: DeltaTypeThinking, Thinking: "hmm"}})
	resp.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected thinking event")
	}
	evt := recv.events[0]
	if evt.EventType != EventTypeThinking || evt.Content != "hmm" {
		t.Errorf("expected thinking 'hmm', got type=%s content=%q", evt.EventType, evt.Content)
	}
}

// drainBlocks 消费 response 中的所有 block。
func drainBlocks(resp *Response) []Block {
	var blocks []Block
	for b := resp.ReadBlock(); b != nil; b = resp.ReadBlock() {
		blocks = append(blocks, b)
	}
	return blocks
}
