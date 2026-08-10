package agent

import (
	"errors"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

func TestWithoutThinking_StripsThinkingBlock(t *testing.T) {
	blocks := chat.Blocks{
		chat.NewTextBlock("hello"),
		chat.NewThinkingBlock("hmm..."),
		chat.NewTextBlock("world"),
	}
	result := withoutThinking(blocks)
	if len(result) != 2 {
		t.Errorf("expected 2 blocks after stripping thinking, got %d: %v", len(result), result)
	}
	for _, b := range result {
		if b.Type() == chat.ContentTypeThinking {
			t.Error("expected no thinking blocks in result")
		}
	}
}

func TestWithoutThinking_AllThinking(t *testing.T) {
	blocks := chat.Blocks{
		chat.NewThinkingBlock("think1"),
		chat.NewThinkingBlock("think2"),
	}
	result := withoutThinking(blocks)
	if len(result) != 0 {
		t.Errorf("expected 0 blocks when all are thinking, got %d", len(result))
	}
}

func TestWithoutThinking_NoThinking(t *testing.T) {
	blocks := chat.Blocks{
		chat.NewTextBlock("a"),
		chat.NewTextBlock("b"),
	}
	result := withoutThinking(blocks)
	if len(result) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(result))
	}
}

func TestWithoutThinking_Empty(t *testing.T) {
	result := withoutThinking(chat.Blocks{})
	if len(result) != 0 {
		t.Errorf("expected 0 for empty input, got %d", len(result))
	}
}

func TestWithoutThinking_Nil(t *testing.T) {
	result := withoutThinking(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 for nil input, got %d", len(result))
	}
}

func TestWithoutThinking_PreservesOrder(t *testing.T) {
	blocks := chat.Blocks{
		chat.NewThinkingBlock("think1"),
		chat.NewTextBlock("first"),
		chat.NewThinkingBlock("think2"),
		chat.NewTextBlock("second"),
	}
	result := withoutThinking(blocks)
	if len(result) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(result))
	}
	if tb, ok := result[0].(*chat.TextBlock); !ok || tb.Text != "first" {
		t.Errorf("expected first text block to be 'first', got %v", result[0])
	}
	if tb, ok := result[1].(*chat.TextBlock); !ok || tb.Text != "second" {
		t.Errorf("expected second text block to be 'second', got %v", result[1])
	}
}

// ── BlockStream: WriteBlock 文本拼接 ──

func TestBlockStream_WriteBlock_TextCoalescing(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(chat.NewTextBlock("hello "))
	stream.WriteBlock(chat.NewTextBlock("world"))
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*chat.TextBlock)
	if !ok || tb.Text != "hello world" {
		t.Errorf("expected 'hello world', got %v", blocks[0])
	}
}

func TestBlockStream_WriteBlock_NonTextBreaksCoalescing(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(chat.NewTextBlock("hello "))
	stream.WriteBlock(chat.NewThinkingBlock("think"))
	stream.WriteBlock(chat.NewTextBlock("world"))
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	// block[0] = coalesced "hello "
	// block[1] = thinking
	// block[2] = "world"
	if tb, ok := blocks[0].(*chat.TextBlock); !ok || tb.Text != "hello " {
		t.Errorf("block[0] expected 'hello ', got %v", blocks[0])
	}
	if blocks[1].Type() != chat.ContentTypeThinking {
		t.Errorf("block[1] expected thinking, got %s", blocks[1])
	}
	if tb, ok := blocks[2].(*chat.TextBlock); !ok || tb.Text != "world" {
		t.Errorf("block[2] expected 'world', got %v", blocks[2])
	}
}

func TestBlockStream_Close_Idempotent(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(chat.NewTextBlock("x"))
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
	stream.WriteBlock(chat.NewTextBlock("pending text"))
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
	stream.Write(&chat.MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewTextBlock("")})
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: "Hello "}})
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: "World"}})
	stream.Write(&chat.ContentBlockStopEvent{Index: 0})
	stream.Write(&chat.MessageStopEvent{})
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*chat.TextBlock)
	if !ok || tb.Text != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", tb.Text)
	}
}

func TestBlockStream_Write_ThinkingStream(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&chat.MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewThinkingBlock("")})
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeThinking, Thinking: "let me think..."}})
	stream.Write(&chat.ContentBlockStopEvent{Index: 0})
	stream.Write(&chat.MessageStopEvent{})
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*chat.ThinkingBlock)
	if !ok || tb.Thinking != "let me think..." {
		t.Errorf("expected 'let me think...', got %v", blocks[0])
	}
}

func TestBlockStream_Write_ToolUseStream(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&chat.MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewToolUseBlock("tu_1", "my_tool", nil)})
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeInputJSON, PartialJSON: `{"cmd":"ls"}`}})
	stream.Write(&chat.ContentBlockStopEvent{Index: 0})
	stream.Write(&chat.MessageStopEvent{})
	stream.Close()

	blocks := drainBlocks(stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool_use block, got %d", len(blocks))
	}
	tub, ok := blocks[0].(*chat.ToolUseBlock)
	if !ok {
		t.Fatalf("expected *ToolUseBlock, got %T", blocks[0])
	}
	if tub.ID != "tu_1" || tub.Name != "my_tool" {
		t.Errorf("id/name mismatch: %s/%s", tub.ID, tub.Name)
	}
}

func TestBlockStream_Write_StopReason(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.Write(&chat.MessageStartEvent{ID: "m1", Model: "test", Role: "assistant"})
	stream.Write(&chat.ContentBlockStartEvent{Index: 0, ContentBlock: chat.NewTextBlock("")})
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: "ok"}})
	stream.Write(&chat.ContentBlockStopEvent{Index: 0})
	stream.Write(&chat.MessageDeltaEvent{StopReason: chat.StopReasonEndTurn})
	stream.Write(&chat.MessageStopEvent{})
	stream.Close()

	if stream.StopReason() != chat.StopReasonEndTurn {
		t.Errorf("expected end_turn, got %s", stream.StopReason())
	}
}

// ── BlockStream: WriteError ──

func TestBlockStream_WriteError(t *testing.T) {
	stream := NewBlockStream("s1", nil)
	stream.WriteBlock(chat.NewTextBlock("before error"))
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
	events []*chat.ClientEvent
}

func (r *testReceiver) AddEvent(evt *chat.ClientEvent) {
	r.events = append(r.events, evt)
}

func TestBlockStream_EmitsChunkEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream("s1", recv)
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeText, Text: "hello"}})
	stream.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected at least 1 event emitted")
	}
	chunk := recv.events[0]
	if chunk.EventType != chat.EventTypeChunk || chunk.Content != "hello" {
		t.Errorf("expected chunk 'hello', got type=%s content=%q", chunk.EventType, chunk.Content)
	}
	if chunk.SessionId != "s1" {
		t.Errorf("expected sessionId 's1', got %q", chunk.SessionId)
	}
}

func TestBlockStream_EmitsThinkingEvents(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream("s1", recv)
	stream.Write(&chat.ContentBlockDeltaEvent{Index: 0, Delta: chat.ContentDelta{Type: chat.DeltaTypeThinking, Thinking: "hmm"}})
	stream.Close()

	if len(recv.events) == 0 {
		t.Fatal("expected thinking event")
	}
	evt := recv.events[0]
	if evt.EventType != chat.EventTypeThinking || evt.Content != "hmm" {
		t.Errorf("expected thinking 'hmm', got type=%s content=%q", evt.EventType, evt.Content)
	}
}

// drainBlocks 消费流中的所有 block。
func drainBlocks(stream *BlockStream) []chat.Block {
	var blocks []chat.Block
	for b := stream.ReadBlock(); b != nil; b = stream.ReadBlock() {
		blocks = append(blocks, b)
	}
	return blocks
}
