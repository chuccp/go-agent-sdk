package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/chat"
)

// fastRetry 测试用的重试方针：次数照给，等待压到毫秒级——
// 别让"重试到上限"那种用例真等 3 秒 × 轮数。
func fastRetry(times int) retryPolicy {
	return retryPolicy{times: times, baseDelay: time.Millisecond, maxDelay: 2 * time.Millisecond}
}

// fakeWriter 收块用：只数 delta，别的拷都不拷。
type fakeWriter struct{ deltas []string }

func (w *fakeWriter) MessageStart(*chat.Usage)               {}
func (w *fakeWriter) MessageDelta(*chat.Usage)               {}
func (w *fakeWriter) BlockStart(chat.UseDeltaBlock)          {}
func (w *fakeWriter) BlockTextStart()                        {}
func (w *fakeWriter) BlockErrorTextStart()                   {}
func (w *fakeWriter) BlockThinkingStart()                    {}
func (w *fakeWriter) BlockToolUseStart(string, string)       {}
func (w *fakeWriter) BlockServerToolUseStart(string, string) {}
func (w *fakeWriter) Block(chat.Block)                       {}
func (w *fakeWriter) FullText(string)                        {}
func (w *fakeWriter) ErrorText(error)                        {}
func (w *fakeWriter) BlockDelta(content string)              { w.deltas = append(w.deltas, content) }
func (w *fakeWriter) BlockStop()                             {}
func (w *fakeWriter) StopReason(chat.StopReason)             {}

// 一段正常的 SSE（够触发 message_start + 一个 text delta）。
const goodSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// 不是 SSE 的响应体（照抄 deepseek 偶发回来的那几字节开头）。
var garbageBody = []byte{0x6c, 0x51, 0xcb, 0x0d, 0xff, 0xfe, 0x00, 0x01}

// TestChatWithStreamRetriesInvalidStream 「响应体不是 SSE 流」时要在 **HTTP 层**
// 把同一个请求重发（2026-10-12 deepseek 偶发回没声明编码的压缩体，见 chat.ErrInvalidStream）：
// 第一次回垃圾、第二次回正常——要求整体成功、且确实请求了两次。
func TestChatWithStreamRetriesInvalidStream(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&calls, 1) == 1 {
			_, _ = w.Write(garbageBody)
			return
		}
		_, _ = io.WriteString(w, goodSSE)
	}))
	defer srv.Close()

	svc := NewService("test", srv.URL, "test-key", "test-model")
	svc.retry = fastRetry(3)
	writer := &fakeWriter{}
	if err := svc.ChatWithStream(context.Background(), chat.NewMessages(chat.DefaultConfig(), nil), writer); err != nil {
		t.Fatalf("重试后应该成功，实际报错: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("请求次数 = %d，期望 2（先坏后好各一次）", got)
	}
	if len(writer.deltas) == 0 {
		t.Error("正常流的内容块没写进 writer")
	}
}

// TestChatWithStreamGivesUpAfterRetries 一直坏就重试到上限、返回 chat.ErrInvalidStream——
// **不许静默成功**（静默成功就是以前那个"空回合"的病根）。
func TestChatWithStreamGivesUpAfterRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(garbageBody)
	}))
	defer srv.Close()

	svc := NewService("test", srv.URL, "test-key", "test-model")
	const tries = 3
	svc.retry = fastRetry(tries)
	err := svc.ChatWithStream(context.Background(), chat.NewMessages(chat.DefaultConfig(), nil), &fakeWriter{})
	if err == nil {
		t.Fatal("一直坏必须报错，不能静默成功")
	}
	if !errors.Is(err, chat.ErrInvalidStream) {
		t.Errorf("错误应能 errors.Is 到 chat.ErrInvalidStream，实际: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != tries+1 {
		t.Errorf("请求次数 = %d，期望 %d（首次 + 重试 %d 次）", got, tries+1, tries)
	}
}

// TestChatWithStreamShortBody 空响应（0 字节）同样算"不是 SSE"，别当成正常空流。
func TestChatWithStreamShortBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	svc := NewService("test", srv.URL, "test-key", "test-model")
	svc.retry = fastRetry(1)
	err := svc.ChatWithStream(context.Background(), chat.NewMessages(chat.DefaultConfig(), nil), &fakeWriter{})
	if !errors.Is(err, chat.ErrInvalidStream) {
		t.Errorf("空响应应报 chat.ErrInvalidStream，实际: %v", err)
	}
}

// ── 2026-10-15 加：上游"服务端错误"也要在 HTTP 层重发（用户报的"deepseek 暂时不可用"）──

// TestChatWithStreamRetriesServerError 先回 503（上游过载/网关抽风），再回正常流——
// 要求整体成功、确实重发过。
func TestChatWithStreamRetriesServerError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"temporarily unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, goodSSE)
	}))
	defer srv.Close()

	svc := NewService("test", srv.URL, "test-key", "test-model")
	svc.retry = fastRetry(3)
	writer := &fakeWriter{}
	if err := svc.ChatWithStream(context.Background(), chat.NewMessages(chat.DefaultConfig(), nil), writer); err != nil {
		t.Fatalf("503 之后该重发并成功，实际报错: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("请求次数 = %d，期望 2", got)
	}
	if len(writer.deltas) == 0 {
		t.Error("正常流的内容块没写进 writer")
	}
}

// TestChatWithStreamGivesUpOnServerError 一直 503 → 重试到上限、**如实报错**（错误里带状态码和响应体）。
func TestChatWithStreamGivesUpOnServerError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"temporarily unavailable"}}`)
	}))
	defer srv.Close()

	svc := NewService("test", srv.URL, "test-key", "test-model")
	const tries = 3
	svc.retry = fastRetry(tries)
	err := svc.ChatWithStream(context.Background(), chat.NewMessages(chat.DefaultConfig(), nil), &fakeWriter{})
	if err == nil {
		t.Fatal("一直 503 必须报错")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("错误里要带状态码，实际: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != tries+1 {
		t.Errorf("请求次数 = %d，期望 %d（首次 + 重试 %d 次）", got, tries+1, tries)
	}
}

// TestChatWithStreamDoesNotRetryClientError 400 是请求本身的问题，**重发只是白等**——只准打一次。
func TestChatWithStreamDoesNotRetryClientError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"max_tokens: too large"}}`)
	}))
	defer srv.Close()

	svc := NewService("test", srv.URL, "test-key", "test-model")
	svc.retry = fastRetry(3)
	err := svc.ChatWithStream(context.Background(), chat.NewMessages(chat.DefaultConfig(), nil), &fakeWriter{})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("400 该原样报错，实际: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("请求次数 = %d，期望 1（400 不重发）", got)
	}
}

// TestRetryDelay 重发与否的判据：只有"一个块都没写过"的那几类可以重发；
// 上限到了就停；Retry-After 听服务端的但封顶一分钟。
func TestRetryDelay(t *testing.T) {
	svc := NewService("test", "http://127.0.0.1:1", "k", "m")
	svc.retry = retryPolicy{times: 3, baseDelay: time.Second, maxDelay: time.Second}

	// 普通错误（读到一半出的事）不重发
	if _, ok := svc.retryDelay(errors.New("SSE stream read error: unexpected EOF"), 0); ok {
		t.Error("读到一半出的事不该重发（会出双份正文）")
	}
	// ctx 取消了不重发
	if _, ok := svc.retryDelay(&retryableError{err: context.Canceled}, 0); ok {
		t.Error("ctx 取消不该重发")
	}
	if _, ok := svc.retryDelay(&retryableError{err: context.DeadlineExceeded}, 0); ok {
		t.Error("ctx 超时不该重发")
	}
	// 可重发的：默认退避就是 1 秒（base == max 时不翻倍）
	if delay, ok := svc.retryDelay(&retryableError{err: errors.New("503")}, 0); !ok || delay != time.Second {
		t.Errorf("第一次该等 1 秒，实际 %v ok=%v", delay, ok)
	}
	if delay, ok := svc.retryDelay(&retryableError{err: errors.New("503")}, 2); !ok || delay != time.Second {
		t.Errorf("固定间隔：最后一次也该等 1 秒，实际 %v ok=%v", delay, ok)
	}
	// 次数用光
	if _, ok := svc.retryDelay(&retryableError{err: errors.New("503")}, 3); ok {
		t.Error("重试次数用光后不该再重发")
	}
	// Retry-After：听它的，但封顶一分钟
	if delay, ok := svc.retryDelay(&retryableError{err: errors.New("429"), retryAfter: 5 * time.Second}, 0); !ok || delay != 5*time.Second {
		t.Errorf("Retry-After 该被采纳，实际 %v ok=%v", delay, ok)
	}
	if delay, ok := svc.retryDelay(&retryableError{err: errors.New("429"), retryAfter: 10 * time.Minute}, 0); !ok || delay != maxRetryAfter {
		t.Errorf("Retry-After 该封顶在 %v，实际 %v ok=%v", maxRetryAfter, delay, ok)
	}
}

// TestRetryPolicyFromEnv 三个旋钮从环境变量读；读不出来（没设/不是数）退回默认。
func TestRetryPolicyFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_RETRY_TIMES", "5")
	t.Setenv("ANTHROPIC_RETRY_BASE_DELAY_MS", "200")
	t.Setenv("ANTHROPIC_RETRY_MAX_DELAY_MS", "4000")
	policy := retryPolicyFromEnv()
	if policy.times != 5 || policy.baseDelay != 200*time.Millisecond || policy.maxDelay != 4*time.Second {
		t.Errorf("读环境变量 = %+v", policy)
	}

	t.Setenv("ANTHROPIC_RETRY_TIMES", "abc")
	t.Setenv("ANTHROPIC_RETRY_BASE_DELAY_MS", "")
	t.Setenv("ANTHROPIC_RETRY_MAX_DELAY_MS", "")
	policy = retryPolicyFromEnv()
	if policy.times != defaultRetryTimes || policy.baseDelay != defaultRetryBaseDelay || policy.maxDelay != defaultRetryMaxDelay {
		t.Errorf("读不出来该退回默认（每秒一次、三次），实际 %+v", policy)
	}
}
