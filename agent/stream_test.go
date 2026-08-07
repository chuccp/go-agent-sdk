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
