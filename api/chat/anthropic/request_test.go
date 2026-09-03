package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

func TestNewRequest_SystemAndToolsCacheControl(t *testing.T) {
	config := chat.DefaultConfig()
	config.Set(chat.ModelConfigKey, "claude-sonnet-4-6")
	config.SetSystemPrompt("你是一个助手")

	messages := &chat.Messages{
		Messages: []chat.Message{chat.NewTextMessage("hi")},
		Tools: []chat.ToolFunction{
			{Name: "cmd", Description: "run", InputSchema: map[string]any{"type": "object"}},
		},
	}

	req := NewRequest(messages, config)
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	// system 应为带 cache_control 的内容块数组
	sys, ok := m["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system 应为单个内容块数组, got %#v", m["system"])
	}
	sysBlock := sys[0].(map[string]any)
	if sysBlock["type"] != "text" || sysBlock["text"] != "你是一个助手" {
		t.Errorf("system 块内容错误: %#v", sysBlock)
	}
	if cc := sysBlock["cache_control"].(map[string]any)["type"]; cc != "ephemeral" {
		t.Errorf("system cache_control 错误: %#v", sysBlock["cache_control"])
	}

	// tools 每个定义都带 cache_control
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools 应为长度 1 的数组, got %#v", m["tools"])
	}
	tool := tools[0].(map[string]any)
	if cc := tool["cache_control"].(map[string]any)["type"]; cc != "ephemeral" {
		t.Errorf("tool cache_control 错误: %#v", tool["cache_control"])
	}
}

func TestNewRequest_EmptySystemAndNoTools(t *testing.T) {
	config := chat.DefaultConfig()
	req := NewRequest(&chat.Messages{}, config)
	data, _ := json.Marshal(req)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["system"]; ok {
		t.Errorf("空 system 提示应省略 system 字段, got %#v", m["system"])
	}
	if _, ok := m["tools"]; ok {
		t.Errorf("无工具应省略 tools 字段")
	}
}
