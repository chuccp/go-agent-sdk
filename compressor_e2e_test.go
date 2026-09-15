package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// seededHistoryStore 提供预置历史，并记录 SaveSummary 写入的分界点。
type seededHistoryStore struct {
	messages []*chat.Message
	summary  *chat.Message
}

func (s *seededHistoryStore) LoadAfter(_ string, since uint64, limit int) ([]*chat.Message, error) {
	var out []*chat.Message
	for _, m := range s.messages {
		if m.Start+m.Offset > since {
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *seededHistoryStore) Append(_ string, messages []*chat.Message) error {
	s.messages = append(s.messages, messages...)
	return nil
}

func (s *seededHistoryStore) LoadSummary(string) (*chat.Message, error) { return s.summary, nil }

func (s *seededHistoryStore) SaveSummary(_ string, summary *chat.Message) error {
	s.summary = summary
	return nil
}

// capturingProvider 每轮返回固定文本，抄一份请求消息，并按上下文条数上报用量
// （每条 600 token）——水位闸门读的就是这份用量。
// summaryMarker 非空时，提示词里带这个标记的请求按摘要答复，单独记进 summaries。
type capturingProvider struct {
	requests [][]*chat.Message

	summaryMarker string
	summaryReply  string
	summaries     [][]*chat.Message
}

func (p *capturingProvider) ID() string { return "compress-fake" }

func (p *capturingProvider) ChatWithStream(_ context.Context, m *chat.Messages, w chat.BlockWriter) error {
	messages := append([]*chat.Message(nil), m.Messages()...)
	if p.summaryMarker != "" && strings.Contains(strings.Join(messageTexts(messages), "\n"), p.summaryMarker) {
		p.summaries = append(p.summaries, messages)
		w.BlockTextStart()
		w.BlockDelta(p.summaryReply)
		w.StopReason(chat.StopReasonEndTurn)
		return nil
	}
	p.requests = append(p.requests, messages)
	w.MessageStart(&chat.Usage{InputTokens: len(messages) * 600, OutputTokens: 100})
	w.BlockTextStart()
	w.BlockDelta("收到")
	w.StopReason(chat.StopReasonEndTurn)
	return nil
}

// messageTexts 把每条消息里的文本块拼成一个字符串，便于断言上下文内容。
func messageTexts(msgs []*chat.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var sb strings.Builder
		for _, b := range m.Content {
			if tb, ok := b.(*chat.TextBlock); ok {
				sb.WriteString(tb.Text)
			}
		}
		out = append(out, sb.String())
	}
	return out
}

// seededHistory 构造 旧消息1..9，Start 逐条递增。
func seededHistory() []*chat.Message {
	history := make([]*chat.Message, 0, 9)
	for i := 1; i <= 9; i++ {
		history = append(history, &chat.Message{
			Start:   uint64(i),
			Offset:  1,
			Role:    chat.RoleUser,
			Content: chat.Blocks{chat.NewFullTextBlock(fmt.Sprintf("旧消息%d", i))},
		})
	}
	return history
}

// newCompressSession 起一个水位 5000、超水位后保留一半的会话：
// 每条按 600 token 计，10 条历史 + 本轮（6100）超水位，压完剩 7 条（4300）又落回水位下。
func newCompressSession(t *testing.T, sessionID string, store *seededHistoryStore, compressor agent.Compressor) (*capturingProvider, *agent.Session) {
	t.Helper()
	provider := &capturingProvider{}
	config := agent.NewConfig()
	config.RegisterChat(provider)
	config.MessageStore(store)
	config.Compressor(compressor, agent.WithMaxContextLength(5_000), agent.WithKeepRatio(0.5))

	manager := config.CreateServer(context.Background())
	return provider, manager.GetOrCreateSession(sessionID)
}

// runRound 发一轮消息并等到本轮结束。
func runRound(t *testing.T, provider *capturingProvider, session *agent.Session, client *agent.Client, round int) {
	t.Helper()
	session.WriteText(fmt.Sprintf("第%d轮问题", round))
	waitForDone(t, client, fmt.Sprintf("compress-round-%d", round))
}

// TestCompressor_CutsHistoryBeforeRequest 端到端验证压缩接进 buildRequest：
// 预置 9 条历史，水位 10k、超水位保留一半。
// 第 1 轮还没上报过用量（水位判断无从下手）→ 不切；第 2 轮用量已超水位 → 切一刀，
// 发给模型的上下文只剩 [占位消息 + 旧消息7~9 + 上一轮 + 本轮]，分界点落盘。
func TestCompressor_CutsHistoryBeforeRequest(t *testing.T) {
	store := &seededHistoryStore{messages: seededHistory()}
	provider, session := newCompressSession(t, "compress-session", store, &agent.CutCompressor{})
	client := session.Client(context.Background(), 0)

	runRound(t, provider, session, client, 1)
	runRound(t, provider, session, client, 2)

	if len(provider.requests) != 2 {
		t.Fatalf("请求次数 = %d, 期望 2", len(provider.requests))
	}

	// 第 1 轮：没有用量，整段历史原样进上下文
	first := strings.Join(messageTexts(provider.requests[0]), "\n")
	for i := 1; i <= 9; i++ {
		if !strings.Contains(first, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("第 1 轮不该切割，旧消息%d 却不在上下文里:\n%s", i, first)
		}
	}

	// 第 2 轮：用量超水位 → 砍掉前 6 条
	second := messageTexts(provider.requests[1])
	joined := strings.Join(second, "\n")
	for i := 1; i <= 6; i++ {
		if strings.Contains(joined, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("旧消息%d 应被切掉，实际进了上下文:\n%s", i, joined)
		}
	}
	for i := 7; i <= 9; i++ {
		if !strings.Contains(joined, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("旧消息%d 应保留，实际上下文:\n%s", i, joined)
		}
	}
	if len(second) == 0 || !strings.Contains(second[0], "已超出上下文上限") {
		t.Errorf("占位消息应排在上下文最前，实际第一条 = %q", firstOf(second))
	}
	if last := second[len(second)-1]; last != "第2轮问题" {
		t.Errorf("本轮用户消息应在最后，实际最后一条 = %q", last)
	}

	if store.summary == nil {
		t.Fatal("分界点未落盘")
	}
	if want := provider.requests[1][1].Start; store.summary.Start != want {
		t.Errorf("落盘分界点 = %d, 期望 %d（保留段第一条）", store.summary.Start, want)
	}
}

// TestCompressor_OnlyCutsOverWatermark 端到端验证水位闸门：切完一刀用量回落到水位下，
// 第 3 轮就不再继续切。修复前每轮都会再切一刀，第 2 轮刚保留下来的 旧消息7~9
// 到第 3 轮就被啃掉了。
func TestCompressor_OnlyCutsOverWatermark(t *testing.T) {
	store := &seededHistoryStore{messages: seededHistory()}
	provider, session := newCompressSession(t, "compress-session-gate", store, &agent.CutCompressor{})
	client := session.Client(context.Background(), 0)

	for round := 1; round <= 3; round++ {
		runRound(t, provider, session, client, round)
	}

	if len(provider.requests) != 3 {
		t.Fatalf("请求次数 = %d, 期望 3", len(provider.requests))
	}
	sizes := []int{
		len(provider.requests[0]),
		len(provider.requests[1]),
		len(provider.requests[2]),
	}
	t.Logf("每轮上下文条数 = %d/%d/%d", sizes[0], sizes[1], sizes[2])

	// 第 3 轮还在水位以下 → 不该再切，第 2 轮保留下来的三条都还在
	third := strings.Join(messageTexts(provider.requests[2]), "\n")
	for i := 7; i <= 9; i++ {
		if !strings.Contains(third, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("第 3 轮用量已回落，不该继续切割，旧消息%d 却没了:\n%s", i, third)
		}
	}
	if !strings.Contains(third, "已超出上下文上限") {
		t.Errorf("第 3 轮上下文里应还留着分界消息占位:\n%s", third)
	}
}

// TestSummaryCompressor_SummarizesDroppedHistory 端到端：压缩时多调一次大模型，
// 被切掉的那几条进的是摘要提示词，下一轮上下文里是摘要消息而不是占位文本。
func TestSummaryCompressor_SummarizesDroppedHistory(t *testing.T) {
	const marker = "SUMMARY-CALL"
	store := &seededHistoryStore{messages: seededHistory()}
	provider, session := newCompressSession(t, "compress-session-summary", store,
		&agent.SummaryCompressor{Prompt: marker})
	provider.summaryMarker = marker
	provider.summaryReply = "用户在看压缩逻辑，结论是保留后一半。"
	client := session.Client(context.Background(), 0)

	runRound(t, provider, session, client, 1)
	runRound(t, provider, session, client, 2)

	if len(provider.summaries) != 1 {
		t.Fatalf("摘要调用次数 = %d, 期望 1", len(provider.summaries))
	}
	material := strings.Join(messageTexts(provider.summaries[0]), "\n")
	for i := 1; i <= 6; i++ {
		if !strings.Contains(material, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("旧消息%d 应进摘要材料:\n%s", i, material)
		}
	}
	for i := 7; i <= 9; i++ {
		if strings.Contains(material, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("旧消息%d 在保留段里，不该进摘要材料:\n%s", i, material)
		}
	}

	second := strings.Join(messageTexts(provider.requests[1]), "\n")
	if !strings.Contains(second, "[历史摘要] 用户在看压缩逻辑") {
		t.Errorf("下一轮上下文里应是摘要消息，实际:\n%s", second)
	}
	if strings.Contains(second, "已超出上下文上限") {
		t.Errorf("摘要方案不该出现强行切割的占位文本:\n%s", second)
	}
	for i := 1; i <= 6; i++ {
		if strings.Contains(second, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("旧消息%d 已被摘要取代，不该再进上下文:\n%s", i, second)
		}
	}
	for i := 7; i <= 9; i++ {
		if !strings.Contains(second, fmt.Sprintf("旧消息%d", i)) {
			t.Errorf("旧消息%d 应保留:\n%s", i, second)
		}
	}
}

func firstOf(texts []string) string {
	if len(texts) == 0 {
		return ""
	}
	return texts[0]
}
