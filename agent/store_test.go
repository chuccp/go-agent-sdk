package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// seedMessageStore 是返回固定消息列表的 MessageStore，按 Start 升序存储。
// LoadAfter 遵循接口契约：返回 Start+Offset > since 的消息，最多 limit 条。
type seedMessageStore struct {
	messages []*chat.Message
	summary  *chat.Message
}

func (m *seedMessageStore) LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error) {
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

func (m *seedMessageStore) Append(sessionID string, messages []*chat.Message) error {
	m.messages = append(m.messages, messages...)
	return nil
}

func (m *seedMessageStore) LoadSummary(sessionID string) (*chat.Message, error) {
	return m.summary, nil
}

func (m *seedMessageStore) SaveSummary(sessionID string, summary *chat.Message) error {
	m.summary = summary
	return nil
}

// seedMsg 构造一条只有 Start/Offset 的测试消息（Content 不参与 LoadMessagesAfter）。
func seedMsg(start, offset uint64) *chat.Message {
	return &chat.Message{Start: start, Offset: offset, Role: chat.RoleUser}
}

// noopSendEvent 丢弃事件、维护独立序号的 SendEvent 实现，仅供测试。
type noopSendEvent struct{ seq uint64 }

func (n *noopSendEvent) sendEvent(_ *Event)  {}
func (n *noopSendEvent) getSeq() uint64      { return n.seq }
func (n *noopSendEvent) storeSeq(seq uint64) { n.seq = seq }
func (n *noopSendEvent) getAndAddSeq() uint64 {
	n.seq++
	return n.seq
}

func newStoreWith(ms MessageStore) *Store {
	return NewStore(0, "test", &noopSendEvent{}, nil, ms)
}

func starts(ms []*chat.Message) []uint64 {
	out := make([]uint64, len(ms))
	for i, m := range ms {
		out[i] = m.Start
	}
	return out
}

func equalStarts(ms []*chat.Message, want []uint64) bool {
	if len(ms) != len(want) {
		return false
	}
	for i := range ms {
		if ms[i].Start != want[i] {
			return false
		}
	}
	return true
}

// TestLoadMessagesAfter_SpanningMessage 是本次修复的核心回归用例：
// since 落在一条消息中间（Start <= since < Start+Offset）时，必须返回该条消息，
// 供 messageToEvent 截断重放剩余 block；旧实现用 Start >= since 会漏掉它。
func TestLoadMessagesAfter_SpanningMessage(t *testing.T) {
	store := newStoreWith(&seedMessageStore{messages: []*chat.Message{
		seedMsg(1, 3), // 占 1,2,3
		seedMsg(4, 1), // 占 4
		seedMsg(5, 1), // 占 5
	}})

	got, err := store.LoadMessagesAfter(2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{1, 4, 5}; !equalStarts(got, want) {
		t.Fatalf("LoadMessagesAfter(2) = %v, want %v", starts(got), want)
	}
}

// TestLoadMessagesAfter_ExactBoundary 验证边界语义：消息结束位置 == since 时不返回
// （已完全消费），消息起始位置 == since 时返回。
func TestLoadMessagesAfter_ExactBoundary(t *testing.T) {
	store := newStoreWith(&seedMessageStore{messages: []*chat.Message{
		seedMsg(1, 1), // 占 [1,1]
		seedMsg(2, 2), // 占 [2,3]
		seedMsg(4, 1), // 占 [4,4]
	}})

	got, err := store.LoadMessagesAfter(2)
	if err != nil {
		t.Fatal(err)
	}
	// seedMsg(1,1) 结束位置 2 不 > 2 → 排除；其余包含。
	if want := []uint64{2, 4}; !equalStarts(got, want) {
		t.Fatalf("LoadMessagesAfter(2) = %v, want %v", starts(got), want)
	}
}

func TestLoadMessagesAfter_MaxBatchSize(t *testing.T) {
	var msgs []*chat.Message
	for i := 1; i <= 10; i++ {
		msgs = append(msgs, seedMsg(uint64(i), 1))
	}
	store := newStoreWith(&seedMessageStore{messages: msgs})
	store.maxBatchSize = 4 // 直接设批次大小，验证 LoadAfter 按批次截断

	got, err := store.LoadMessagesAfter(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{1, 2, 3, 4}; !equalStarts(got, want) {
		t.Fatalf("LoadMessagesAfter(0) = %v, want %v", starts(got), want)
	}
}

// TestLoadMessagesAfter_AdvancingCursor 验证缓存复用：第二次调用 with 更大的 since
// 从缓存末尾继续拉取，既不重复也不遗漏。
func TestLoadMessagesAfter_AdvancingCursor(t *testing.T) {
	var msgs []*chat.Message
	for i := 1; i <= 10; i++ {
		msgs = append(msgs, seedMsg(uint64(i), 1))
	}
	store := newStoreWith(&seedMessageStore{messages: msgs})
	store.maxBatchSize = 4

	got1, err := store.LoadMessagesAfter(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{1, 2, 3, 4}; !equalStarts(got1, want) {
		t.Fatalf("first = %v, want %v", starts(got1), want)
	}

	got2, err := store.LoadMessagesAfter(5)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{5, 6, 7, 8}; !equalStarts(got2, want) {
		t.Fatalf("second = %v, want %v", starts(got2), want)
	}
}

func TestLoadMessagesAfter_SummaryBoundary(t *testing.T) {
	// since == summary.Start=5：从 5 开始回放，返回 [5,6,7]。
	store := newStoreWith(&seedMessageStore{
		messages: []*chat.Message{
			seedMsg(1, 1), seedMsg(2, 1), seedMsg(3, 1), seedMsg(4, 1),
			seedMsg(5, 1), seedMsg(6, 1), seedMsg(7, 1),
		},
		summary: &chat.Message{Start: 5},
	})

	got, err := store.LoadMessagesAfter(5)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{5, 6, 7}; !equalStarts(got, want) {
		t.Fatalf("LoadMessagesAfter(5) = %v, want %v", starts(got), want)
	}
}

// TestLoadMessagesAfter_SinceBeforeSummary 验证 since < summary.Start 的边界：
// summary.Start 只是压缩节点（只影响 LLM 上下文），回放绝不能因它丢掉压缩节点之前的
// 用户历史——应返回 since 之后的完整原始消息，包括 Start < summary.Start 的旧消息。
func TestLoadMessagesAfter_SinceBeforeSummary(t *testing.T) {
	store := newStoreWith(&seedMessageStore{
		messages: []*chat.Message{
			seedMsg(1, 1), seedMsg(2, 1), seedMsg(3, 1), seedMsg(4, 1),
			seedMsg(5, 1), seedMsg(6, 1), seedMsg(7, 1),
		},
		summary: &chat.Message{Start: 5},
	})

	got, err := store.LoadMessagesAfter(3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{3, 4, 5, 6, 7}; !equalStarts(got, want) {
		t.Fatalf("LoadMessagesAfter(3) = %v, want %v", starts(got), want)
	}
}

// TestLoadMessagesAfter_SinceBeforeSummary_AfterWarmup 验证：缓存已被更大的 since 预热
// （缓存只含活跃消息，从 summary.Start 起），随后用更小的 since（< summary.Start）请求时，
// 仍必须返回压缩节点之前的完整原始历史——不能因为缓存里没有旧消息就漏掉它们。
func TestLoadMessagesAfter_SinceBeforeSummary_AfterWarmup(t *testing.T) {
	store := newStoreWith(&seedMessageStore{
		messages: []*chat.Message{
			seedMsg(1, 1), seedMsg(2, 1), seedMsg(3, 1), seedMsg(4, 1),
			seedMsg(5, 1), seedMsg(6, 1), seedMsg(7, 1),
		},
		summary: &chat.Message{Start: 5},
	})

	// 先用 since=6（> summary.Start）预热，缓存只装活跃消息。
	if _, err := store.LoadMessagesAfter(6); err != nil {
		t.Fatal(err)
	}
	// 再用 since=3（< summary.Start）请求，必须返回 [3,4,5,6,7]。
	got, err := store.LoadMessagesAfter(3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{3, 4, 5, 6, 7}; !equalStarts(got, want) {
		t.Fatalf("LoadMessagesAfter(3) = %v, want %v", starts(got), want)
	}
}

func TestLoadMessagesAfter_EmptyStore(t *testing.T) {
	store := newStoreWith(&seedMessageStore{})
	got, err := store.LoadMessagesAfter(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0, got %d", len(got))
	}
}

func TestLoadMessagesAfter_NilStore(t *testing.T) {
	store := newStoreWith(nil)
	got, err := store.LoadMessagesAfter(0)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

// TestStore_LoadAllHistory 验证 LoadAllHistory 把持久化历史完整拉进缓存，
// 供 History()（LLM 上下文）在进程重启后拿到完整历史。
func TestStore_LoadAllHistory(t *testing.T) {
	store := newStoreWith(&seedMessageStore{messages: []*chat.Message{
		seedMsg(1, 1), seedMsg(2, 1), seedMsg(3, 1),
	}})

	if err := store.LoadAllHistory(); err != nil {
		t.Fatal(err)
	}

	if want := []uint64{1, 2, 3}; !equalStarts(store.History(), want) {
		t.Fatalf("History() = %v, want %v", starts(store.History()), want)
	}
}
