package agent

import (
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

// ── BlockStream: WriteBlock 文本拼接（工具输出场景）──

func TestBlockStream_WriteBlock_TextCoalescing(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.WriteBlock(chat.NewTextBlock("hello "))
	stream.WriteBlock(chat.NewTextBlock("world"))

	var text string
	count := 0
	blocks, _ := stream.ReadBlocks()
	for _, b := range blocks {
		count++
		if tb, ok := b.(*chat.TextBlock); ok {
			text += tb.Text
		}
	}
	if count != 1 {
		t.Errorf("expected 1 coalesced block, got %d", count)
	}
	if text != "hello world" {
		t.Errorf("expected 'hello world', got %q", text)
	}
}

// ── BlockStream: WriteEvent 流式输出（长耗时命令实时回显场景）──

type testReceiver struct {
	events []*chat.ClientEvent
}

func (r *testReceiver) AddEvent(evt *chat.ClientEvent) {
	r.events = append(r.events, evt)
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
		if recv.events[i].EventType != chat.EventTypeChunk || recv.events[i].Content != want {
			t.Errorf("event[%d] expected chunk %q, got type=%s content=%q",
				i, want, recv.events[i].EventType, recv.events[i].Content)
		}
	}

	// 同时进入 tool_result（连续文本拼接为一块）
	blocks, _ := stream.ReadBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 coalesced block, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*chat.TextBlock); !ok || tb.Text != "line1\nline2\n" {
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
	blocks, _ := stream.ReadBlocks()
	if len(blocks) != 0 {
		t.Errorf("empty content should not collect block, got %d", len(blocks))
	}
}

func TestBlockStream_WriteEvent_NilReceiver(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.WriteEvent("no receiver") // 不应 panic

	blocks, _ := stream.ReadBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block even without receiver, got %d", len(blocks))
	}
}
