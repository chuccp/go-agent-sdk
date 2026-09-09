package tools

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
	"resty.dev/v3"
)

// maxResponseBody 响应体最大显示字节数（避免超长响应撑爆上下文）。
const maxResponseBody = 8192

// HttpRequestTool 发送 HTTP 请求的工具，用于无命令行环境调用外部 API。
type HttpRequestTool struct {
	client *resty.Client
}

// HttpRequestOption 配置 HttpRequestTool 的可选参数。
type HttpRequestOption func(*HttpRequestTool)

// WithHTTPTimeout 设置请求超时时间（默认 30 秒）。
func WithHTTPTimeout(d time.Duration) HttpRequestOption {
	return func(t *HttpRequestTool) {
		t.client.SetTimeout(d)
	}
}

// WithHTTPBaseURL 设置基础 URL（请求的 url 参数会拼接其后）。
func WithHTTPBaseURL(baseURL string) HttpRequestOption {
	return func(t *HttpRequestTool) {
		t.client.SetBaseURL(baseURL)
	}
}

// NewHttpRequestTool 创建 HTTP 请求工具。
func NewHttpRequestTool(opts ...HttpRequestOption) agent.ToolExecutor {
	t := &HttpRequestTool{
		client: resty.New().SetTimeout(30 * time.Second),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Name 返回工具名称。
func (t *HttpRequestTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *HttpRequestTool) UsagePrompt() string { return "" }

func (t *HttpRequestTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "http_request",
		Description: "发送 HTTP 请求并返回响应。适用于：调用 REST API、获取网页数据、测试接口等。" +
			" 支持 GET/POST/PUT/DELETE/PATCH 方法，可自定义 headers、query params、body。" +
			" 返回状态码、响应头、响应体（超过 8KB 自动截断）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"method": map[string]any{
					"type":        "string",
					"description": "HTTP 方法",
					"enum":        []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"},
				},
				"url": map[string]any{
					"type":        "string",
					"description": "请求 URL（完整地址或相对路径，如已配置 baseUrl）。例如: 'https://api.example.com/data'",
				},
				"headers": map[string]any{
					"type":        "object",
					"description": "请求头键值对。例如: {\"Authorization\": \"Bearer token\"}",
				},
				"params": map[string]any{
					"type":        "object",
					"description": "URL 查询参数键值对。例如: {\"page\": \"1\", \"limit\": \"10\"}",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "请求体内容。JSON 字符串或纯文本。例如: '{\"name\": \"test\"}'",
				},
				"content_type": map[string]any{
					"type":        "string",
					"description": "Content-Type 请求头。默认自动推断（有 body 时为 application/json）。例如: 'application/x-www-form-urlencoded'",
				},
			},
			"required": []string{"method", "url"},
		},
	}
}

// Execute 实现 agent.ToolExecutor 接口：发送 HTTP 请求，结果写入 writer；错误经 ErrorText 写入。
func (t *HttpRequestTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	args := turn.Args()
	method := strings.ToUpper(strings.TrimSpace(args.GetString("method")))
	url := strings.TrimSpace(args.GetString("url"))

	if method == "" {
		writer.ErrorText(errors.New("缺少 method 参数"))
		return
	}
	if url == "" {
		writer.ErrorText(errors.New("缺少 url 参数"))
		return
	}

	req := t.client.R()

	// 设置请求头
	if headers := args.GetObject("headers"); headers != nil {
		headers.ForEach(func(k string, v value.Value) bool {
			if v.IsText() {
				req.SetHeader(k, v.String())
			}
			return true
		})
	}

	// 设置查询参数
	if params := args.GetObject("params"); params != nil {
		params.ForEach(func(k string, v value.Value) bool {
			if v.IsText() {
				req.SetQueryParam(k, v.String())
			}
			return true
		})
	}

	// 设置请求体
	body := args.GetString("body")
	if body != "" {
		contentType := strings.TrimSpace(args.GetString("content_type"))
		if contentType == "" {
			contentType = "application/json"
		}
		req.SetHeader("Content-Type", contentType)
		req.SetBody(body)
	}

	// 发送请求
	resp, err := req.Execute(method, url)
	if err != nil {
		writer.ErrorText(fmt.Errorf("HTTP 请求失败: %w", err))
		return
	}

	// 格式化输出
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Status: %d %s\n", resp.StatusCode(), resp.Status()))

	// 响应头（仅关键头）
	sb.WriteString("Headers:\n")
	for _, h := range []string{"Content-Type", "Content-Length", "Date", "Server", "X-Request-Id"} {
		if v := resp.Header().Get(h); v != "" {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", h, v))
		}
	}

	// 响应体
	bodyStr := resp.String()
	if len(bodyStr) > maxResponseBody {
		bodyStr = bodyStr[:maxResponseBody] + "\n...(truncated)"
	}
	if bodyStr != "" {
		sb.WriteString(fmt.Sprintf("\nBody:\n%s", bodyStr))
	}

	writer.FullText(sb.String())
}
