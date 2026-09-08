package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// 模拟 buildRequest 的消息过滤逻辑：
// 当 tool_use 所在消息被过滤为空（跳过），但 tool_result 消息被保留时，
// 会导致 Anthropic API 400（tool_result 没有配对的 tool_use）。
func TestBuildRequest_ToolUseSkippedOrphansToolResult(t *testing.T) {
	l := &Loop{}

	// 模拟历史：assistant 消息含 tool_use，user 消息含 tool_result
	tu := chat.NewToolUseBlock("call_00_test", "execute_command")
	tu.Input = value.NewObjectFromMap(map[string]any{"command": "ls"})

	tr := chat.NewToolResultBlock("call_00_test", chat.Blocks{
		chat.NewFullTextBlock("file1.txt\nfile2.txt"),
	})

	assistantMsg := &chat.Message{
		Role:    chat.RoleAssistant,
		Content: chat.Blocks{tu},
	}
	userMsg := &chat.Message{
		Role:    chat.RoleUser,
		Content: chat.Blocks{tr},
	}

	// 模拟 buildRequest 中的过滤逻辑
	history := []*chat.Message{assistantMsg, userMsg}
	var messages []chat.Message
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		msg := *m
		msg.Content = l.blocksForContext(m.Content)
		if len(msg.Content) == 0 {
			continue // 跳过空消息
		}
		messages = append(messages, msg)
	}
	// 翻转（倒序收集的）
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// 验证：tool_use 和 tool_result 都应保留
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	// 检查 assistant 消息包含 tool_use
	assistantContent := messages[0].Content
	hasToolUse := false
	for _, b := range assistantContent {
		if _, ok := b.(*chat.ToolUseBlock); ok {
			hasToolUse = true
			break
		}
	}
	if !hasToolUse {
		t.Error("assistant 消息中缺少 tool_use 块，会导致 tool_result 无配对")
	}

	// 检查 user 消息包含 tool_result
	userContent := messages[1].Content
	hasToolResult := false
	for _, b := range userContent {
		if _, ok := b.(*chat.ToolResultBlock); ok {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Error("user 消息中缺少 tool_result 块")
	}
}

// 测试当 tool_result 的内容全被过滤时，占位文本能保证消息不被跳过。
func TestBuildRequest_ToolResultWithAllFilteredContent(t *testing.T) {
	l := &Loop{}

	// assistant 消息：含 tool_use
	tu := chat.NewToolUseBlock("call_01_test", "render_card")
	tu.Input = value.NewObjectFromMap(map[string]any{"data": "test"})

	// user 消息：tool_result 内容全是 CustomTextBlock（ForContext=false），会被过滤
	tr := chat.NewToolResultBlock("call_01_test", chat.Blocks{
		chat.NewCustomTextBlock(`{"resource":"data"}`, "resource_card"),
	})

	assistantMsg := &chat.Message{
		Role:    chat.RoleAssistant,
		Content: chat.Blocks{tu},
	}
	userMsg := &chat.Message{
		Role:    chat.RoleUser,
		Content: chat.Blocks{tr},
	}

	// 模拟 buildRequest 过滤
	history := []*chat.Message{assistantMsg, userMsg}
	var messages []chat.Message
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		msg := *m
		msg.Content = l.blocksForContext(m.Content)
		if len(msg.Content) == 0 {
			continue
		}
		messages = append(messages, msg)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d (tool_result 被跳过了？)", len(messages))
	}

	// tool_result 内容应包含占位文本
	userContent := messages[1].Content
	if len(userContent) == 0 {
		t.Fatal("user 消息内容为空")
	}
	trBlock, ok := userContent[0].(*chat.ToolResultBlock)
	if !ok {
		t.Fatalf("user 消息第一个块类型 = %T, want *chat.ToolResultBlock", userContent[0])
	}
	if len(trBlock.Content) == 0 {
		t.Error("tool_result.content 为空，会触发 Anthropic 400")
	}
}

// 测试多轮 tool_use/tool_result 配对完整性。
func TestBuildRequest_MultipleToolUsePairs(t *testing.T) {
	l := &Loop{}

	// 第一轮：tool_use + tool_result
	tu1 := chat.NewToolUseBlock("call_aaa", "search")
	tu1.Input = value.NewObjectFromMap(map[string]any{"query": "test"})
	tr1 := chat.NewToolResultBlock("call_aaa", chat.Blocks{
		chat.NewFullTextBlock("search results"),
	})

	// 第二轮：tool_use + tool_result
	tu2 := chat.NewToolUseBlock("call_bbb", "execute")
	tu2.Input = value.NewObjectFromMap(map[string]any{"cmd": "run"})
	tr2 := chat.NewToolResultBlock("call_bbb", chat.Blocks{
		chat.NewFullTextBlock("execution done"),
	})

	msg1 := &chat.Message{Role: chat.RoleAssistant, Content: chat.Blocks{tu1}}
	msg2 := &chat.Message{Role: chat.RoleUser, Content: chat.Blocks{tr1}}
	msg3 := &chat.Message{Role: chat.RoleAssistant, Content: chat.Blocks{tu2}}
	msg4 := &chat.Message{Role: chat.RoleUser, Content: chat.Blocks{tr2}}

	history := []*chat.Message{msg1, msg2, msg3, msg4}
	var messages []chat.Message
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		msg := *m
		msg.Content = l.blocksForContext(m.Content)
		if len(msg.Content) == 0 {
			continue
		}
		messages = append(messages, msg)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}

	// 验证配对：每条 tool_result 前一条必须有对应的 tool_use
	toolUseIDs := make(map[string]bool)
	for _, msg := range messages {
		for _, b := range msg.Content {
			if tu, ok := b.(*chat.ToolUseBlock); ok {
				toolUseIDs[tu.ID] = true
			}
			if tr, ok := b.(*chat.ToolResultBlock); ok {
				if !toolUseIDs[tr.ToolUseID] {
					t.Errorf("tool_result(%s) 没有配对的 tool_use", tr.ToolUseID)
				}
			}
		}
	}
}
