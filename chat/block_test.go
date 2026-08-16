package chat

import (
	"encoding/json"
	"testing"

	"github.com/chuccp/go-agent-sdk/value"
)

// 注：UnmarshalBlock / Blocks.UnmarshalJSON 目前被注释掉（与 *value.Object 入参不兼容），
// 对应的往返反序列化测试暂移除；仅保留 Marshal 方向的测试。

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

func TestBlocks_MarshalIncludesType(t *testing.T) {
	bs := Blocks{NewTextBlock("hi")}
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"text","text":"hi"}]`
	if string(data) != want {
		t.Errorf("got %s, want %s", string(data), want)
	}
}

func TestMessage_MarshalOmitsInternalFields(t *testing.T) {
	msg := NewTextMessage("hi")
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"hi"}]}`
	if string(data) != want {
		t.Errorf("got %s, want %s", string(data), want)
	}
}

func TestMessage_MarshalIncludesToolUseType(t *testing.T) {
	input := value.NewObjectFromMap(map[string]any{"a": float64(1)})
	msg := Message{
		Role:    RoleAssistant,
		Content: Blocks{NewToolUseBlock("tu_1", "my_tool", input)},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"my_tool","input":{"a":1}}]}`
	if string(data) != want {
		t.Errorf("got %s, want %s", string(data), want)
	}
}

// ==================== 反序列化 / 往返 ====================

func TestBlocks_UnmarshalText(t *testing.T) {
	var bs Blocks
	if err := json.Unmarshal([]byte(`[{"type":"text","text":"hi"}]`), &bs); err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 {
		t.Fatalf("expected 1 block, got %d", len(bs))
	}
	tb, ok := bs[0].(*TextBlock)
	if !ok || tb.Text != "hi" {
		t.Errorf("expected TextBlock{Text:hi}, got %#v", bs[0])
	}
}

func TestBlocks_UnmarshalNull(t *testing.T) {
	var bs Blocks = Blocks{NewTextBlock("x")}
	if err := json.Unmarshal([]byte("null"), &bs); err != nil {
		t.Fatal(err)
	}
	if bs != nil {
		t.Errorf("expected nil Blocks after unmarshal null, got %#v", bs)
	}
}

func TestBlocks_RoundTripToolUse(t *testing.T) {
	input := value.NewObjectFromMap(map[string]any{"a": float64(1), "b": "x"})
	orig := Blocks{NewToolUseBlock("tu_1", "my_tool", input)}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back Blocks
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	tu, ok := back[0].(*ToolUseBlock)
	if !ok {
		t.Fatalf("expected *ToolUseBlock, got %#v", back[0])
	}
	if tu.ID != "tu_1" || tu.Name != "my_tool" {
		t.Errorf("ID/Name mismatch: %q %q", tu.ID, tu.Name)
	}
	if tu.Input.GetInt("a") != 1 || tu.Input.GetString("b") != "x" {
		t.Errorf("input mismatch: %s", tu.Input.String())
	}
}

func TestBlocks_RoundTripToolResult(t *testing.T) {
	orig := Blocks{NewToolResultBlock("tu_1", Blocks{NewTextBlock("result")})}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back Blocks
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	tr, ok := back[0].(*ToolResultBlock)
	if !ok {
		t.Fatalf("expected *ToolResultBlock, got %#v", back[0])
	}
	if tr.ToolUseID != "tu_1" {
		t.Errorf("ToolUseID mismatch: %q", tr.ToolUseID)
	}
	nested, ok := tr.Content.(Blocks)
	if !ok {
		t.Fatalf("expected Content of type Blocks, got %T", tr.Content)
	}
	tb, ok := nested[0].(*TextBlock)
	if !ok || tb.Text != "result" {
		t.Errorf("expected nested TextBlock{result}, got %#v", nested[0])
	}
}

func TestBlocks_RoundTripImage(t *testing.T) {
	orig := Blocks{&ImageBlock{Source: &ImageSource{SourceType: "base64", MediaType: "image/png", Data: "abcd"}}}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back Blocks
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	img, ok := back[0].(*ImageBlock)
	if !ok || img.Source == nil || img.Source.SourceType != "base64" || img.Source.MediaType != "image/png" || img.Source.Data != "abcd" {
		t.Errorf("image round-trip mismatch: %#v", back[0])
	}
}

func TestBlocks_RoundTripMetadata(t *testing.T) {
	orig := Blocks{
		NewUsageBlock(&Usage{InputTokens: 10, OutputTokens: 20}),
		NewStopReasonBlock(StopReasonToolUse),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back Blocks
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	ub, ok := back[0].(*UsageBlock)
	if !ok || ub.Usage == nil || ub.Usage.InputTokens != 10 || ub.Usage.OutputTokens != 20 {
		t.Errorf("usage round-trip mismatch: %#v", back[0])
	}
	sb, ok := back[1].(*StopReasonBlock)
	if !ok || sb.Reason != StopReasonToolUse {
		t.Errorf("stop_reason round-trip mismatch: %#v", back[1])
	}
}
