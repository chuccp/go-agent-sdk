package agent

import (
	"context"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// newEventAt 构造一条只有序号的测试事件（Blocks 不参与过滤）。
func newEventAt(start uint64) *Event {
	return &Event{No: 0, Start: start, Offset: 1, Blocks: chat.Blocks{chat.NewFullTextBlock("")}}
}

// TestLastClient_NoStart_DoesNotReplayBacklog 钉住「调用方忘记传 start」的契约：
// 前端 QueryUint64 缺参得 0（cast.ToUint64("") == 0），LastClient 必须回落到自动
// 分配的最新序号——不回放积压历史，且新事件仍能正常收到。
func TestLastClient_NoStart_DoesNotReplayBacklog(t *testing.T) {
	tr := newTestTransfer()
	for i := uint64(1); i <= 3; i++ {
		tr.entries.Append(newEventAt(i))
	}
	tr.start.Store(3)

	cl := tr.lastClient(context.Background(), 0)
	defer cl.Close()

	events, err := tr.greaterStart(cl.start, cl.isLast)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("未传 start 时不应回放积压，却拿到 %d 条", len(events))
	}

	tr.entries.Append(newEventAt(4))
	events, err = tr.greaterStart(cl.start, cl.isLast)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Start != 4 {
		t.Fatalf("新事件应能收到，却拿到 %v", events)
	}
}

// TestLastClient_ExplicitStartWinsIfAhead 记录 start 参数的实际语义：
// 它只在「大于自动分配序号」时生效（max），正常续传场景恒被自动序号覆盖。
func TestLastClient_ExplicitStartWinsIfAhead(t *testing.T) {
	tr := newTestTransfer()
	tr.start.Store(3)

	cl := tr.lastClient(context.Background(), 100)
	defer cl.Close()

	if cl.start != 100 {
		t.Fatalf("start=100 大于当前序号时应生效，得到 cl.start=%d", cl.start)
	}

	// 新事件的序号远小于游标 100，会被 greaterEntries 全部过滤掉。
	tr.entries.Append(newEventAt(4))
	events, err := tr.greaterStart(cl.start, cl.isLast)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0，得到 %v", events)
	}
}
