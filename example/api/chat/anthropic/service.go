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
// 将解析后的内容写入 response（独享 BlockStream），完成后关闭。
func (s *serviceImpl) ChatWithStream(ctx context.Context, chatMessages *chat.Request, response *chat.BlockStream) error {
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

	return s.parseSSE(ctx, r.RawResponse.Body, response)
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

// SSE 事件结构体（sseEvent/sseContentBlock/sseDelta/sseMessage）定义见 entity.go。

// parseSSE 从 HTTP 响应体中读取 SSE 事件流，转换为简化的 Stream 项写入 response：
// 块开始（BlockStart）→ 内容增量（Delta）→ 停止原因/用量，解析完成后关闭 response。
// SSE 协议细节（index/message_start 等）在这里被消化，不外泄到流模型。
// 请求上下文取消时主动关闭响应体中断流读取（Go 的 http 客户端在响应头已接收后
// 不会因 ctx 取消中断 Body 读取，必须显式关闭），读取失败时返回错误（由调用方透传）。
func (s *serviceImpl) parseSSE(ctx context.Context, body io.ReadCloser, resp *chat.BlockStream) error {
	defer body.Close()
	// 停止支持：ctx 取消时关闭 body，scanner 读取将立即报错返回

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			break
		default:
		}

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
			switch raw.ContentBlock.Type {
			case "thinking":
				resp.Write(&chat.ThinkingBlockStart{})
			case "tool_use":
				resp.Write(&chat.ToolUseBlockStart{Id: raw.ContentBlock.ID, Name: raw.ContentBlock.Name})
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
