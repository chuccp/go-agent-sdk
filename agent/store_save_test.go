package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// mockMessageStore 记录 Append 调用，用于验证哪些消息被持久化。
type mockMessageStore struct {
	appended []*chat.Message
}

func (m *mockMessageStore) Append(sessionID string, messages []*chat.Message) error {
	m.appended = append(m.appended, messages...)
	return nil
}
func (m *mockMessageStore) LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error) {
	return nil, nil
}
func (m *mockMessageStore) LoadSummary(sessionID string) (*chat.Message, error) {
	return nil, nil
}
func (m *mockMessageStore) SaveSummary(sessionID string, summary *chat.Message) error {
	return nil
}

// 模拟真实场景：save() 在 chatWithStream 返回前被调用（客户端读事件），
// 此时 tool_use/tool_result 消息还没进 tempHistory。
// 验证这些消息最终能否被持久化。
func TestSave_ToolUseToolResultPersisted(t *testing.T) {
	ms := &mockMessageStore{}
	store := NewStore(1, "test-session", nil, nil, ms)

	// 1. 用户消息通过 SendBlock → append() 进入 history（模拟 buildRequest）
	userMsg := &chat.Message{
		Start:  100,
		Offset: 1,
		Role:   chat.RoleUser,
		Content: chat.Blocks{
			chat.NewUserBlock(1, chat.Blocks{chat.NewFullTextBlock("今天星期几")}, chat.Consume),
		},
	}
	store.AppendHistory(userMsg)

	// 2. 客户端读事件 → save(100) 被调用
	//    此时 tempHistory 里只有 userMsg
	err := store.save(100)
	if err != nil {
		t.Fatalf("save(100) failed: %v", err)
	}

	// 验证 userMsg 已入库
	if len(ms.appended) != 1 {
		t.Fatalf("expected 1 message saved, got %d", len(ms.appended))
	}

	// 3. chatWithStream 返回 → appendAssistantMessage → tool_use 消息加入 tempHistory
	toolUseMsg := &chat.Message{
		Start:  150,
		Offset: 1,
		Role:   chat.RoleAssistant,
		Content: chat.Blocks{
			chat.NewToolUseBlock("call_00", "execute_command"),
		},
	}
	store.AppendHistory(toolUseMsg)

	// 4. 工具执行 → appendUserMessage → tool_result 消息加入 tempHistory
	toolResultMsg := &chat.Message{
		Start:  160,
		Offset: 1,
		Role:   chat.RoleUser,
		Content: chat.Blocks{
			chat.NewToolResultBlock("call_00", chat.Blocks{chat.NewFullTextBlock("执行结果")}),
		},
	}
	store.AppendHistory(toolResultMsg)

	// 5. DoneBlock 保存 → save(200) 被调用
	doneMsg := &chat.Message{
		Start:  200,
		Offset: 1,
		Role:   chat.RoleAssistant,
		Content: chat.Blocks{
			chat.NewDoneBlock(),
		},
	}
	store.AppendHistory(doneMsg)

	err = store.save(200)
	if err != nil {
		t.Fatalf("save(200) failed: %v", err)
	}

	// 调试：打印入库消息详情
	for i, m := range ms.appended {
		var blockTypes []string
		for _, b := range m.Content {
			blockTypes = append(blockTypes, string(b.GetType()))
		}
		t.Logf("  appended[%d]: Start=%d Role=%s Blocks=%v", i, m.Start, m.Role, blockTypes)
	}

	// 验证所有消息都已入库
	if len(ms.appended) != 4 {
		t.Fatalf("expected 4 messages saved, got %d", len(ms.appended))
	}

	// 验证入库顺序和内容
	savedRoles := make([]string, len(ms.appended))
	for i, m := range ms.appended {
		savedRoles[i] = string(m.Role)
	}
	t.Logf("saved messages: %v", savedRoles)

	// 验证 tool_use 和 tool_result 在入库消息中
	hasToolUse := false
	hasToolResult := false
	for _, m := range ms.appended {
		for _, b := range m.Content {
			if _, ok := b.(*chat.ToolUseBlock); ok {
				hasToolUse = true
			}
			if _, ok := b.(*chat.ToolResultBlock); ok {
				hasToolResult = true
			}
		}
	}
	if !hasToolUse {
		t.Error("tool_use message not persisted to DB")
	}
	if !hasToolResult {
		t.Error("tool_result message not persisted to DB")
	}
}

// 模拟 save() 被多次调用的场景，验证不会重复入库。
func TestSave_NoDuplicatePersistence(t *testing.T) {
	ms := &mockMessageStore{}
	store := NewStore(1, "test-session", nil, nil, ms)

	msg1 := &chat.Message{Start: 100, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("msg1")}}
	msg2 := &chat.Message{Start: 200, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{chat.NewFullTextBlock("msg2")}}

	store.AppendHistory(msg1)
	store.AppendHistory(msg2)

	// 第一次 save(100) → 只保存 msg1
	err := store.save(100)
	if err != nil {
		t.Fatalf("save(100) failed: %v", err)
	}
	if len(ms.appended) != 1 {
		t.Fatalf("after save(100): expected 1, got %d", len(ms.appended))
	}

	// 第二次 save(200) → 保存 msg2，不重复保存 msg1
	err = store.save(200)
	if err != nil {
		t.Fatalf("save(200) failed: %v", err)
	}
	if len(ms.appended) != 2 {
		t.Fatalf("after save(200): expected 2, got %d", len(ms.appended))
	}
}

// 模拟 save(minStart) 中 minStart 不够大，导致部分消息未入库的场景。
func TestSave_PartialSaveWithLowMinStart(t *testing.T) {
	ms := &mockMessageStore{}
	store := NewStore(1, "test-session", nil, nil, ms)

	msg1 := &chat.Message{Start: 100, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("msg1")}}
	msg2 := &chat.Message{Start: 200, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{chat.NewToolUseBlock("call_00", "tool")}}
	msg3 := &chat.Message{Start: 300, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewToolResultBlock("call_00", chat.Blocks{chat.NewFullTextBlock("result")})}}

	store.AppendHistory(msg1)
	store.AppendHistory(msg2)
	store.AppendHistory(msg3)

	// save(150) → 只保存 msg1，msg2 和 msg3 留在 tempHistory
	err := store.save(150)
	if err != nil {
		t.Fatalf("save(150) failed: %v", err)
	}
	if len(ms.appended) != 1 {
		t.Fatalf("after save(150): expected 1, got %d", len(ms.appended))
	}

	// save(300) → 保存 msg2 和 msg3
	err = store.save(300)
	if err != nil {
		t.Fatalf("save(300) failed: %v", err)
	}
	if len(ms.appended) != 3 {
		t.Fatalf("after save(300): expected 3, got %d", len(ms.appended))
	}
}
