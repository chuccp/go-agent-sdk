package agent

import (
	"errors"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// alternating 构造 n 条消息：Start 逐条递增、Offset=1，user/assistant 交替。
func alternating(n int) []*chat.Message {
	msgs := make([]*chat.Message, 0, n)
	for i := 0; i < n; i++ {
		role := chat.RoleUser
		if i%2 == 1 {
			role = chat.RoleAssistant
		}
		msgs = append(msgs, &chat.Message{
			Start:   uint64(i),
			Offset:  1,
			Role:    role,
			Content: chat.Blocks{chat.NewFullTextBlock("m")},
		})
	}
	return msgs
}

// toolResultMessage 构造一条 user 的 tool_result 消息：它必须以配对的 tool_use 为前提，
// 切在它前面会拆散这一对。
func toolResultMessage(start uint64, wrapInUserBlock bool) *chat.Message {
	tr := chat.NewToolResultBlock("call_1", chat.Blocks{chat.NewFullTextBlock("ok")})
	content := chat.Blocks{tr}
	if wrapInUserBlock {
		content = chat.Blocks{chat.NewUserBlock(1, chat.Blocks{tr}, chat.Consume)}
	}
	return &chat.Message{Start: start, Offset: 1, Role: chat.RoleUser, Content: content}
}

// recordingCompressor 记录 Compress 收到的入参，返回预设消息（预设为 nil 即「不压缩」）。
type recordingCompressor struct {
	calls int
	arg   []*chat.Message
	ret   *chat.Message
}

func (r *recordingCompressor) Compress(_ Context, messages []*chat.Message) *chat.Message {
	r.calls++
	r.arg = messages
	return r.ret
}

func newManager(keepRatio float64, compressor Compressor) *CompressorManager {
	return newManagerWith(keepRatio, compressor, nil)
}

// newManagerWith 构造水位 5000 的 manager，并把用量报到 6000（超水位）——
// 切割类用例不必每处都自己过水位闸门。
func newManagerWith(keepRatio float64, compressor Compressor, summary Summary) *CompressorManager {
	m := NewCompressorManager("test-session", compressor, &CompressorOptions{
		maxContextLength: 5000,
		keepRatio:        keepRatio,
	}, summary)
	m.UpdateUsage(&chat.Usage{InputTokens: 5000, OutputTokens: 1000})
	return m
}

// fakeSummary 记录落盘的分界点，err 非 nil 时模拟落盘失败。
type fakeSummary struct {
	saved []*chat.Message
	err   error
}

func (f *fakeSummary) LoadSummary(string) (*chat.Message, error) { return nil, f.err }

func (f *fakeSummary) SaveSummary(_ string, summary *chat.Message) error {
	f.saved = append(f.saved, summary)
	return f.err
}

// TestCutCompressor_PlaceholderAtDroppedEnd 验证 CutCompressor 只做压缩、不做切割：
// 分界点取被切掉那段历史的末尾，也就是保留段的第一条。
func TestCutCompressor_PlaceholderAtDroppedEnd(t *testing.T) {
	dropped := alternating(50)

	msg := (&CutCompressor{}).Compress(nil, dropped)
	if msg == nil {
		t.Fatal("应返回占位消息")
	}
	last := dropped[len(dropped)-1]
	if want := last.Start + last.Offset; msg.Start != want {
		t.Errorf("分界点 = %d, 期望 %d（被切段的末尾）", msg.Start, want)
	}
	if msg.Role != chat.RoleUser {
		t.Errorf("占位消息 role = %q, 期望 %q", msg.Role, chat.RoleUser)
	}
	if len(msg.Content) == 0 {
		t.Error("占位消息内容为空")
	}
}

// TestCutCompressor_NothingToCompress 验证没有历史可压时返回 nil。
func TestCutCompressor_NothingToCompress(t *testing.T) {
	if got := (&CutCompressor{}).Compress(nil, nil); got != nil {
		t.Errorf("空切片应返回 nil，实际 %+v", got)
	}
}

// TestCompressorManager_CutsByRatioAndPrependsPlaceholder 验证切割由 manager 完成：
// 只保留末尾比例的消息，被切掉的那段才是交给压缩器的材料，压缩结果拼在保留段前面。
func TestCompressorManager_CutsByRatioAndPrependsPlaceholder(t *testing.T) {
	msgs := alternating(100)
	rec := &recordingCompressor{ret: &chat.Message{Start: 50, Role: chat.RoleUser}}

	got, cut := newManager(0.5, rec).compress(nil, msgs)

	if !cut {
		t.Error("发生了切割，第二个返回值应为 true")
	}
	if rec.calls != 1 {
		t.Fatalf("Compress 调用次数 = %d, 期望 1", rec.calls)
	}
	if len(rec.arg) != 50 {
		t.Errorf("交给压缩器的应是被切掉的那段，长度 = %d, 期望 50", len(rec.arg))
	}
	if len(got) != 51 {
		t.Fatalf("返回长度 = %d, 期望 51（占位 + 保留 50 条）", len(got))
	}
	if got[0] != rec.ret {
		t.Error("压缩结果应排在保留段前面")
	}
	if got[1].Start != msgs[50].Start {
		t.Errorf("保留段第一条 Start = %d, 期望 %d", got[1].Start, msgs[50].Start)
	}
}

// TestCompressorManager_OnlyCompressesOverWatermark 验证水位闸门：用量没超
// maxContextLength 时一条都不切、也不调压缩器；超了才切。
func TestCompressorManager_OnlyCompressesOverWatermark(t *testing.T) {
	msgs := alternating(10)
	rec := &recordingCompressor{}
	m := NewCompressorManager("test-session", rec, &CompressorOptions{
		maxContextLength: 5000,
		keepRatio:        0.5,
	}, nil)

	m.UpdateUsage(&chat.Usage{InputTokens: 2500, OutputTokens: 1}) // 2501，没超
	got, cut := m.compress(nil, msgs)
	if cut {
		t.Error("没超水位不该切割")
	}
	if rec.calls != 0 {
		t.Errorf("没超水位不该调压缩器，实际 %d 次", rec.calls)
	}
	if len(got) != len(msgs) || got[0] != msgs[0] {
		t.Errorf("没超水位应原样返回，实际 %d 条", len(got))
	}

	m.UpdateUsage(&chat.Usage{InputTokens: 5000, OutputTokens: 1}) // 5001，超了
	if _, cut := m.compress(nil, msgs); !cut {
		t.Error("超水位应切割")
	}
	if rec.calls != 1 {
		t.Errorf("超水位应调压缩器 1 次，实际 %d 次", rec.calls)
	}
}

// TestCompressorManager_KeepRatioFallbacks 验证比例缺省、按比例切割、以及 >=1 不切割。
func TestCompressorManager_KeepRatioFallbacks(t *testing.T) {
	// 没配比例按缺省 0.5 处理
	rec := &recordingCompressor{}
	newManager(0, rec).compress(nil, alternating(100))
	if len(rec.arg) != 50 {
		t.Errorf("缺省比例应切掉一半，实际切掉 %d 条", len(rec.arg))
	}

	// 0.25 只留后 25%
	rec = &recordingCompressor{}
	newManager(0.25, rec).compress(nil, alternating(100))
	if len(rec.arg) != 75 {
		t.Errorf("比例 0.25 应切掉 75 条，实际 %d 条", len(rec.arg))
	}

	// >=1 不切割：压缩器不该被调用，消息原样返回
	msgs := alternating(100)
	rec = &recordingCompressor{}
	got, cut := newManager(1, rec).compress(nil, msgs)
	if cut {
		t.Error("没切割，第二个返回值应为 false")
	}
	if rec.calls != 0 {
		t.Errorf("不切割时不应调用压缩器，实际调用 %d 次", rec.calls)
	}
	if len(got) != len(msgs) || got[0] != msgs[0] {
		t.Errorf("不切割时应原样返回，实际 %d 条", len(got))
	}
}

// TestCompressorManager_PushesCutPastToolResult 验证切点落在 tool_result 上时后移：
// 丢掉 tool_use 却留下 tool_result 会被 Anthropic 判 400。
func TestCompressorManager_PushesCutPastToolResult(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		msgs := alternating(10)
		msgs[5] = toolResultMessage(5, wrap)

		rec := &recordingCompressor{}
		got, cut := newManager(0.5, rec).compress(nil, msgs)

		if !cut {
			t.Errorf("wrap=%v: 发生了切割，第二个返回值应为 true", wrap)
		}
		if len(rec.arg) != 6 {
			t.Errorf("wrap=%v: 切点应后移到第 6 条，实际切掉 %d 条", wrap, len(rec.arg))
		}
		// 压缩器返回 nil，返回的只剩保留段 4 条
		if len(got) != 4 {
			t.Errorf("wrap=%v: 返回长度 = %d, 期望 4", wrap, len(got))
		}
	}
}

// TestCompressorManager_NoCutWhenTailIsAllToolResults 验证推到底也找不到安全切点时
// 本轮不动，而不是把上下文切空。
func TestCompressorManager_NoCutWhenTailIsAllToolResults(t *testing.T) {
	msgs := alternating(4)
	msgs[2] = toolResultMessage(2, false)
	msgs[3] = toolResultMessage(3, false)

	rec := &recordingCompressor{}
	got, cut := newManager(0.5, rec).compress(nil, msgs)

	if cut {
		t.Error("没有安全切点，第二个返回值应为 false")
	}
	if rec.calls != 0 {
		t.Errorf("没有可切的段时不应调用压缩器，实际 %d 次", rec.calls)
	}
	if len(got) != len(msgs) {
		t.Errorf("返回长度 = %d, 期望原样 %d 条", len(got), len(msgs))
	}
}

// TestCompressorManager_KeepsCutWhenCompressorReturnsNil 验证压缩器返回 nil 时切割
// 照常生效，只是没有东西替换被切掉的历史。
func TestCompressorManager_KeepsCutWhenCompressorReturnsNil(t *testing.T) {
	msgs := alternating(10)

	got, cut := newManager(0.5, &recordingCompressor{}).compress(nil, msgs)

	if !cut {
		t.Error("压缩器返回 nil 不影响切割，第二个返回值应为 true")
	}
	if len(got) != 5 {
		t.Fatalf("返回长度 = %d, 期望 5", len(got))
	}
	if got[0].Start != msgs[5].Start {
		t.Errorf("保留段第一条 Start = %d, 期望 %d", got[0].Start, msgs[5].Start)
	}
}

// TestCompressorManager_SavesSummaryOnCut 验证切割产生的分界消息会落盘，
// 并且顶掉内存缓存（loadSummary 之后直接返回它）。
func TestCompressorManager_SavesSummaryOnCut(t *testing.T) {
	msg := &chat.Message{Start: 5, Role: chat.RoleUser}
	summary := &fakeSummary{}
	m := newManagerWith(0.5, &recordingCompressor{ret: msg}, summary)

	got, cut := m.compress(nil, alternating(10))

	if !cut {
		t.Fatal("发生了切割，第二个返回值应为 true")
	}
	if len(got) != 6 || got[0] != msg { // 分界消息 + 保留的 5 条
		t.Fatalf("分界消息应排在保留段前面，实际 %d 条", len(got))
	}
	if len(summary.saved) != 1 || summary.saved[0] != msg {
		t.Fatalf("分界消息未落盘，实际 %d 条", len(summary.saved))
	}
	loaded, err := m.loadSummary()
	if err != nil {
		t.Fatalf("loadSummary 报错: %v", err)
	}
	if loaded != msg {
		t.Errorf("内存缓存未更新，loadSummary 返回 %+v", loaded)
	}
}

// TestCompressorManager_SavesBoundaryWhenCompressorReturnsNil 验证压缩器没产出内容时
// 仍然记下分界点（留空 content），但不会往返回的历史里塞消息。
func TestCompressorManager_SavesBoundaryWhenCompressorReturnsNil(t *testing.T) {
	summary := &fakeSummary{}
	m := newManagerWith(0.5, &recordingCompressor{}, summary)

	got, cut := m.compress(nil, alternating(10))

	if !cut {
		t.Fatal("发生了切割，第二个返回值应为 true")
	}
	if len(got) != 5 { // 只有保留段，没有占位消息
		t.Fatalf("返回长度 = %d, 期望 5", len(got))
	}
	if len(summary.saved) != 1 {
		t.Fatalf("分界点应落盘一次，实际 %d 次", len(summary.saved))
	}
	if want := got[0].Start; summary.saved[0].Start != want {
		t.Errorf("落盘的分界点 = %d, 期望 %d（保留段第一条）", summary.saved[0].Start, want)
	}
	if loaded, _ := m.loadSummary(); loaded.Start != got[0].Start {
		t.Errorf("内存缓存未更新，loadSummary 返回 Start=%d", loaded.Start)
	}
}

// TestCompressorManager_SaveSummaryIgnoresNil 验证 saveSummary(nil) 不会把已记下的
// 分界点清掉（清掉等于回到「未压缩」，旧历史下次加载会整段回来）。
func TestCompressorManager_SaveSummaryIgnoresNil(t *testing.T) {
	boundary := &chat.Message{Start: 5, Role: chat.RoleUser}
	summary := &fakeSummary{}
	m := newManagerWith(0.5, &recordingCompressor{ret: boundary}, summary)
	m.compress(nil, alternating(10))

	m.saveSummary(nil)

	if len(summary.saved) != 1 {
		t.Errorf("nil 不该触发落盘，实际落盘 %d 次", len(summary.saved))
	}
	if loaded, _ := m.loadSummary(); loaded != boundary {
		t.Errorf("nil 不该清掉内存分界点，loadSummary 返回 %+v", loaded)
	}
}

// TestCompressorManager_KeepsCutWhenSaveFails 验证落盘失败只记日志，切割照常生效。
func TestCompressorManager_KeepsCutWhenSaveFails(t *testing.T) {
	msg := &chat.Message{Start: 5, Role: chat.RoleUser}
	summary := &fakeSummary{err: errors.New("db down")}
	m := newManagerWith(0.5, &recordingCompressor{ret: msg}, summary)

	got, cut := m.compress(nil, alternating(10))

	if !cut {
		t.Error("落盘失败不应回退切割")
	}
	if len(got) != 6 || got[0] != msg { // 分界消息 + 保留的 5 条
		t.Errorf("落盘失败不应影响返回结果，实际 %d 条", len(got))
	}
	if loaded, _ := m.loadSummary(); loaded != msg {
		t.Errorf("落盘失败也应顶掉内存缓存，loadSummary 返回 %+v", loaded)
	}
}

// TestCompressorManager_WithCutCompressor 端到端：切割 + 占位消息接起来后，
// 分界点正好落在保留段第一条上。
func TestCompressorManager_WithCutCompressor(t *testing.T) {
	msgs := alternating(100)

	got, cut := newManager(0.5, &CutCompressor{}).compress(nil, msgs)

	if !cut {
		t.Error("发生了切割，第二个返回值应为 true")
	}
	if len(got) != 51 {
		t.Fatalf("返回长度 = %d, 期望 51", len(got))
	}
	if got[0].Start != msgs[50].Start || got[1].Start != msgs[50].Start {
		t.Errorf("分界点 = %d / 保留段首条 = %d, 都应为 %d", got[0].Start, got[1].Start, msgs[50].Start)
	}
}
