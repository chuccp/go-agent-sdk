package chat

import (
	"encoding/json"
	"testing"
)

// ── Block JSON 序列化往返测试 ──

func TestBlockRoundtrip_TextBlock(t *testing.T) {
	orig := NewTextBlock("hello world")
	data, err := MarshalBlock(orig)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	tb, ok := back.(*TextBlock)
	if !ok {
		t.Fatalf("expected *TextBlock, got %T", back)
	}
	if tb.Text != "hello world" {
		t.Errorf("expected 'hello world', got %q", tb.Text)
	}
}

func TestBlockRoundtrip_ThinkingBlock(t *testing.T) {
	orig := NewThinkingBlock("hmm...")
	data, err := MarshalBlock(orig)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	tb, ok := back.(*ThinkingBlock)
	if !ok {
		t.Fatalf("expected *ThinkingBlock, got %T", back)
	}
	if tb.Thinking != "hmm..." {
		t.Errorf("expected 'hmm...', got %q", tb.Thinking)
	}
}

func TestBlockRoundtrip_ToolUseBlock(t *testing.T) {
	orig := NewToolUseBlock("tu_1", "my_tool", map[string]any{"key": "val"})
	data, err := MarshalBlock(orig)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	tub, ok := back.(*ToolUseBlock)
	if !ok {
		t.Fatalf("expected *ToolUseBlock, got %T", back)
	}
	if tub.ID != "tu_1" || tub.Name != "my_tool" {
		t.Errorf("id/name mismatch: %s/%s", tub.ID, tub.Name)
	}
}

func TestBlockRoundtrip_ToolResultBlock(t *testing.T) {
	orig := NewToolResultBlock("tu_1", "result string")
	data, err := MarshalBlock(orig)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	trb, ok := back.(*ToolResultBlock)
	if !ok {
		t.Fatalf("expected *ToolResultBlock, got %T", back)
	}
	if trb.ToolUseID != "tu_1" {
		t.Errorf("expected tu_1, got %s", trb.ToolUseID)
	}
}

func TestBlocks_JSONRoundtrip(t *testing.T) {
	orig := Blocks{
		NewTextBlock("hello"),
		NewThinkingBlock("..."),
		NewToolUseBlock("tu_1", "cmd", map[string]any{"command": "ls"}),
		NewToolResultBlock("tu_1", "ok"),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	var restored Blocks
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	if len(restored) != 4 {
		t.Fatalf("expected 4 blocks, got %d", len(restored))
	}

	// 逐个验证类型
	typeChecks := []ContentType{
		ContentTypeText, ContentTypeThinking, ContentTypeToolUse, ContentTypeToolResult,
	}
	for i, c := range typeChecks {
		if restored[i].Type() != c {
			t.Errorf("block[%d]: expected %s, got %s", i, c, restored[i].Type())
		}
	}
}

func TestBlocks_NilMarshalAsNull(t *testing.T) {
	var bs Blocks
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "null" {
		t.Errorf("expected null for nil Blocks, got %s", string(data))
	}
}

func TestBlocks_EmptyMarshalAsArray(t *testing.T) {
	bs := make(Blocks, 0)
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[]" {
		t.Errorf("expected [] for empty Blocks, got %s", string(data))
	}
}

func TestUnmarshalBlock_UnknownType(t *testing.T) {
	_, err := UnmarshalBlock([]byte(`{"type":"unknown"}`))
	if err == nil {
		t.Error("expected error for unknown type")
	}
}
