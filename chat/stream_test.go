package chat

import (
	"errors"
	"testing"
)

// drainBlocks 取回 BlockStream 中的全部内容 block。
func drainBlocks(t *testing.T, stream *BlockStream) []Block {
	t.Helper()
	return stream.ReadBlocks()
}

// ── BlockStart + Delta：Block 组装 ──

func TestBlockStream_TextStream(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.BlockTextStart()
	stream.Delta("Hello ")
	stream.Delta("World")

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*TextBlock)
	if !ok || tb.Text != "Hello World" {
		t.Errorf("expected 'Hello World', got %v", blocks[0])
	}
}

func TestBlockStream_ThinkingStream(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.BlockThinkingStart()
	stream.Delta("let me think...")

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(blocks))
	}
	tb, ok := blocks[0].(*ThinkingBlock)
	if !ok || tb.Thinking != "let me think..." {
		t.Errorf("expected 'let me think...', got %v", blocks[0])
	}
}

func TestBlockStream_EmptyThinkingSkipped(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.BlockThinkingStart()
	// 无增量：空 thinking block 应被跳过
	stream.BlockTextStart()
	stream.Delta("text")

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (empty thinking skipped), got %d", len(blocks))
	}
	if _, ok := blocks[0].(*TextBlock); !ok {
		t.Errorf("expected text block, got %T", blocks[0])
	}
}

func TestBlockStream_ToolUseStream(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.BlockToolUseStart("tu_1", "my_tool")
	stream.Delta(`{"cmd":"ls"}`)

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

func TestBlockStream_MultipleBlocks(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.BlockThinkingStart()
	stream.Delta("hmm")
	stream.BlockTextStart()
	stream.Delta("answer")
	stream.BlockToolUseStart("tu_1", "tool")
	stream.Delta(`{}`)

	blocks := drainBlocks(t, stream)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if _, ok := blocks[0].(*ThinkingBlock); !ok {
		t.Errorf("expected ThinkingBlock, got %T", blocks[0])
	}
	if _, ok := blocks[1].(*TextBlock); !ok {
		t.Errorf("expected TextBlock, got %T", blocks[1])
	}
	if _, ok := blocks[2].(*ToolUseBlock); !ok {
		t.Errorf("expected ToolUseBlock, got %T", blocks[2])
	}
}

// ── StopReason / Usage ──

func TestBlockStream_StopReasonAndUsage(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.StopReason(StopReasonToolUse)
	stream.MessageDelta(&Usage{InputTokens: 10, OutputTokens: 20})

	if stream.GetStopReason() != StopReasonToolUse {
		t.Errorf("expected tool_use, got %s", stream.GetStopReason())
	}

	// Usage block is added to blocks list
	stream.BlockTextStart()
	stream.Delta("hi")
	blocks := stream.ReadBlocks()
	if stream.GetStopReason() != StopReasonToolUse {
		t.Errorf("expected tool_use, got %s", stream.GetStopReason())
	}
	// blocks contain text + usage
	if len(blocks) != 2 {
		t.Errorf("expected 2 blocks (text + usage), got %d", len(blocks))
	}
}

// TestBlockStream_MetadataDefaults 验证未上报元数据时的默认值：
// stop_reason 默认 end_turn。
func TestBlockStream_MetadataDefaults(t *testing.T) {
	stream := NewBlockStream(nil)
	if stream.GetStopReason() != StopReasonEndTurn {
		t.Errorf("expected default end_turn, got %s", stream.GetStopReason())
	}
}

// ── Block：文本拼接 / ErrorText ──

func TestBlockStream_Block_NoCoalescing(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Block(NewFullTextBlock("hello "))
	stream.Block(NewFullTextBlock("world"))

	blocks := stream.ReadBlocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 separate blocks, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "hello " {
		t.Errorf("expected 'hello ', got %v", blocks[0])
	}
	if tb, ok := blocks[1].(*TextBlock); !ok || tb.Text != "world" {
		t.Errorf("expected 'world', got %v", blocks[1])
	}
}

func TestBlockStream_FullText(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.FullText("line1\n")
	stream.FullText("line2\n")

	blocks := stream.ReadBlocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

// TestBlockStream_ErrorText 验证错误以文本写入：
// 错误文本与正常文本不合并（先 flush 已有正常文本）。
func TestBlockStream_ErrorText(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.Block(NewFullTextBlock("partial: "))
	stream.ErrorText(errors.New("boom"))

	blocks := stream.ReadBlocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (normal + error), got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "partial: " {
		t.Errorf("expected normal 'partial: ', got %v", blocks[0])
	}
	if tb, ok := blocks[1].(*TextBlock); !ok || tb.Text != "boom" {
		t.Errorf("expected error 'boom', got %v", blocks[1])
	}
}

// TestBlockStream_ErrorText_Multiple 每次 ErrorText 产生独立的错误块。
func TestBlockStream_ErrorText_Multiple(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.ErrorText(errors.New("err1"))
	stream.ErrorText(errors.New("err2"))

	blocks := stream.ReadBlocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 error blocks, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "err1" {
		t.Errorf("expected 'err1', got %v", blocks[0])
	}
	if tb, ok := blocks[1].(*TextBlock); !ok || tb.Text != "err2" {
		t.Errorf("expected 'err2', got %v", blocks[1])
	}
}

// TestBlockStream_ErrorText_NormalAfterError 正常文本在错误之后写入时独立成块。
func TestBlockStream_ErrorText_NormalAfterError(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.ErrorText(errors.New("boom"))
	stream.Block(NewFullTextBlock("recovered"))

	blocks := stream.ReadBlocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if tb, ok := blocks[0].(*TextBlock); !ok || tb.Text != "boom" {
		t.Errorf("expected error block 'boom', got %v", blocks[0])
	}
	if tb, ok := blocks[1].(*TextBlock); !ok || tb.Text != "recovered" {
		t.Errorf("expected normal block 'recovered', got %v", blocks[1])
	}
}

// ── ReadBlocks：空流 ──

func TestBlockStream_ReadBlocks_Empty(t *testing.T) {
	stream := NewBlockStream(nil)
	blocks := stream.ReadBlocks()
	if len(blocks) != 0 {
		t.Errorf("expected empty blocks, got %v", blocks)
	}
}

// ── Flush：未完成 block ──

func TestBlockStream_FlushesActiveBlock(t *testing.T) {
	stream := NewBlockStream(nil)
	stream.BlockTextStart()
	stream.Delta("pending text")
	// 无下一个 BlockStart，ReadBlocks 应 flush 当前组装中的 block

	blocks := drainBlocks(t, stream)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block from flush, got %d", len(blocks))
	}
}

// ── BlockReceiver：SendBlock 被调用 ──

type testReceiver struct {
	blocks []Block
}

func (r *testReceiver) SendBlock(block Block) uint64 {
	r.blocks = append(r.blocks, block)
	return 0
}

func TestBlockStream_EmitsStartAndDeltaBlocks(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.BlockTextStart()
	stream.Delta("hello")

	// receiver 收到 StartBlock + DeltaBlock
	if len(recv.blocks) < 2 {
		t.Fatalf("expected at least 2 blocks emitted, got %d", len(recv.blocks))
	}
	if _, ok := recv.blocks[0].(*StartBlock); !ok {
		t.Errorf("expected StartBlock, got %T", recv.blocks[0])
	}
	if db, ok := recv.blocks[1].(*DeltaBlock); !ok || db.Content != "hello" {
		t.Errorf("expected DeltaBlock{hello}, got %v", recv.blocks[1])
	}
}

func TestBlockStream_ThinkingEmitsStartAndDelta(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.BlockThinkingStart()
	stream.Delta("hmm")

	if len(recv.blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(recv.blocks))
	}
	if _, ok := recv.blocks[0].(*StartBlock); !ok {
		t.Errorf("expected StartBlock, got %T", recv.blocks[0])
	}
	if db, ok := recv.blocks[1].(*DeltaBlock); !ok || db.Content != "hmm" {
		t.Errorf("expected DeltaBlock{hmm}, got %v", recv.blocks[1])
	}
}

func TestBlockStream_ToolUseEmitsStartAndDelta(t *testing.T) {
	recv := &testReceiver{}
	stream := NewBlockStream(recv)
	stream.BlockToolUseStart("tu_1", "tool")
	stream.Delta(`{"a":1}`)

	if len(recv.blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(recv.blocks))
	}
	if sb, ok := recv.blocks[0].(*StartBlock); !ok {
		t.Errorf("expected StartBlock, got %T", recv.blocks[0])
	} else if tu, ok := sb.Block.(*ToolUseBlock); !ok || tu.ID != "tu_1" {
		t.Errorf("StartBlock should wrap ToolUseBlock, got %v", sb.Block)
	}
}
