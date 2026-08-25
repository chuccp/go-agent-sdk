package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// 工具往 tool_result 里塞的 CustomTextBlock（资源卡片 JSON）不能进 LLM 上下文，
// 但必须留在历史里——断线重连时前端靠回放里的这个块重建卡片。
func TestBlocksForContext_FiltersNestedCustomText(t *testing.T) {
	l := &Loop{}
	card := `{"resource":"content","items":[{"title":"《姑苏擎天一柱》"}]}`

	writer := chat.NewBlockStream(nil)
	trw := chat.NewToolResultBlockStream(writer, "call_06")
	trw.Block(chat.NewCustomTextBlock(card, chat.TextType("resource_card")))
	trw.FullText("已输出 1 条内容资源卡片")

	original := chat.NewToolResultBlock("call_06", writer.ReadBlockGroup().Content)

	filtered := l.blocksForContext(chat.Blocks{original})
	payload, err := json.Marshal(chat.Request{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: filtered}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	if strings.Contains(string(payload), "姑苏擎天一柱") {
		t.Errorf("卡片 JSON 回灌进了 LLM 请求体: %s", payload)
	}
	if !strings.Contains(string(payload), "已输出 1 条") {
		t.Errorf("回执文本丢失: %s", payload)
	}

	// 过滤走的是拷贝，原始块不能被就地改动
	if len(original.Content) != 2 {
		t.Errorf("历史块被就地修改, len(Content) = %d, want 2", len(original.Content))
	}
	history, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	if !strings.Contains(string(history), "姑苏擎天一柱") {
		t.Errorf("历史里的卡片丢失，断线重连将无法重建: %s", history)
	}
}

// tool_result.content 全是不回灌块时要补占位。留空会让 buildRequest 跳过整条消息，
// 导致 tool_use 没有配对的 tool_result，Anthropic 直接 400。
func TestBlocksForContext_EmptyToolResultGetsPlaceholder(t *testing.T) {
	l := &Loop{}
	only := chat.NewToolResultBlock("call_07", chat.Blocks{
		chat.NewCustomTextBlock(`{"plan_id":7}`, chat.TextType("plan_card")),
	})

	filtered := l.blocksForContext(chat.Blocks{only})
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	got, ok := filtered[0].(*chat.ToolResultBlock)
	if !ok {
		t.Fatalf("filtered[0] 类型 = %T, want *chat.ToolResultBlock", filtered[0])
	}
	if len(got.Content) == 0 {
		t.Error("tool_result.content 为空，会触发 Anthropic 400")
	}
}

// 不含需剔除子块的 tool_result 应原样返回，避免无谓拷贝。
func TestBlocksForContext_KeepsCleanToolResultAsIs(t *testing.T) {
	l := &Loop{}
	clean := chat.NewToolResultBlock("call_08", chat.Blocks{
		chat.NewFullTextBlock("plain result"),
	})

	filtered := l.blocksForContext(chat.Blocks{clean})
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0] != chat.Block(clean) {
		t.Error("无需过滤时不应拷贝")
	}
}
