package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
	"resty.dev/v3"
)

const (
	AnthropicVersion      = "2023-06-01"
	DefaultBaseURL        = "https://api.anthropic.com"
	DefaultMaxTokens      = 4096
	DefaultThinkingBudget = 10000
)

// Service 定义 Anthropic 聊天服务接口，嵌入通用的 chat.Service。
type Service interface {
	chat.Service
}

// serviceImpl 是 Service 的具体实现，封装 HTTP 客户端与配置。
type serviceImpl struct {
	config      *Config
	restyClient *resty.Client
}

// NewService 根据给定配置创建一个 Anthropic 聊天服务实例。
// 若 BaseURL 为空则默认使用 Anthropic 官方 API 地址。
func NewService(config *Config) Service {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &serviceImpl{
		config:      config,
		restyClient: resty.New().SetBaseURL(baseURL),
	}
}

// ChatWithStream 向 Anthropic Messages API 发送流式请求，
// 将解析后的内容写入 response（独享 StreamWriter），完成后关闭。
func (s *serviceImpl) ChatWithStream(ctx context.Context, chatMessages *chat.Request, response *chat.StreamWriter) error {
	s.applyDefaults(chatMessages)
	chatMessages.Stream = true

	r, err := s.restyClient.R().
		SetContext(ctx).
		SetHeader("x-api-key", s.config.APIKey).
		SetHeader("anthropic-version", AnthropicVersion).
		SetHeader("Content-Type", "application/json").
		SetBody(chatMessages).
		SetResponseDoNotParse(true).
		Post("/v1/messages")
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}

	if r.StatusCode() != 200 {
		body, readErr := io.ReadAll(r.RawResponse.Body)
		r.RawResponse.Body.Close()
		if readErr != nil {
			return fmt.Errorf("API error (%d), failed to read body: %w", r.StatusCode(), readErr)
		}
		return fmt.Errorf("API error (%d): %s", r.StatusCode(), string(body))
	}

	return s.parseSSE(r.RawResponse.Body, response)
}

// applyDefaults 将 Config 中的默认值填入请求。
func (s *serviceImpl) applyDefaults(m *chat.Request) {
	if m.Model == "" && s.config.Model != "" {
		m.Model = s.config.Model
	}
	if m.MaxTokens == 0 {
		m.MaxTokens = DefaultMaxTokens
	}
	// 应用思考级别配置
	if m.Thinking == nil && s.config.Thinking {
		budget := s.config.ThinkingBudget
		if budget == 0 {
			budget = DefaultThinkingBudget
		}
		m.Thinking = &chat.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: budget,
		}
	}
}

// -------- SSE 解析 --------

// sseEvent 表示 Anthropic 流式响应中的一条原始 SSE 事件。
type sseEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Delta        *sseDelta       `json:"delta"`
	ContentBlock json.RawMessage `json:"content_block"`
	Message      *sseMessage     `json:"message"`
	Usage        *chat.Usage     `json:"usage"`
}

type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

type sseMessage struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	Usage      chat.Usage      `json:"usage"`
	StopReason chat.StopReason `json:"stop_reason"`
}

// parseSSE 从 HTTP 响应体中读取 SSE 事件流，转换为简化的 Stream 项写入 response：
// 块开始（BlockStart）→ 内容增量（Delta）→ 停止原因/用量，解析完成后关闭 response。
// SSE 协议细节（index/message_start 等）在这里被消化，不外泄到流模型。
// 读取失败时返回错误（由调用方 ChatWithStream 透传）。
func (s *serviceImpl) parseSSE(body io.ReadCloser, resp *chat.StreamWriter) error {
	defer resp.Close()
	defer body.Close()

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var raw sseEvent
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			continue
		}

		switch raw.Type {
		case "message_start":
			if raw.Message != nil {
				resp.Write(&chat.Start{})
				usage := raw.Message.Usage
				resp.Usage(&usage)
			}

		case "content_block_start":
			if raw.ContentBlock == nil {
				continue
			}
			block, err := chat.UnmarshalBlock(raw.ContentBlock)
			if err != nil {
				continue
			}
			switch b := block.(type) {
			case *chat.ThinkingBlock:
				resp.Write(&chat.ThinkingBlockStart{})
			case *chat.ToolUseBlock:
				resp.Write(&chat.ToolUseBlockStart{Id: b.ID, Name: b.Name})
			default:
				resp.Write(&chat.TextBlockStart{})
			}

		case "content_block_delta":
			if raw.Delta == nil {
				continue
			}
			// text/thinking/input_json 三种增量统一为 Delta，语义由当前 block 决定
			content := raw.Delta.Text + raw.Delta.Thinking + raw.Delta.PartialJSON
			if content != "" {
				resp.Write(&chat.Delta{Content: content})
			}

		case "message_delta":
			if raw.Delta != nil && raw.Delta.StopReason != "" {
				resp.StopReason(chat.StopReason(raw.Delta.StopReason))
			}
			if raw.Usage != nil {
				resp.Usage(raw.Usage)
			}

		case "message_stop":
			return nil // 流正常结束
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE stream read error: %w", err)
	}
	return nil
}
