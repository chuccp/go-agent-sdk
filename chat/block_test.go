package chat

import (
	"encoding/json"
	"testing"

	"github.com/chuccp/go-agent-sdk/value"
)

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
	bs := Blocks{NewFullTextBlock("hi")}
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	// 验证包含 type 和 text 字段（UseDeltaBlock 嵌入可能产生额外字段，不影响功能）
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw[0]["type"] != "text" || raw[0]["text"] != "hi" {
		t.Errorf("fields mismatch: %v", raw[0])
	}
}

func TestMessage_MarshalOmitsInternalFields(t *testing.T) {
	msg := NewTextMessage("hi")
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["role"] != "user" {
		t.Errorf("expected role=user, got %v", raw["role"])
	}
	content := raw["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hi" {
		t.Errorf("content mismatch: %v", block)
	}
}

func TestMessage_MarshalIncludesToolUseType(t *testing.T) {
	msg := Message{
		Role:    RoleAssistant,
		Content: Blocks{NewToolUseBlock("tu_1", "my_tool")},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	content := raw["content"].([]any)
	tu := content[0].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "tu_1" || tu["name"] != "my_tool" {
		t.Errorf("tool_use fields mismatch: %v", tu)
	}
}

func TestBlocks_MarshalToolUseWithInput(t *testing.T) {
	tu := NewToolUseBlock("tu_1", "my_tool")
	tu.Input = value.NewObjectFromMap(map[string]any{"a": float64(1)})
	bs := Blocks{tu}
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw[0]["type"] != "tool_use" || raw[0]["id"] != "tu_1" {
		t.Errorf("tool_use fields mismatch: %v", raw[0])
	}
	input := raw[0]["input"].(map[string]any)
	if input["a"] != float64(1) {
		t.Errorf("input mismatch: %v", input)
	}
}

func TestBlocks_MarshalToolResult(t *testing.T) {
	orig := Blocks{NewToolResultBlock("tu_1", Blocks{NewFullTextBlock("result")})}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	tr := raw[0]
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "tu_1" {
		t.Errorf("tool_result fields mismatch: %v", tr)
	}
}

func TestBlocks_MarshalImage(t *testing.T) {
	orig := Blocks{&ImageBlock{Type: ImageBlockType, Source: &ImageSource{SourceType: "base64", MediaType: "image/png", Data: "abcd"}}}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	img := raw[0]
	if img["type"] != "image" {
		t.Errorf("expected image type, got %v", img["type"])
	}
}

func TestBlocks_MarshalMetadata(t *testing.T) {
	orig := Blocks{
		NewUsageBlock(&Usage{InputTokens: 10, OutputTokens: 20}),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	// UsageBlock has private usage field, just verify it marshals without error
	if len(data) == 0 {
		t.Error("expected non-empty marshal output")
	}
}

func TestBlocks_MarshalErrorText(t *testing.T) {
	orig := Blocks{NewErrorFullTextBlock("something broke")}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	tb := raw[0]
	if tb["text"] != "something broke" || tb["type"] != "text" {
		t.Errorf("error text mismatch: %v", tb)
	}
}

func TestBlocks_ForContext(t *testing.T) {
	cases := []struct {
		block Block
		want  bool
	}{
		{NewFullTextBlock("t"), true},
		{NewErrorFullTextBlock("e"), true},
		{&ImageBlock{Source: &ImageSource{SourceType: "base64"}}, true},
		{NewToolUseBlock("tu_1", "tool"), true},
		{NewToolResultBlock("tu_1", Blocks{NewFullTextBlock("result")}), true},
		{NewThinkingBlock(), false},
		{NewUsageBlock(&Usage{InputTokens: 1}), false},
		{NewDoneBlock(), false},
	}
	for _, c := range cases {
		if got := c.block.ForContext(); got != c.want {
			t.Errorf("%T.ForContext() = %v, want %v", c.block, got, c.want)
		}
	}
}
