package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// noopMessageStore 是一个不做任何持久化的 MessageStore 实现，仅供测试。
type noopMessageStore struct{}

func (n *noopMessageStore) LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error) {
	return nil, nil
}
func (n *noopMessageStore) Append(sessionID string, messages []*chat.Message) error { return nil }
func (n *noopMessageStore) LoadSummary(sessionID string) (*chat.Message, error) {
	return nil, nil
}
func (n *noopMessageStore) SaveSummary(sessionID string, summary *chat.Message) error { return nil }

// memoryMessageStore 支持内存持久化的 MessageStore，测试历史消息场景时使用。
type memoryMessageStore struct {
	messages []*chat.Message
}

func (m *memoryMessageStore) LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error) {
	var result []*chat.Message
	for _, msg := range m.messages {
		if msg.Start+msg.Offset > since {
			result = append(result, msg)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}
func (m *memoryMessageStore) Append(sessionID string, messages []*chat.Message) error {
	m.messages = append(m.messages, messages...)
	return nil
}
func (m *memoryMessageStore) LoadSummary(sessionID string) (*chat.Message, error) {
	return nil, nil
}
func (m *memoryMessageStore) SaveSummary(sessionID string, summary *chat.Message) error { return nil }

func newTestTransfer() *Transfer {
	return &Transfer{
		entries:     new(util.SliceArray[*Event]),
		chatClients: new(util.SliceArray[*Client]),
		messageStore: &Store{
			history:      new(util.SliceArray[*chat.Message]),
			tempHistory:  new(util.SliceArray[*chat.Message]),
			doneManifest: &splitManifest{starts: new(util.SliceArray[uint64])},
			messageStore: &noopMessageStore{},
			maxBatchSize: 10,
		},
	}
}

func newTestTransferWithHistory() (*Transfer, *memoryMessageStore) {
	ms := &memoryMessageStore{}
	return &Transfer{
		entries:     new(util.SliceArray[*Event]),
		chatClients: new(util.SliceArray[*Client]),
		messageStore: &Store{
			history:      new(util.SliceArray[*chat.Message]),
			tempHistory:  new(util.SliceArray[*chat.Message]),
			doneManifest: &splitManifest{starts: new(util.SliceArray[uint64])},
			messageStore: ms,
			maxBatchSize: 10,
		},
	}, ms
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

	// start=0 应返回全部 5 个（升序）
	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("greaterStart(0) = %d events, want 5", len(events))
	}
	for i, e := range events {
		want := uint64(i)
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

	// start=2: greaterEntries 过滤 Start>=2 的事件（升序：2,3,4）
	events, err := tr.greaterStart(2)
	if err != nil {
		t.Fatalf("greaterStart(2) error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("greaterStart(2) = %d events, want 3", len(events))
	}
	if events[0].Start != 2 || events[1].Start != 3 || events[2].Start != 4 {
		t.Errorf("got Start=[%d,%d,%d], want [2,3,4]", events[0].Start, events[1].Start, events[2].Start)
	}
}

func TestGreaterStart_AllConsumed(t *testing.T) {
	tr := newTestTransfer()
	for i := uint64(0); i < 3; i++ {
		tr.entries.Append(&Event{No: 0, Start: i, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	}

	// start=3: 最后一个事件 Start=2, 2+1=3, 3>3 为 false，返回空
	events, err := tr.greaterStart(3)
	if err != nil {
		t.Fatalf("greaterStart(3) error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("greaterStart(3) = %d events, want 0", len(events))
	}
}

func TestGreaterStart_Empty(t *testing.T) {
	tr := newTestTransfer()
	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}
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

	// start=1: greaterEntries 过滤 Start>=1 的事件
	// 事件0: Start=0 < 1 → 不含; 事件1: Start=3 >= 1 ✓; 事件2: Start=5 >= 1 ✓
	events, err := tr.greaterStart(1)
	if err != nil {
		t.Fatalf("greaterStart(1) error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("greaterStart(1) = %d events, want 2", len(events))
	}

	// start=3: greaterEntries 过滤 Start>=3 的事件（升序：3,5）
	events, err = tr.greaterStart(3)
	if err != nil {
		t.Fatalf("greaterStart(3) error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("greaterStart(3) = %d events, want 2", len(events))
	}
	if events[0].Start != 3 || events[1].Start != 5 {
		t.Errorf("got Start=[%d,%d], want [3,5]", events[0].Start, events[1].Start)
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
	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("greaterStart(0) = %d events, want 2", len(events))
	}
}

func TestGreaterStart_OnlyHistory(t *testing.T) {
	// 模拟纯历史场景：entries 为空，history 有持久化消息
	tr, ms := newTestTransferWithHistory()
	ms.messages = append(ms.messages,
		&chat.Message{Start: 0, Offset: 1, Role: chat.RoleUser, Content: chat.Blocks{textBlockWithStart(0)}},
		&chat.Message{Start: 1, Offset: 1, Role: chat.RoleAssistant, Content: chat.Blocks{textBlockWithStart(1)}},
	)

	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("greaterStart(0) history-only = %d events, want 2", len(events))
	}
	// 升序: msg1 先, msg2 后
	if events[0].Start != 0 || events[1].Start != 1 {
		t.Errorf("got Start=[%d,%d], want [0,1]", events[0].Start, events[1].Start)
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

	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}
	// firstEvent.Start=0 <= start=0 → greaterEntries 过滤 Start>=0 → 3个
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

	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}
	// firstEvent.Start=3 > start=0 → 走 history 路径，返回 history 的 2 条消息；
	// tempHistory（save 前）与 entries（Start=3 在 gap 之后）均不读取。
	if len(events) != 2 {
		t.Fatalf("greaterStart(0) all-sources = %d events, want 2", len(events))
	}
}

// TestGreaterEntries_FilterAndSort 验证 greaterEntries 的过滤和排序逻辑。
func TestGreaterEntries_FilterAndSort(t *testing.T) {
	tr := newTestTransfer()
	for i := uint64(0); i < 5; i++ {
		tr.entries.Append(&Event{Start: i, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	}

	// Start >= 2: 应返回 Start=2,3,4（升序）
	events := tr.greaterEntries(2)
	if len(events) != 3 {
		t.Fatalf("greaterEntries(2) = %d events, want 3", len(events))
	}
	if events[0].Start != 2 || events[1].Start != 3 || events[2].Start != 4 {
		t.Errorf("got Start=[%d,%d,%d], want [2,3,4]", events[0].Start, events[1].Start, events[2].Start)
	}
}

// TestGreaterEntries_Empty 验证空 entries 时 greaterEntries 返回空。
func TestGreaterEntries_Empty(t *testing.T) {
	tr := newTestTransfer()
	events := tr.greaterEntries(0)
	if len(events) != 0 {
		t.Fatalf("greaterEntries(0) on empty = %d events, want 0", len(events))
	}
}

// TestGreaterEntries_AllFiltered 验证所有事件都被过滤掉时返回空。
func TestGreaterEntries_AllFiltered(t *testing.T) {
	tr := newTestTransfer()
	tr.entries.Append(&Event{Start: 0, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})
	tr.entries.Append(&Event{Start: 1, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}})

	events := tr.greaterEntries(5)
	if len(events) != 0 {
		t.Fatalf("greaterEntries(5) = %d events, want 0", len(events))
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

// TestReadEvents_ToolResultBlockNotReEmitted 验证 readEvents 正确推进 cl.start：
// entries 和 history 都有数据时，greaterStart 合并返回，cl.start 推进到最后一个事件的终点，
// 第二次 readEvents 返回空（无新事件）。
func TestReadEvents_ToolResultBlockNotReEmitted(t *testing.T) {
	tr, ms := newTestTransferWithHistory()
	customText := chat.NewCustomTextBlock(`[{"question":"Q"}]`, chat.AskUserTextType)
	customText.SetStart(1622)
	text := chat.NewFullTextBlock("已向用户提出问题")
	text.SetStart(1623)

	tr.entries.Append(&Event{Start: 1622, Offset: 1, Blocks: chat.Blocks{customText}})
	tr.entries.Append(&Event{Start: 1623, Offset: 1, Blocks: chat.Blocks{text}})

	trb := chat.NewToolResultBlock("call_1", chat.Blocks{customText, text})
	ms.messages = append(ms.messages, &chat.Message{Start: 1622, Offset: 2, Role: chat.RoleUser, Content: chat.Blocks{trb}})

	cl := &Client{start: 0, readEvents: tr}

	events, err := tr.readEvents(cl)
	if err != nil {
		t.Fatalf("第一次 readEvents error: %v", err)
	}
	// greaterStart: firstEvent.Start=1622 > start=0 → 跳过 entries，走 history 路径
	// history 返回 tool_result 消息，messageToEvent 过滤后返回
	if len(events) == 0 {
		t.Fatal("第一次 readEvents 应返回事件")
	}
	// cl.start 应推进到最后一个事件的终点
	if cl.start == 0 {
		t.Fatal("cl.start should have advanced")
	}

	// 第二次 readEvents：无新事件
	second, err := tr.readEvents(cl)
	if err != nil {
		t.Fatalf("第二次 readEvents error: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("第二次 readEvents = %d 事件, want 0（无新事件应返回空）", len(second))
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

	events, err := tr.greaterStart(0)
	if err != nil {
		t.Fatalf("greaterStart(0) error: %v", err)
	}

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
