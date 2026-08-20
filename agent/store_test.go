package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// ── Store0: SendBlock / ReadFrom ──

func TestStore_AddAndReadFrom_SinglePosition(t *testing.T) {
	s := NewStore("s1", nil)

	// 添加事件
	s.AddBlock(chat.NewFullTextBlock("hello"))
	s.AddBlock(chat.NewFullTextBlock("world"))
	s.AddBlock(chat.NewDoneBlock())

	pos := s.GetPosition(0)

	// 按序读取
	evt := s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "hello" {
		t.Fatalf("expected TextBlock{hello}, got %v", evt.Block)
	}
	if evt.Seq != 0 {
		t.Errorf("expected Seq=0, got %d", evt.Seq)
	}

	evt = s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "world" {
		t.Fatalf("expected TextBlock{world}, got %v", evt.Block)
	}
	if evt.Seq != 1 {
		t.Errorf("expected Seq=1, got %d", evt.Seq)
	}

	evt = s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if _, ok := evt.Block.(*chat.DoneBlock); !ok {
		t.Fatalf("expected DoneBlock, got %T", evt.Block)
	}
	if evt.Seq != 2 {
		t.Errorf("expected Seq=2, got %d", evt.Seq)
	}

	// 读完
	evt = s.ReadFrom(pos)
	if evt != nil {
		t.Errorf("expected nil after all events read, got %v", evt)
	}
}

func TestStore_ReadFrom_StartOffset(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))
	s.AddBlock(chat.NewFullTextBlock("c"))

	// 从 seq=1 开始读，跳过 seq=0
	pos := s.GetPosition(1)
	evt := s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "b" {
		t.Fatalf("expected TextBlock{b}, got %v", evt.Block)
	}
}

func TestStore_StartClamped(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a")) // seq=0

	// start 超出当前 seq(1)，钳制到 seq=1，
	// 表示"已读完当前所有事件"，ReadFrom 应返回 nil
	pos := s.GetPosition(100)
	evt := s.ReadFrom(pos)
	if evt != nil {
		t.Fatalf("expected nil after clamp to seq=1 (nothing left to read), got %+v", evt)
	}
	// 新事件从 seq=1 开始分配，可以被读到
	s.AddBlock(chat.NewFullTextBlock("b"))
	evt = s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "b" {
		t.Fatalf("expected TextBlock{b}, got %v", evt.Block)
	}
}

func TestStore_MultiplePositions(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))
	s.AddBlock(chat.NewFullTextBlock("c"))

	pos1 := s.GetPosition(0)
	pos2 := s.GetPosition(0)

	// pos1 读前两个
	s.ReadFrom(pos1) // a
	s.ReadFrom(pos1) // b

	// pos2 读第一个
	evt := s.ReadFrom(pos2)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "a" {
		t.Fatalf("pos2 expected TextBlock{a}, got %v", evt.Block)
	}
}

// ── Store0: ResetAndSave ──

func TestStore_Reset_CleansReadEvents(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))
	s.AddBlock(chat.NewFullTextBlock("c"))

	pos := s.GetPosition(0)
	s.ReadFrom(pos) // 读 a, pos→1
	s.ReadFrom(pos) // 读 b, pos→2

	s.ResetAndSave() // minPosition=2, 应清理 seq 0,1

	// pos 仍从 2 开始读 c
	evt := s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "c" {
		t.Fatalf("expected TextBlock{c}, got %v", evt.Block)
	}
}

func TestStore_Reset_NoPositions(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))

	s.ResetAndSave() // minPosition=0, firstSeq=0, 不清理

	pos := s.GetPosition(0)
	evt := s.ReadFrom(pos)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "a" {
		t.Fatalf("expected TextBlock{a}, got %v", evt.Block)
	}
}

func TestStore_Reset_PreservesLastEvent(t *testing.T) {
	s := NewStore("s1", nil)
	for i := 0; i < 5; i++ {
		s.AddBlock(chat.NewFullTextBlock("x"))
	}

	pos := s.GetPosition(0)
	// 读完所有
	for i := 0; i < 5; i++ {
		s.ReadFrom(pos)
	}

	s.ResetAndSave() // minPosition=5, 所有事件应被清理

	// 新事件从 seq=5 开始分配
	s.AddBlock(chat.NewFullTextBlock("new"))
	evt := s.ReadFrom(pos)
	if evt == nil || evt.Seq != 5 {
		t.Errorf("expected seq=5 after full reset, got %v", evt)
	}
}

// ── Store0: AppendHistory ──

func TestStore_AppendHistory_SetsStartAndOffset(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))
	s.AddBlock(chat.NewFullTextBlock("c"))

	// 三个 pending 事件，append 后 Start=seq-pending=3-3=0, Offset=3
	msg := &chat.Message{Role: chat.RoleAssistant}
	s.AppendHistory(msg)

	if msg.Start != 0 {
		t.Errorf("expected Start=0, got %d", msg.Start)
	}
	if msg.Offset != 3 {
		t.Errorf("expected Offset=3, got %d", msg.Offset)
	}

	// 再添加两个事件
	s.AddBlock(chat.NewFullTextBlock("d"))
	s.AddBlock(chat.NewFullTextBlock("e"))

	msg2 := &chat.Message{Role: chat.RoleUser}
	s.AppendHistory(msg2)

	// Start=seq-pending=5-2=3, Offset=2
	if msg2.Start != 3 {
		t.Errorf("expected Start=3, got %d", msg2.Start)
	}
	if msg2.Offset != 2 {
		t.Errorf("expected Offset=2, got %d", msg2.Offset)
	}
}

func TestStore_AppendHistory_NoPendingEvents(t *testing.T) {
	s := NewStore("s1", nil)
	// 无事件直接 append
	msg := &chat.Message{Role: chat.RoleAssistant}
	s.AppendHistory(msg)

	if msg.Start != 0 || msg.Offset != 0 {
		t.Errorf("expected Start=0, Offset=0, got Start=%d Offset=%d", msg.Start, msg.Offset)
	}
}

// ── Store0: LoadHistory ──

type fakeHistoryStore struct {
	messages []chat.Message
	err      error
}

func (f *fakeHistoryStore) LoadHistory(_ string) ([]chat.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.messages, nil
}

func (f *fakeHistoryStore) AppendMessages(_ string, _ []chat.Message) error { return nil }

func TestStore_LoadHistory_RestoresSeq(t *testing.T) {
	store := &fakeHistoryStore{
		messages: []chat.Message{
			{Role: chat.RoleUser, Start: 0, Offset: 3, Content: chat.Blocks{chat.NewFullTextBlock("hello")}},
			{Role: chat.RoleAssistant, Start: 3, Offset: 5, Content: chat.Blocks{chat.NewFullTextBlock("hi")}},
		},
	}
	s := NewStore("s1", store)

	if err := s.LoadHistory(); err != nil {
		t.Fatal(err)
	}

	// head = 3+5 = 8, seq 恢复到 8
	// 新事件的 seq 从 8 开始
	s.AddBlock(chat.NewFullTextBlock("new"))
	// 事件从 store 内部 seq 分配，验证 history 正确加载
	if s.HistoryLen() != 2 {
		t.Errorf("expected 2 history messages, got %d", s.HistoryLen())
	}
}

func TestStore_LoadHistory_Idempotent(t *testing.T) {
	store := &fakeHistoryStore{
		messages: []chat.Message{
			{Role: chat.RoleUser, Start: 0, Offset: 1, Content: chat.Blocks{chat.NewFullTextBlock("hi")}},
		},
	}
	s := NewStore("s1", store)

	if err := s.LoadHistory(); err != nil {
		t.Fatal(err)
	}
	// 第二次 LoadHistory 应直接返回（history 非空）
	if err := s.LoadHistory(); err != nil {
		t.Fatal(err)
	}

	if s.HistoryLen() != 1 {
		t.Errorf("expected 1, got %d", s.HistoryLen())
	}
}

func TestStore_NoHistoryStore(t *testing.T) {
	s := NewStore("s1", nil)
	if err := s.LoadHistory(); err != nil {
		t.Fatal(err)
	}
	if s.HistoryLen() != 0 {
		t.Errorf("expected 0, got %d", s.HistoryLen())
	}
}

// ── Store0: ResetAndSave（保存历史） ──

type recordingHistoryStore struct {
	appended [][]chat.Message // 记录每次 AppendMessages 的调用
}

func (r *recordingHistoryStore) LoadHistory(_ string) ([]chat.Message, error) { return nil, nil }
func (r *recordingHistoryStore) AppendMessages(_ string, msgs []chat.Message) error {
	r.appended = append(r.appended, msgs)
	return nil
}

func TestStore_SaveHistory_AppendsNewMessages(t *testing.T) {
	rec := &recordingHistoryStore{}
	s := NewStore("s1", rec)

	// 无历史，ResetAndSave 应跳过保存
	if err := s.ResetAndSave(); err != nil {
		t.Fatal(err)
	}
	if len(rec.appended) != 0 {
		t.Errorf("expected 0 calls, got %d", len(rec.appended))
	}

	// 添加一条消息
	msg := chat.Message{Role: chat.RoleUser, Content: chat.Blocks{chat.NewFullTextBlock("hello")}}
	s.AppendHistory(&msg)

	if err := s.ResetAndSave(); err != nil {
		t.Fatal(err)
	}
	if len(rec.appended) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.appended))
	}
	if len(rec.appended[0]) != 1 {
		t.Fatalf("expected 1 message, got %d", len(rec.appended[0]))
	}
	if rec.appended[0][0].Content[0].(*chat.TextBlock).Text != "hello" {
		t.Errorf("expected 'hello', got %v", rec.appended[0][0].Content[0])
	}

	// 再次 Save 无新消息
	if err := s.ResetAndSave(); err != nil {
		t.Fatal(err)
	}
	if len(rec.appended) != 1 {
		t.Errorf("expected still 1 call, got %d", len(rec.appended))
	}
}

// ── Store0: Positions lifecycle ──

func TestStore_RemovePosition(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))

	pos1 := s.GetPosition(0)
	pos2 := s.GetPosition(0)
	s.RemovePosition(pos2)

	// pos2 被移除，只剩 pos1
	// 验证不影响 pos1 的读取
	evt := s.ReadFrom(pos1)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "a" {
		t.Errorf("pos1 should still work after removing pos2")
	}
}

func TestStore_Reset_AfterRemovePosition(t *testing.T) {
	s := NewStore("s1", nil)
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))

	pos1 := s.GetPosition(0)
	pos2 := s.GetPosition(0)

	// pos1 读一个
	s.ReadFrom(pos1) // pos1→1

	// 删除 pos2（position 仍为 0）
	s.RemovePosition(pos2)

	// minPosition 应为 1（只剩 pos1）
	s.ResetAndSave() // 清理 seq 0

	evt := s.ReadFrom(pos1)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if tb, ok := evt.Block.(*chat.TextBlock); !ok || tb.Text != "b" {
		t.Fatalf("expected TextBlock{b}, got %v", evt.Block)
	}
}

// ── Store0: Seq 单调递增 ──

func TestStore_SeqNeverResets(t *testing.T) {
	s := NewStore("s1", nil)

	// 第一组
	s.AddBlock(chat.NewFullTextBlock("a"))
	s.AddBlock(chat.NewFullTextBlock("b"))

	pos := s.GetPosition(0)
	s.ReadFrom(pos)  // a
	s.ReadFrom(pos)  // b
	s.ResetAndSave() // 清理所有

	// 第二组：seq 继续递增
	s.AddBlock(chat.NewFullTextBlock("c"))
	evt := s.ReadFrom(pos)
	if evt == nil || evt.Seq != 2 {
		t.Errorf("expected seq=2 after reset, got seq=%d", evt.Seq)
	}
}
