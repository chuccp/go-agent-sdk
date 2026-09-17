package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// summaryService 假的 LLM Service：返回预设文本或错误，并记录收到的请求。
type summaryService struct {
	id       string
	reply    string
	err      error
	requests []*chat.Messages
}

func (s *summaryService) ID() string {
	if s.id == "" {
		return "summary-fake"
	}
	return s.id
}

func (s *summaryService) ChatWithStream(_ context.Context, m *chat.Messages, w chat.BlockWriter) error {
	s.requests = append(s.requests, m)
	if s.err != nil {
		return s.err
	}
	w.BlockTextStart()
	w.BlockDelta(s.reply)
	w.StopReason(chat.StopReasonEndTurn)
	return nil
}

// fakeContext 只实现摘要压缩器用到的那部分 Context（其余方法用不上，给零值）。
type fakeContext struct {
	context.Context
	chat *chat.Chat
}

func (f *fakeContext) SessionId() string                       { return "test-session" }
func (f *fakeContext) GetChat() *chat.Chat                     { return f.chat }
func (f *fakeContext) Store() *Store                           { return nil }
func (f *fakeContext) Ctx() context.Context                    { return f.Context }
func (f *fakeContext) ChatWithStream(msgs *chat.Messages, w chat.BlockWriter) error {
	return f.GetChat().ChatWithStream(f.Ctx(), msgs, w)
}
func (f *fakeContext) SendBlock(chat.Block) uint64             { return 0 }
func (f *fakeContext) SendSignalBlock(chat.Block) uint64       { return 0 }
func (f *fakeContext) GetTransferStart() uint64                { return 0 }
func (f *fakeContext) AppendAssistantMessage(*chat.BlockGroup) {}
func (f *fakeContext) AppendUserMessage(*chat.BlockGroup)      {}
func (f *fakeContext) AppendHistory(*chat.Message)             {}
func (f *fakeContext) GetConfig() *chat.Config                 { return nil }

func newSummaryContext(service chat.Service) *fakeContext {
	c := chat.NewChat()
	c.Register(service)
	return &fakeContext{Context: context.Background(), chat: c}
}

// blocksText 把块里的文本拼起来，便于断言提示词与摘要内容。
func blocksText(blocks chat.Blocks) string {
	var sb strings.Builder
	for _, b := range blocks {
		switch v := b.(type) {
		case *chat.TextBlock:
			sb.WriteString(v.Text)
		case *chat.UserBlock:
			sb.WriteString(blocksText(v.Content))
		}
	}
	return sb.String()
}

// droppedHistory 构造 3 条带正文的被切历史。
func droppedHistory() []*chat.Message {
	msgs := make([]*chat.Message, 0, 3)
	for i := 0; i < 3; i++ {
		msgs = append(msgs, &chat.Message{
			Start:   uint64(i),
			Offset:  1,
			Role:    chat.RoleUser,
			Content: chat.Blocks{chat.NewFullTextBlock("历史正文" + string(rune('0'+i)))},
		})
	}
	return msgs
}

// TestSummaryCompressor_WrapsModelText 验证正常路径：摘要在分界点上、带前缀，
// 且提示词里带上了整段被切历史。
func TestSummaryCompressor_WrapsModelText(t *testing.T) {
	svc := &summaryService{reply: "用户在问压缩，结论是保留后一半。"}
	ctx := newSummaryContext(svc)
	dropped := droppedHistory()

	msg := (&SummaryCompressor{}).Compress(ctx, dropped)
	if msg == nil {
		t.Fatal("应返回摘要消息")
	}
	last := dropped[len(dropped)-1]
	if want := last.Start + last.Offset; msg.Start != want {
		t.Errorf("分界点 = %d, 期望 %d（被切段末尾）", msg.Start, want)
	}
	if msg.Role != chat.RoleUser {
		t.Errorf("摘要消息 role = %q, 期望 %q", msg.Role, chat.RoleUser)
	}
	if text := blocksText(msg.Content); !strings.Contains(text, "用户在问压缩") || !strings.Contains(text, summaryPrefix) {
		t.Errorf("摘要消息内容 = %q, 期望带前缀 + 模型输出", text)
	}

	if len(svc.requests) != 1 {
		t.Fatalf("模型调用次数 = %d, 期望 1", len(svc.requests))
	}
	prompt := blocksText(svc.requests[0].Messages()[0].Content)
	for _, want := range []string{"历史正文0", "历史正文1", "历史正文2", "压缩成一段简洁的摘要"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("提示词里缺少 %q:\n%s", want, prompt)
		}
	}
}

// TestSummaryCompressor_DefaultsAndOverrides 验证默认 ServiceID / MaxTokens 与覆盖。
func TestSummaryCompressor_DefaultsAndOverrides(t *testing.T) {
	svc := &summaryService{reply: "摘要"}
	ctx := newSummaryContext(svc)

	(&SummaryCompressor{}).Compress(ctx, droppedHistory())
	if got := svc.requests[0].Config().GetMaxTokens(); got != defaultSummaryMaxTokens {
		t.Errorf("默认 MaxTokens = %d, 期望 %d", got, defaultSummaryMaxTokens)
	}
	if got := svc.requests[0].Config().GetID(); got != "" {
		t.Errorf("默认不该指定 ServiceID，实际 %q", got)
	}

	svc.id = "cheap-model"
	(&SummaryCompressor{ServiceID: "cheap-model", MaxTokens: 256, Prompt: "自定义提示词"}).Compress(newSummaryContext(svc), droppedHistory())
	if len(svc.requests) != 2 {
		t.Fatalf("模型调用次数 = %d, 期望 2", len(svc.requests))
	}
	second := svc.requests[1]
	if got := second.Config().GetID(); got != "cheap-model" {
		t.Errorf("ServiceID = %q, 期望 cheap-model", got)
	}
	if got := second.Config().GetMaxTokens(); got != 256 {
		t.Errorf("MaxTokens = %d, 期望 256", got)
	}
	if prompt := blocksText(second.Messages()[0].Content); !strings.Contains(prompt, "自定义提示词") {
		t.Errorf("自定义提示词没进请求:\n%s", prompt)
	}
}

// TestSummaryCompressor_FallsBackOnFailure 验证模型报错或没吐出文本时返回 nil：
// 由 manager 记分界点、退化成强行切割。
func TestSummaryCompressor_FallsBackOnFailure(t *testing.T) {
	failing := &summaryService{err: errors.New("boom")}
	if got := (&SummaryCompressor{}).Compress(newSummaryContext(failing), droppedHistory()); got != nil {
		t.Errorf("模型报错应返回 nil，实际 %+v", got)
	}

	empty := &summaryService{reply: "   "}
	if got := (&SummaryCompressor{}).Compress(newSummaryContext(empty), droppedHistory()); got != nil {
		t.Errorf("模型没吐出文本应返回 nil，实际 %+v", got)
	}
}

// TestSummaryCompressor_NothingToCompress 验证没有历史可压时不调模型。
func TestSummaryCompressor_NothingToCompress(t *testing.T) {
	svc := &summaryService{reply: "摘要"}
	if got := (&SummaryCompressor{}).Compress(newSummaryContext(svc), nil); got != nil {
		t.Errorf("空历史应返回 nil，实际 %+v", got)
	}
	if len(svc.requests) != 0 {
		t.Errorf("空历史不该调模型，实际 %d 次", len(svc.requests))
	}
}

// TestSummaryCompressor_HistoryText 验证进摘要材料的内容：文本、工具调用、工具结果
// 都在，包在 UserBlock 里的照样本样展开，thinking 之类不进。
func TestSummaryCompressor_HistoryText(t *testing.T) {
	msgs := []*chat.Message{
		{
			Start: 0, Offset: 1, Role: chat.RoleUser,
			Content: chat.Blocks{chat.NewUserBlock(1, chat.Blocks{chat.NewFullTextBlock("用户问题")}, chat.Consume)},
		},
		{
			Start: 1, Offset: 2, Role: chat.RoleAssistant,
			Content: chat.Blocks{
				chat.NewToolUseBlock("tu_1", "command"),
				chat.NewToolResultBlock("tu_1", chat.Blocks{chat.NewFullTextBlock("命令输出")}),
			},
		},
	}

	text := historyText(msgs)
	for _, want := range []string{"user: 用户问题", "assistant: [调用工具 command]", "[工具结果] 命令输出"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要材料里缺少 %q:\n%s", want, text)
		}
	}
}
