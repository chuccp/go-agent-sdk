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
	}
}

func (s *Service) ChatWithStream(ctx context.Context, chatMessages *chat.Messages, response *chat.BlockStream) error {
	config := chat.Combine(s.config, chatMessages.Config)
	request := NewRequest(chatMessages, config)
	r, err := s.restyClient.R().
		SetContext(ctx).
		SetHeader("x-api-key", s.apiKey).
		SetHeader("anthropic-version", AnthropicVersion).
		SetHeader("Content-Type", "application/json").
		SetBody(request).
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

func (s *Service) ID() string {
	return s.id
}

func (s *Service) parseSSE(ctx context.Context, body io.ReadCloser, resp *chat.BlockStream) error {
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
				resp.Delta(content)
			}

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
	return nil
}
