package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

func newTestTransfer() *Transfer {
	return &Transfer{
		doneManifest: &splitManifest{starts: new(util.SliceArray[uint64])},
		entries:      new(util.SliceArray[*Event]),
		chatClients:  new(util.SliceArray[*Client]),
		messageStore: &Store{
			history:     new(util.SliceArray[*chat.Message]),
			tempHistory: new(util.SliceArray[*chat.Message]),
		},
	}
}

// textBlockWithStart 构造一个带 start（事件流序号）的文本 block，模拟真实场景。
func textBlockWithStart(start uint64) *chat.TextBlock {
	b := chat.NewFullTextBlock("")
	b.SetStart(start)
	return b
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
	tr.entries.Append(&Event{Start: 0, Offset: 1, Blocks: chat.Blocks{textBlockWithStart(0)}})
	tr.entries.Append(&Event{Start: 1, Offset: 1, Blocks: chat.Blocks{textBlockWithStart(1)}})
	// tempHistory 有1条消息 (Start=2)——save 前 tempHistory 不读取，entries 兜底
	tr.messageStore.tempHistory.Append(&chat.Message{Start: 2, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{textBlockWithStart(2)}})

	// start=0: 只读 entries 2个（tempHistory 不读）
	events := tr.greaterStart(0)
	if len(events) != 2 {
		t.Fatalf("greaterStart(0) = %d events, want 2", len(events))
	}
}

func TestGreaterStart_OnlyHistory(t *testing.T) {
	// 模拟纯历史场景：entries 为空，history 有持久化消息
	tr := newTestTransfer()
	tr.messageStore.history.Append(&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{textBlockWithStart(0)}})
	tr.messageStore.history.Append(&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{textBlockWithStart(1)}})

	events := tr.greaterStart(0)
	if len(events) != 2 {
		t.Fatalf("greaterStart(0) history-only = %d events, want 2", len(events))
	}
	// 倒序: msg2 先, msg1 后
	if events[0].Start != 1 || events[1].Start != 0 {
		t.Errorf("got Start=[%d,%d], want [1,0]", events[0].Start, events[1].Start)
	}
}

func TestGreaterStart_DedupEntriesVsHistory(t *testing.T) {
	// entries 和 history 有重叠区间：entries 的事件被 history 覆盖
	tr := newTestTransfer()
	// entries: Start=0,1,2 (旧的运行时事件)
	for i := uint64(0); i < 3; i++ {
		tr.entries.Append(&Event{Start: i, Offset: 1, Blocks: chat.Blocks{textBlockWithStart(i)}})
	}
	// history: Start=0,1 (持久化的用户/助手消息，覆盖 entries 中的前两个)
	tr.messageStore.history.Append(&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{textBlockWithStart(0)}})
	tr.messageStore.history.Append(&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{textBlockWithStart(1)}})

	events := tr.greaterStart(0)
	// entries 中 Start=0,1 被 history 覆盖（删除），Start=2 保留
	// history 的 Start=0,1 追加
	// 总计: entries 保留1个 + history 2个 = 3个
	if len(events) != 3 {
		t.Fatalf("greaterStart(0) dedup = %d events, want 3", len(events))
	}
}

func TestGreaterStart_AllSources(t *testing.T) {
	// history 与 entries 两个数据源，验证完整合并（tempHistory 不读取）
	tr := newTestTransfer()
	// history: Start=0,1
	tr.messageStore.history.Append(&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{textBlockWithStart(0)}})
	tr.messageStore.history.Append(&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{textBlockWithStart(1)}})
	// tempHistory: Start=2（save 前不读）
	tr.messageStore.tempHistory.Append(&chat.Message{Start: 2, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{textBlockWithStart(2)}})
	// entries: Start=3
	tr.entries.Append(&Event{Start: 3, Offset: 1, Blocks: chat.Blocks{textBlockWithStart(3)}})

	events := tr.greaterStart(0)
	// entries 1个 + history 2个 = 3个（tempHistory 不读）
	if len(events) != 3 {
		t.Fatalf("greaterStart(0) all-sources = %d events, want 3", len(events))
	}
}

// TestMessageToEvent_ToolResultBlockPreservesOffset 验证 ToolResultBlock 消息
// 的 event 沿用 message 自身 Offset（2），不被重算缩成 1——否则 cl.start 推进不足、
// 消息会被 relay 反复重发（前端看到 tool_result 重复输出）。
func TestMessageToEvent_ToolResultBlockPreservesOffset(t *testing.T) {
	tr := newTestTransfer()
	customText := chat.NewCustomTextBlock(`[{"question":"Q"}]`, chat.AskUserTextType)
	customText.SetStart(1622)
	text := chat.NewFullTextBlock("已向用户提出问题")
	text.SetStart(1623)
	trb := chat.NewToolResultBlock("call_1", chat.Blocks{customText, text})

	tr.messageStore.history.Append(&chat.Message{
		Start: 1622, Offset: 2, Role: chat.RoleUser, Content: chat.Blocks{trb},
	})

	ev := messageToEvent(tr.messageStore.history.Get(0), 0)
	if ev.Start != 1622 || ev.Offset != 2 {
		t.Fatalf("messageToEvent = Start %d / Offset %d, want 1622 / 2（不应缩成 1）", ev.Start, ev.Offset)
	}
}

// TestReadEvents_ToolResultBlockNotReEmitted 验证去重后 tool_result 消息只发一次：
// entries 里的 standalone custom_text/text 被 history 覆盖，且 cl.start 正确推进到 1624，
// 第二次 readEvents 不再重复返回该消息。
func TestReadEvents_ToolResultBlockNotReEmitted(t *testing.T) {
	tr := newTestTransfer()
	customText := chat.NewCustomTextBlock(`[{"question":"Q"}]`, chat.AskUserTextType)
	customText.SetStart(1622)
	text := chat.NewFullTextBlock("已向用户提出问题")
	text.SetStart(1623)

	tr.entries.Append(&Event{Start: 1622, Offset: 1, Blocks: chat.Blocks{customText}})
	tr.entries.Append(&Event{Start: 1623, Offset: 1, Blocks: chat.Blocks{text}})

	trb := chat.NewToolResultBlock("call_1", chat.Blocks{customText, text})
	tr.messageStore.history.Append(&chat.Message{Start: 1622, Offset: 2, Role: chat.RoleUser, Content: chat.Blocks{trb}})

	cl := &Client{start: 0, readEvents: tr}

	events := tr.readEvents(cl)
	if len(events) != 1 {
		t.Fatalf("第一次 readEvents = %d 事件, want 1（entries 被去重，仅剩 tool_result 消息）", len(events))
	}
	if cl.start != 1624 {
		t.Fatalf("cl.start = %d, want 1624（应推进到消息终点，否则会重复发送）", cl.start)
	}

	if second := tr.readEvents(cl); len(second) != 0 {
		t.Fatalf("第二次 readEvents = %d 事件, want 0（tool_result 不应重复发送）", len(second))
	}
}

// TestMessageDeltaSurvivesMergeAndDedup 模拟第一轮 message 持久化后，
// readEvents + relay block 去重流程，验证 MessageDeltaBlock 不丢失。
func TestMessageDeltaSurvivesMergeAndDedup(t *testing.T) {
	tr := newTestTransfer()

	// entries：live MessageDeltaBlock(274) + DoneBlock(275)
	md := chat.NewMessageDeltaBlock(&chat.Usage{InputTokens: 100, OutputTokens: 50})
	md.SetStart(274)
	tr.entries.Append(&Event{Start: 274, Offset: 1, Blocks: chat.Blocks{md}})
	done := chat.NewDoneBlock()
	done.SetStart(275)
	tr.entries.Append(&Event{Start: 275, Offset: 1, Blocks: chat.Blocks{done}})

	// history：assistant message，Content 含 MessageDeltaBlock(start=274)
	md2 := chat.NewMessageDeltaBlock(&chat.Usage{InputTokens: 100, OutputTokens: 50})
	md2.SetStart(274)
	tr.messageStore.history.Append(&chat.Message{
		Start: 2, Offset: 273, Role: chat.RoleAssistant,
		Content: chat.Blocks{md2},
	})

	events := tr.greaterStart(0)

	// 验证 MessageDeltaBlock 在结果里（relay 不再按 lastSeq 去重，直接转发）
	found := false
	for _, ev := range events {
		for _, b := range ev.Blocks {
			if mb, ok := b.(*chat.MessageDeltaBlock); ok {
				found = true
				if mb.GetStart() != 274 {
					t.Errorf("MessageDeltaBlock start=%d, want 274", mb.GetStart())
				}
			}
		}
	}
	if !found {
		t.Error("MessageDeltaBlock not found in greaterStart result")
	}
}
