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

// TestLastClient_StartNotAhead_SameAsOmitting 钉住「start 可以不传」的正面表述：
// 只要传入值不大于当前序号，max 的结果与完全不传（0）一致——传了不影响，不传也能继续。
// 两次调用会各自消耗一个序号，故用两个同初始状态的 Transfer 对比。
func TestLastClient_StartNotAhead_SameAsOmitting(t *testing.T) {
	omittedTr := newTestTransfer()
	omittedTr.start.Store(3)
	omitted := omittedTr.lastClient(context.Background(), 0)
	defer omitted.Close()

	explicitTr := newTestTransfer()
	explicitTr.start.Store(3)
	explicit := explicitTr.lastClient(context.Background(), 2)
	defer explicit.Close()

	if omitted.start != explicit.start {
		t.Fatalf("start=2 不大于当前序号时应与不传等价，得到 不传=%d 传2=%d",
			omitted.start, explicit.start)
	}
}

// TestLastClient_ExplicitStartWinsIfAhead 记录 start 参数唯一生效的场景：
// 只在「大于自动分配序号」时作为起点下限（max），保证客户端起点不早于已知进度。
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
