package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

func TestTmpToolResultContextFilter(t *testing.T) {
	l := &Loop{}
	big := `{"resource":"content","items":[{"title":"《姑苏擎天一柱》"}]}`

	// 复刻 format_resource：一个大卡片块 + 一句回执
	writer := chat.NewBlockStream(nil)
	trw := chat.NewToolResultBlockStream(writer, "call_06")
	trw.Block(chat.NewCustomTextBlock(big, chat.TextType("resource_card")))
	trw.FullText("已输出 1 条内容资源卡片")
	group := writer.ReadBlockGroup()

	orig := chat.NewToolResultBlock("call_06", group.Content)
	history := chat.Blocks{orig}

	filtered := l.blocksForContext(history)
	payload, _ := json.Marshal(chat.Request{Messages: []chat.Message{{Role: chat.RoleUser, Content: filtered}}})

	if strings.Contains(string(payload), "姑苏擎天一柱") {
		t.Fatalf("大 JSON 仍然回灌了: %s", payload)
	}
	if !strings.Contains(string(payload), "已输出 1 条") {
		t.Fatalf("回执丢失: %s", payload)
	}
	t.Logf("进 LLM 的 payload: %s", payload)

	// 历史必须原封不动（断线重连要靠它重建卡片）
	if len(orig.Content) != 2 {
		t.Fatalf("原始历史被就地改坏了, len=%d", len(orig.Content))
	}
	raw, _ := json.Marshal(orig)
	if !strings.Contains(string(raw), "姑苏擎天一柱") {
		t.Fatalf("历史里的卡片丢了: %s", raw)
	}
	t.Logf("历史仍完整: %s", raw)

	// 全是不回灌块时要补占位，不能留空
	only := chat.NewToolResultBlock("call_07", chat.Blocks{
		chat.NewCustomTextBlock(big, chat.TextType("plan_card")),
	})
	got := l.blocksForContext(chat.Blocks{only})[0].(*chat.ToolResultBlock)
	if len(got.Content) == 0 {
		t.Fatal("空 tool_result.content 会导致 Anthropic 400")
	}
	t.Logf("占位块: %+v", got.Content[0])
}
