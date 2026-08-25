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
	orig := Blocks{&ImageBlock{BaseBlock: BaseBlock{Type: ImageBlockType}, Source: &ImageSource{SourceType: "base64", MediaType: "image/png", Data: "abcd"}}}
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
		NewMessageStartBlock(&Usage{InputTokens: 10, OutputTokens: 20}),
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

func TestBlocks_RoundTrip(t *testing.T) {
	orig := Blocks{
		NewMessageStartBlock(&Usage{InputTokens: 104, OutputTokens: 0}),
		&ThinkingBlock{BaseBlock: BaseBlock{Type: ThinkingBlockType}, Thinking: "思考中"},
		NewFullTextBlock("你好"),
		func() *ToolUseBlock {
			tu := NewToolUseBlock("call_00", "execute_command")
			tu.Input = value.NewObjectFromMap(map[string]any{"command": "ver"})
			return tu
		}(),
		NewToolResultBlock("call_00", Blocks{NewErrorFullTextBlock("Microsoft Windows")}),
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	var got Blocks
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(got) != len(orig) {
		t.Fatalf("length mismatch: got %d want %d", len(got), len(orig))
	}

	if u, ok := got[0].(*MessageStartBlock); !ok {
		t.Errorf("block[0] = %T, want *MessageStartBlock", got[0])
	} else if u.Usage == nil || u.Usage.InputTokens != 104 {
		t.Errorf("usage mismatch: %+v", u.Usage)
	}

	if th, ok := got[1].(*ThinkingBlock); !ok {
		t.Errorf("block[1] = %T, want *ThinkingBlock", got[1])
	} else if th.Thinking != "思考中" {
		t.Errorf("thinking mismatch: %q", th.Thinking)
	}

	if tb, ok := got[2].(*TextBlock); !ok {
		t.Errorf("block[2] = %T, want *TextBlock", got[2])
	} else if tb.Text != "你好" {
		t.Errorf("text mismatch: %q", tb.Text)
	}

	if tu, ok := got[3].(*ToolUseBlock); !ok {
		t.Errorf("block[3] = %T, want *ToolUseBlock", got[3])
	} else if tu.Input == nil || tu.Input.GetString("command") != "ver" {
		t.Errorf("tool_use input mismatch: %v", tu.Input)
	}

	if tr, ok := got[4].(*ToolResultBlock); !ok {
		t.Errorf("block[4] = %T, want *ToolResultBlock", got[4])
	} else if len(tr.Content) != 1 {
		t.Errorf("tool_result content length = %d, want 1", len(tr.Content))
	} else if tb, ok := tr.Content[0].(*TextBlock); !ok {
		t.Errorf("tool_result content[0] = %T, want *TextBlock", tr.Content[0])
	} else if tb.Text != "Microsoft Windows" {
		t.Errorf("tool_result content text mismatch: %q", tb.Text)
	}
}

// TestBlocks_CustomTextBlockRoundTrip 验证 CustomTextBlock 可随 Blocks 无损往返：
// 业务自定义内容放进 Text，用 TextType 表达语义，不新增块类型。
func TestBlocks_CustomTextBlockRoundTrip(t *testing.T) {
	orig := Blocks{
		NewCustomTextBlock("card payload", TextType("resource_card")),
		NewFullTextBlock("普通文本"),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	var got Blocks
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("length = %d, want 2", len(got))
	}
	card, ok := got[0].(*CustomTextBlock)
	if !ok {
		t.Fatalf("block[0] = %T, want *CustomTextBlock", got[0])
	}
	if card.Text != "card payload" || card.TextType != TextType("resource_card") {
		t.Errorf("card round-trip mismatch: %+v", card)
	}
	if card.GetType() != CustomTextBlockType {
		t.Errorf("GetType = %q, want %q", card.GetType(), CustomTextBlockType)
	}
	if card.ForContext() {
		t.Error("CustomTextBlock.ForContext() should stay false after round-trip")
	}
}

func TestBlocks_UnknownBlockTypeErrors(t *testing.T) {
	data := []byte(`[{"type":"does_not_exist","text":"x"}]`)
	var got Blocks
	if err := json.Unmarshal(data, &got); err == nil {
		t.Error("expected error for unknown block type")
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
		{NewMessageStartBlock(&Usage{InputTokens: 1}), false},
		{NewDoneBlock(), false},
	}
	for _, c := range cases {
		if got := c.block.ForContext(); got != c.want {
			t.Errorf("%T.ForContext() = %v, want %v", c.block, got, c.want)
		}
	}
}
