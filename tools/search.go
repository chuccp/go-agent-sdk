package tools

import (
	"encoding/json"
	"fmt"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/api/chat/anthropic"
	"github.com/chuccp/go-agent-sdk/chat"
	"resty.dev/v3"
)

const defaultSearchEndpoint = "/v1/search"

// SearchTool 调用搜索 API 端点获取网络搜索结果。
//
// 构造时传入 *chat.Chat，执行时通过类型断言获取 *anthropic.Service
// 的 baseUrl 和 apiKey，复用同一个搜索服务地址与认证信息。
type SearchTool struct {
	chat     *chat.Chat
	endpoint string
}

// SearchToolOption 配置 SearchTool 的可选参数。
type SearchToolOption func(*SearchTool)

// WithSearchEndpoint 自定义搜索端点路径（默认 "/v1/search"）。
func WithSearchEndpoint(endpoint string) SearchToolOption {
	return func(t *SearchTool) {
		t.endpoint = endpoint
	}
}

// NewSearchTool 创建搜索工具，通过 *chat.Chat 自动获取搜索服务地址与认证信息。
//
// 执行时从 Chat.GetService() 获取 Service，类型断言为 *anthropic.Service
// 以提取 baseUrl 和 apiKey，搜索请求发往 {baseUrl}{endpoint}。
func NewSearchTool(chatInst *chat.Chat, opts ...SearchToolOption) agent.ToolExecutor {
	t := &SearchTool{
		chat:     chatInst,
		endpoint: defaultSearchEndpoint,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Name 返回工具名称。
func (t *SearchTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *SearchTool) UsagePrompt() string { return "" }

// Definition 返回工具的元数据定义，发给模型用于生成 tool_use content block。
func (t *SearchTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "web_search",
		Description: "搜索互联网获取最新信息。适用于：查询实时数据、查找文档、了解最新动态、验证事实等。" +
			" 返回搜索结果列表，包含标题、链接和摘要。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "搜索查询词。例如: 'Go 语言 goroutine 最佳实践', 'Anthropic Claude API 文档'",
				},
			},
			"required": []string{"query"},
		},
	}
}

// searchRequest 是发给搜索 API 的请求体。
type searchRequest struct {
	Query string `json:"query"`
}

// searchResponse 是搜索 API 返回的响应体。
type searchResponse struct {
	Results []searchItem `json:"results"`
}

// searchItem 是单条搜索结果。
type searchItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Execute 实现 agent.ToolExecutor 接口：
// 1. 通过 turn.Context().GetChat() 获取 Chat
// 2. Chat.GetService(nil) 获取 Service，类型断言为 *anthropic.Service
// 3. 提取 baseUrl + apiKey，调用搜索 API
// 错误经 ErrorText 以文本写入（随 tool_result 回传给模型）。
func (t *SearchTool) Execute(turn *agent.Turn, writer *chat.ToolResultBlockStream) {
	args := turn.Args()
	query := args.GetString("query")
	if query == "" {
		writer.ErrorText(fmt.Errorf("缺少 query 参数"))
		return
	}

	// 通过类型断言获取 *anthropic.Service 的 baseUrl 和 apiKey
	svc := t.chat.GetService(nil)
	anthropicSvc, ok := svc.(*anthropic.Service)
	if !ok {
		writer.ErrorText(fmt.Errorf("搜索工具仅支持 Anthropic 服务，当前服务类型: %T", svc))
		return
	}

	restyClient := resty.New().SetBaseURL(anthropicSvc.BaseURL())
	apiKey := anthropicSvc.APIKey()

	reqBody := &searchRequest{Query: query}
	resp, err := restyClient.R().
		SetHeader("Authorization", "Bearer "+apiKey).
		SetHeader("Content-Type", "application/json").
		SetBody(reqBody).
		Post(t.endpoint)
	if err != nil {
		writer.ErrorText(fmt.Errorf("搜索请求失败: %w", err))
		return
	}
	if resp.StatusCode() != 200 {
		writer.ErrorText(fmt.Errorf("搜索 API 错误 (%d): %s", resp.StatusCode(), resp.String()))
		return
	}

	var result searchResponse
	if err := json.Unmarshal(resp.Bytes(), &result); err != nil {
		writer.ErrorText(fmt.Errorf("解析搜索结果失败: %w", err))
		return
	}

	writer.FullText(formatSearchResponse(result))
}

// formatSearchResponse 将搜索结果格式化为文本。
func formatSearchResponse(resp searchResponse) string {
	if len(resp.Results) == 0 {
		return "未找到相关搜索结果。"
	}
	b, _ := json.MarshalIndent(resp.Results, "", "  ")
	return string(b)
}
