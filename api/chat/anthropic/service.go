package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/log"
	"resty.dev/v3"
)

const (
	AnthropicVersion = "2023-06-01"
)

type Service struct {
	chat.Service
	config      *chat.Config
	id          string
	restyClient *resty.Client
	baseUrl     string
	apiKey      string
	model       string
	// retry 见 retryPolicy：测试把它压成毫秒级（别在"重试到上限"的用例里真等十几秒）
	retry retryPolicy
}

func NewService(id, baseUrl, apiKey string, model string, config ...*chat.Config) *Service {
	defaultConfig := chat.Combine(config...)
	defaultConfig.Set(chat.BaseURLConfigKey, baseUrl)
	defaultConfig.Set(chat.APIKEYConfigKey, apiKey)
	defaultConfig.Set(chat.ModelConfigKey, model)
	return &Service{
		config:      defaultConfig,
		id:          id,
		baseUrl:     baseUrl,
		apiKey:      apiKey,
		model:       model,
		restyClient: resty.New().SetBaseURL(baseUrl),
		retry:       retryPolicyFromEnv(),
	}
}
func (s *Service) Config() *chat.Config {
	return s.config
}

// BaseURL 返回 API 基础地址。
func (s *Service) BaseURL() string {
	return s.baseUrl
}

// APIKey 返回 API 密钥。
func (s *Service) APIKey() string {
	return s.apiKey
}

// 一次 HTTP 请求的重试方针（2026-10-15 加：上游偶发"服务端错误/无响应"，
// 长回答 + 中转排队时尤其容易撞上，重发一次基本就过）。
//
// 为什么重试落在这一层而不是 agent 的 loop（2026-10-12 用户定）：要重发的是
// **这一个 HTTP 请求**（同一份 body 再 POST 一遍）；agent 那层重试的是"回合"，
// 走一圈要碰历史/事件，不是一个量级的事。
//
// 为什么**只重"一个块都没写过"的那几类**（见 retryableError）：重发不会产生重复内容。
// 流读了一半断掉（已写过块）绝不能重试——那会出双份正文。
//
// 三个数都能用环境变量临时改（不用重新编译就调得动，排障时最要紧）：
//
//	ANTHROPIC_RETRY_TIMES          重发几次（不含首发），默认 3
//	ANTHROPIC_RETRY_BASE_DELAY_MS  每次重发前等多久，默认 1000
//	ANTHROPIC_RETRY_MAX_DELAY_MS   单次等待的上限，默认 1000
//
// **默认就是"每秒一次、重试三次"**（2026-10-15 用户定：上游那点抽风通常一两秒就过去了，
// 与其退避等待，不如小步快跑）。想改成翻倍退避就把 MAX 调大（1000/2000/4000…）。
const (
	defaultRetryTimes     = 3
	defaultRetryBaseDelay = 1 * time.Second
	defaultRetryMaxDelay  = 1 * time.Second
)

// retryableStatuses 值得**原样重发**的 HTTP 状态码。
//
// 全是"这次不行、下次可能行"的：限流（429）、上游网关/过载（500/502/503/504/529）、
// 超时（408）、Cloudflare 系（520-524，deepseek 前面那层反代常回这几个）。
// **4xx 里的其它码不能重试**（400/401/403/404/413/422 是请求本身的问题，重发只是白等）。
var retryableStatuses = map[int]bool{
	408: true, 429: true,
	500: true, 502: true, 503: true, 504: true,
	520: true, 521: true, 522: true, 523: true, 524: true, 529: true,
}

// retryPolicy 重试方针（字段含义见上面那段注释）。
type retryPolicy struct {
	times     int           // 最多重发几次（不含首发）
	baseDelay time.Duration // 第一次重发的等待（之后翻倍）
	maxDelay  time.Duration // 单次等待上限
}

// retryPolicyFromEnv 读环境变量叠默认值。读不出来（没设/不是数/是负数）就用默认，
// **不报错**——这是排障时拧的旋钮，不该因为写错一个数就让整个服务起不来。
func retryPolicyFromEnv() retryPolicy {
	policy := retryPolicy{
		times:     defaultRetryTimes,
		baseDelay: defaultRetryBaseDelay,
		maxDelay:  defaultRetryMaxDelay,
	}
	if n, ok := envInt("ANTHROPIC_RETRY_TIMES"); ok && n >= 0 {
		policy.times = n
	}
	if n, ok := envInt("ANTHROPIC_RETRY_BASE_DELAY_MS"); ok && n > 0 {
		policy.baseDelay = time.Duration(n) * time.Millisecond
	}
	if n, ok := envInt("ANTHROPIC_RETRY_MAX_DELAY_MS"); ok && n > 0 {
		policy.maxDelay = time.Duration(n) * time.Millisecond
	}
	if policy.maxDelay < policy.baseDelay {
		policy.maxDelay = policy.baseDelay
	}
	return policy
}

func envInt(name string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return 0, false
	}
	return value, true
}

// retryableError 这次失败**可以原样重发**：请求还没往 writer 里写过任何块
// （HTTP 层就没通、状态码非 200、或整条流不是 SSE）。见 ChatWithStream 的循环。
type retryableError struct {
	err error
	// retryAfter 服务端让等多久（Retry-After 头，限流时会给）。0 = 没给，用退避。
	retryAfter time.Duration
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func (s *Service) ChatWithStream(ctx context.Context, chatMessages *chat.Messages, response chat.BlockWriter) error {
	config := chat.Combine(chatMessages.Config(), s.config)
	request := NewRequest(chatMessages, config)
	var err error
	for attempt := 0; ; attempt++ {
		err = s.chatOnce(ctx, request, response)
		if err == nil {
			return nil
		}
		delay, ok := s.retryDelay(err, attempt)
		if !ok {
			return err
		}
		log.Warn("模型请求失败，原样重发", "attempt", attempt+1,
			"delay", delay.String(), "error", err.Error())
		// **等的时候也要能被 ctx 打断**：上游真挂了的话，上层（用户点了停/会话关了）
		// 不该被我们按着睡满整轮退避
		if waitErr := sleepCtx(ctx, delay); waitErr != nil {
			return err
		}
	}
}

// retryDelay 这次失败该不该重发、等多久。
//
// **只有"一个块都没写过"的失败才允许重发**（chatOnce 把它们包成了 retryableError：
// HTTP 层没通 / 状态码可重试 / 整条流不是 SSE）。流读了一半断掉的那种不在其列——
// 已经写出去的正文再写一遍就是双份。ctx 取消/超时也不重发（重发也不会变好）。
func (s *Service) retryDelay(err error, attempt int) (time.Duration, bool) {
	var retryable *retryableError
	if !errors.As(err, &retryable) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	if attempt >= s.retry.times {
		return 0, false
	}
	// 服务端说了等多久就听它的（限流时的 Retry-After 比我们的退避更准），
	// 但**给个上限**：一个 300 秒的 Retry-After 不该把这一轮按在那儿五分钟
	if retryable.retryAfter > 0 {
		if retryable.retryAfter > maxRetryAfter {
			return maxRetryAfter, true
		}
		return retryable.retryAfter, true
	}
	delay := s.retry.baseDelay
	for i := 0; i < attempt && delay < s.retry.maxDelay; i++ {
		delay *= 2
	}
	if delay > s.retry.maxDelay {
		delay = s.retry.maxDelay
	}
	return delay, true
}

// maxRetryAfter 服务端 Retry-After 的封顶（见 retryDelay）。
const maxRetryAfter = 60 * time.Second

// sleepCtx 睡 duration，但 ctx 一断就立刻醒（返回 ctx 的错误）。
func sleepCtx(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// chatOnce 发一次请求并解析——重试的最小单位（见 ChatWithStream 的注释）。
func (s *Service) chatOnce(ctx context.Context, request *Request, response chat.BlockWriter) error {
	r, err := s.restyClient.R().
		SetContext(ctx).
		SetHeader("x-api-key", s.apiKey).
		SetHeader("anthropic-version", AnthropicVersion).
		SetHeader("Content-Type", "application/json").
		// **明确不要压缩**（2026-10-12 实测）：deepseek 侧偶发把响应压了却**不声明**
		//（content-encoding 空、内容却是二进制），SSE 解析整条流零命中、静默出"空回合"。
		// 要 identity 之后实测不再出现。SSE 本身也没多少可压的。
		SetHeader("Accept-Encoding", "identity").
		SetBody(request).
		SetResponseDoNotParse(true).
		Post("/v1/messages")
	if err != nil {
		// 请求压根没通 = 一个块都没写过，可以原样重发（ctx 取消了的那份由 retryDelay 挡掉）
		return &retryableError{err: fmt.Errorf("HTTP request failed: %w", err)}
	}
	if r.StatusCode() != 200 {
		body, readErr := io.ReadAll(r.RawResponse.Body)
		r.RawResponse.Body.Close()
		if readErr != nil {
			return fmt.Errorf("API error (%d), failed to read body: %w", r.StatusCode(), readErr)
		}
		apiErr := fmt.Errorf("API error (%d): %s", r.StatusCode(), string(body))
		// 限流/过载/网关抽风这类才重发；4xx 的其它码是请求本身的问题，重发只是白等
		if retryableStatuses[r.StatusCode()] {
			return &retryableError{err: apiErr, retryAfter: retryAfterOf(r)}
		}
		return apiErr
	}
	// 排障（默认关，与 parseSSE 的转储同一开关）：把响应头也记一笔——
	// 查"回来的不是 SSE 文本"这类问题时，content-encoding 是第一现场。
	if path := os.Getenv("ANTHROPIC_SSE_DUMP"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "--- HEADERS accept-encoding=%q content-encoding=%q content-type=%q ---\n",
				r.Request.Header.Get("Accept-Encoding"),
				r.Header().Get("Content-Encoding"),
				r.Header().Get("Content-Type"))
			f.Close()
		}
	}
	if err := s.parseSSE(ctx, r.RawResponse.Body, response); err != nil {
		// 整条流不是 SSE（零事件）= 还没写过任何块，可以重发——就是 2026-10-12 那次
		// deepseek 回没声明编码的压缩体、静默"空回合"的那条路（见 chat.ErrInvalidStream）
		if errors.Is(err, chat.ErrInvalidStream) {
			return &retryableError{err: err}
		}
		// 别的都是**读到一半出的事**（已写过块），交给上层如实报错，绝不重发
		return err
	}
	return nil
}

// retryAfterOf 读这次响应里的 Retry-After（限流时上游会给，比我们的退避更准）。
//
// 只认"秒数"这一种写法（`Retry-After: 30`）；HTTP-date 那种少见，不猜了，退回退避。
func retryAfterOf(r *resty.Response) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(r.Header().Get("Retry-After")))
	if err != nil || value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Second
}

func (s *Service) ID() string {
	return s.id
}

func (s *Service) parseSSE(ctx context.Context, body io.ReadCloser, resp chat.BlockWriter) error {
	defer body.Close()

	// 排障用（默认关）：ANTHROPIC_SSE_DUMP=<文件路径> 时把整条 SSE 原文按行落盘——
	// 查"空回合"到底是上游回了什么（2026-10-12 deepseek 偶发零内容，服务端什么都看不到）。
	var dump *os.File
	if path := os.Getenv("ANTHROPIC_SSE_DUMP"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			dump = f
			defer dump.Close()
			fmt.Fprintln(dump, "--- OPEN ---")
			defer fmt.Fprintln(dump, "--- CLOSE ---")
		}
	}

	scanner := bufio.NewScanner(body)
	recognized := 0 // 认出来的事件数；整条流 0 个 = 这不是 SSE（见文件尾的判断）
	firstLine := "" // 头一条非空行，出错时截一段拼进错误信息，排障不用再抓包
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			break
		default:
		}

		line := scanner.Text()
		if dump != nil {
			fmt.Fprintln(dump, line)
		}
		if firstLine == "" && strings.TrimSpace(line) != "" {
			firstLine = line
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var raw sseEvent
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			continue
		}
		recognized++

		switch raw.Type {
		case "message_start":
			if raw.Message != nil {
				resp.MessageStart(raw.Message.Usage.toChatUsage())
			}

		case "content_block_start":
			if raw.ContentBlock == nil {
				continue
			}
			switch raw.ContentBlock.Type {
			case "thinking":
				resp.BlockThinkingStart()
			case "tool_use":
				resp.BlockToolUseStart(raw.ContentBlock.ID, raw.ContentBlock.Name)
			case "server_tool_use":
				// Anthropic 内置工具（如 web_search）的服务器端调用，
				// input_json_delta 由 assembler 缓冲，flush 时 ParseStream 组装为结构化 JSON。
				resp.BlockServerToolUseStart(raw.ContentBlock.ID, raw.ContentBlock.Name)
			default:
				resp.BlockTextStart()
			}

		case "content_block_delta":
			if raw.Delta == nil {
				continue
			}
			// text/thinking/input_json 三种增量统一为 Delta，语义由当前 block 决定
			content := raw.Delta.Text + raw.Delta.Thinking + raw.Delta.PartialJSON
			if content != "" {
				resp.BlockDelta(content)
			}

		case "content_block_stop":
			resp.BlockStop()

		case "message_delta":
			if raw.Usage != nil {
				resp.MessageDelta(raw.Usage.toChatUsage())
			}
			if raw.Delta != nil && raw.Delta.StopReason != "" {
				resp.StopReason(chat.StopReason(raw.Delta.StopReason))
			}
		case "message_stop":
			return nil // 流正常结束
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE stream read error: %w", err)
	}
	// **整条流一个事件都没认出来 = 这不是 SSE 流**：以前这里返回 nil，
	// "零内容回合"就这么静默诞生了（用户视角是"说了话没回音"，服务端什么都看不到）。
	// 现在报 chat.ErrInvalidStream，上层的 loop 对这一类做重试（见 agent/agent.go）。
	if recognized == 0 {
		snippet := firstLine
		if len(snippet) > 160 {
			snippet = snippet[:160]
		}
		return fmt.Errorf("%w（首行：%q）", chat.ErrInvalidStream, snippet)
	}
	return nil
}
