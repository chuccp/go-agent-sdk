package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

func newTestTransfer() *Transfer {
	return &Transfer{
		entries: new(util.SliceArray[*Event]),
		messageStore: &Store{
			history:     new(util.SliceArray[*chat.Message]),
			tempHistory: new(util.SliceArray[*chat.Message]),
		},
	}
}

func TestGreaterStart_OnlyEntries(t *testing.T) {
	tr := newTestTransfer()
	// 写入 5 个事件 (Start=0..4, Offset=1)
	for i := uint64(0); i < 5; i++ {
		tr.entries.Append(&Event{No: 0, Start: i, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	}

	// start=0 应返回全部 5 个（greaterStart 返回倒序，readEvents 负责反转）
	events := tr.greaterStart(0)
	if len(events) != 5 {
		t.Fatalf("greaterStart(0) = %d events, want 5", len(events))
	}
	for i, e := range events {
		want := uint64(4 - i)
		if e.Start != want {
			t.Errorf("events[%d].Start = %d, want %d", i, e.Start, want)
		}
	}
}

func TestGreaterStart_FilterByStart(t *testing.T) {
	tr := newTestTransfer()
	for i := uint64(0); i < 5; i++ {
		tr.entries.Append(&Event{No: 0, Start: i, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	}

	// start=2: 倒序遍历 Start=4(5>2✓),3(4>2✓),2(3>2✓),1(2>2✗→停止)
	// 返回3个事件：Start=4,3,2（倒序）
	events := tr.greaterStart(2)
	if len(events) != 3 {
		t.Fatalf("greaterStart(2) = %d events, want 3", len(events))
	}
	if events[0].Start != 4 || events[1].Start != 3 || events[2].Start != 2 {
		t.Errorf("got Start=[%d,%d,%d], want [4,3,2]", events[0].Start, events[1].Start, events[2].Start)
	}
}

func TestGreaterStart_AllConsumed(t *testing.T) {
	tr := newTestTransfer()
	for i := uint64(0); i < 3; i++ {
		tr.entries.Append(&Event{No: 0, Start: i, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	}

	// start=3: 最后一个事件 Start=2, 2+1=3, 3>3 为 false，返回空
	events := tr.greaterStart(3)
	if len(events) != 0 {
		t.Fatalf("greaterStart(3) = %d events, want 0", len(events))
	}
}

func TestGreaterStart_Empty(t *testing.T) {
	tr := newTestTransfer()
	events := tr.greaterStart(0)
	if len(events) != 0 {
		t.Fatalf("greaterStart(0) on empty = %d events, want 0", len(events))
	}
}

func TestGreaterStart_LargeOffset(t *testing.T) {
	tr := newTestTransfer()
	// 事件有不同 Offset
	tr.entries.Append(&Event{No: 0, Start: 0, Offset: 3, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	tr.entries.Append(&Event{No: 0, Start: 3, Offset: 2, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	tr.entries.Append(&Event{No: 0, Start: 5, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})

	// start=1: 事件0: 0+3=3>1 ✓, 事件1: 3+2=5>1 ✓, 事件2: 5+1=6>1 ✓
	events := tr.greaterStart(1)
	if len(events) != 3 {
		t.Fatalf("greaterStart(1) = %d events, want 3", len(events))
	}

	// start=3: 事件0: 0+3=3>3 ✗ → 停止，只返回事件0之前（倒序遍历）
	// 倒序: 先查事件2 (5+1=6>3 ✓), 再事件1 (3+2=5>3 ✓), 再事件0 (0+3=3>3 ✗ → 停止)
	events = tr.greaterStart(3)
	if len(events) != 2 {
		t.Fatalf("greaterStart(3) = %d events, want 2", len(events))
	}
	if events[0].Start != 5 || events[1].Start != 3 {
		t.Errorf("got Start=[%d,%d], want [5,3]", events[0].Start, events[1].Start)
	}
}

func TestGreaterStart_WithTempHistory(t *testing.T) {
	tr := newTestTransfer()
	// entries 有2个事件 (Start=0,1)
	tr.entries.Append(&Event{Start: 0, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	tr.entries.Append(&Event{Start: 1, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	// tempHistory 有1条消息 (Start=2) — 来自 buildRequest 写入
	tr.messageStore.tempHistory.Append(&chat.Message{Start: 2, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("hello")}})

	// start=0: entries 返回2个 + tempHistory 返回1个 = 3个
	events := tr.greaterStart(0)
	if len(events) != 3 {
		t.Fatalf("greaterStart(0) = %d events, want 3", len(events))
	}
}

func TestGreaterStart_OnlyHistory(t *testing.T) {
	// 模拟纯历史场景：entries 为空，history 有持久化消息
	tr := newTestTransfer()
	tr.messageStore.history.Append(&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("msg1")}})
	tr.messageStore.history.Append(&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{chat.NewFullTextBlock("msg2")}})

	events := tr.greaterStart(0)
	if len(events) != 2 {
		t.Fatalf("greaterStart(0) history-only = %d events, want 2", len(events))
	}
	// 倒序: msg2 先, msg1 后
	if events[0].Start != 1 || events[1].Start != 0 {
		t.Errorf("got Start=[%d,%d], want [1,0]", events[0].Start, events[1].Start)
	}
}

func TestGreaterStart_DedupEntriesVsTempHistory(t *testing.T) {
	// entries 和 tempHistory 有重叠区间：entries 的事件被 tempHistory 覆盖
	tr := newTestTransfer()
	// entries: Start=0,1,2 (旧的运行时事件)
	for i := uint64(0); i < 3; i++ {
		tr.entries.Append(&Event{Start: i, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	}
	// tempHistory: Start=0,1 (持久化的用户/助手消息，覆盖 entries 中的前两个)
	tr.messageStore.tempHistory.Append(&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("u")}})
	tr.messageStore.tempHistory.Append(&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{chat.NewFullTextBlock("a")}})

	events := tr.greaterStart(0)
	// entries 中 Start=0,1 被 tempHistory 覆盖（删除），Start=2 保留
	// tempHistory 的 Start=0,1 追加
	// 总计: entries 保留1个 + tempHistory 2个 = 3个
	if len(events) != 3 {
		t.Fatalf("greaterStart(0) dedup = %d events, want 3", len(events))
	}
}

func TestGreaterStart_AllSources(t *testing.T) {
	// 三个数据源都有数据，验证完整合并
	tr := newTestTransfer()
	// history: Start=0,1
	tr.messageStore.history.Append(&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("")}})
	tr.messageStore.history.Append(&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{chat.NewFullTextBlock("")}})
	// tempHistory: Start=2
	tr.messageStore.tempHistory.Append(&chat.Message{Start: 2, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("")}})
	// entries: Start=3
	tr.entries.Append(&Event{Start: 3, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})

	events := tr.greaterStart(0)
	// entries 1个 + tempHistory 1个 + history 2个 = 4个
	if len(events) != 4 {
		t.Fatalf("greaterStart(0) all-sources = %d events, want 4", len(events))
	}
}
