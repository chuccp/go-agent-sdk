package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/api/chat/anthropic"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

func TestSearchTool_Definition(t *testing.T) {
	chatInst := chat.NewChat()
	tool := NewSearchTool(chatInst)

	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "web_search")
	}
	if tool.UsagePrompt() != "" {
		t.Errorf("UsagePrompt() = %q, want empty", tool.UsagePrompt())
	}

	def := tool.Definition()
	if def.Name != "web_search" {
		t.Errorf("Definition().Name = %q, want %q", def.Name, "web_search")
	}
	if def.Description == "" {
		t.Error("Definition().Description should not be empty")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema should have properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("InputSchema should have 'query' property")
	}
	required, ok := def.InputSchema["required"].([]string)
	if !ok {
		t.Fatal("InputSchema should have required")
	}
	if len(required) != 1 || required[0] != "query" {
		t.Errorf("required = %v, want [query]", required)
	}
}

func TestSearchTool_WithEndpoint(t *testing.T) {
	chatInst := chat.NewChat()
	tool := NewSearchTool(chatInst, WithSearchEndpoint("/custom/search"))
	st := tool.(*SearchTool)
	if st.endpoint != "/custom/search" {
		t.Errorf("endpoint = %q, want %q", st.endpoint, "/custom/search")
	}
}

func TestSearchTool_Execute_MissingQuery(t *testing.T) {
	chatInst := chat.NewChat()
	chatInst.Register(anthropic.NewService("default", "https://api.example.com", "key", "model"))

	tool := NewSearchTool(chatInst)
	turn := agent.NewTurn(value.NewObject())

	rec := &searchTestRecorder{}
	w := chat.NewBlockStream(rec)
	writer := chat.NewToolResultBlockStream(w, "s1")

	tool.Execute(turn, writer)

	text := collectSearchTestText(w)
	if text == "" {
		t.Error("expected error text for missing query")
	}
}

func TestSearchTool_Execute_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth: %s", r.Header.Get("Authorization"))
		}

		var req searchRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Query == "" {
			t.Error("empty query in request")
		}

		json.NewEncoder(w).Encode(searchResponse{
			Results: []searchItem{
				{Title: "Go 官方文档", URL: "https://go.dev", Snippet: "Go is an open source programming language..."},
				{Title: "Go Wiki", URL: "https://wiki.go.dev", Snippet: "Go language wiki..."},
			},
		})
	}))
	defer srv.Close()

	chatInst := chat.NewChat()
	chatInst.Register(anthropic.NewService("default", srv.URL, "test-key", "model"))

	tool := NewSearchTool(chatInst)
	args := value.NewObject()
	args.PutAny("query", "Go 语言")
	turn := agent.NewTurn(args)

	rec := &searchTestRecorder{}
	w := chat.NewBlockStream(rec)
	writer := chat.NewToolResultBlockStream(w, "s2")

	tool.Execute(turn, writer)

	text := collectSearchTestText(w)
	if text == "" {
		t.Fatal("expected search results")
	}
	if !searchContains(text, "Go 官方文档") {
		t.Errorf("expected 'Go 官方文档' in result, got: %s", text)
	}
	if !searchContains(text, "https://go.dev") {
		t.Errorf("expected URL in result, got: %s", text)
	}
}

func TestSearchTool_Execute_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	chatInst := chat.NewChat()
	chatInst.Register(anthropic.NewService("default", srv.URL, "key", "model"))

	tool := NewSearchTool(chatInst)
	args := value.NewObject()
	args.PutAny("query", "test")
	turn := agent.NewTurn(args)

	rec := &searchTestRecorder{}
	w := chat.NewBlockStream(rec)
	writer := chat.NewToolResultBlockStream(w, "s3")

	tool.Execute(turn, writer)

	text := collectSearchTestText(w)
	if !searchContains(text, "500") {
		t.Errorf("expected error about status 500, got: %s", text)
	}
}

func TestSearchTool_Execute_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(searchResponse{Results: []searchItem{}})
	}))
	defer srv.Close()

	chatInst := chat.NewChat()
	chatInst.Register(anthropic.NewService("default", srv.URL, "key", "model"))

	tool := NewSearchTool(chatInst)
	args := value.NewObject()
	args.PutAny("query", "nonexistent_xyz")
	turn := agent.NewTurn(args)

	rec := &searchTestRecorder{}
	w := chat.NewBlockStream(rec)
	writer := chat.NewToolResultBlockStream(w, "s4")

	tool.Execute(turn, writer)

	text := collectSearchTestText(w)
	if !searchContains(text, "未找到") {
		t.Errorf("expected '未找到' for empty results, got: %s", text)
	}
}

func TestSearchTool_Execute_WrongServiceType(t *testing.T) {
	chatInst := chat.NewChat()
	chatInst.Register(&mockService{id: "mock"})

	tool := NewSearchTool(chatInst)
	args := value.NewObject()
	args.PutAny("query", "test")
	turn := agent.NewTurn(args)

	rec := &searchTestRecorder{}
	w := chat.NewBlockStream(rec)
	writer := chat.NewToolResultBlockStream(w, "s5")

	tool.Execute(turn, writer)

	text := collectSearchTestText(w)
	if !searchContains(text, "仅支持 Anthropic") {
		t.Errorf("expected type assertion error, got: %s", text)
	}
}

// ==================== test helpers ====================

type searchTestRecorder struct {
	blocks []chat.Block
}

func (r *searchTestRecorder) SendBlock(block chat.Block) uint64 {
	r.blocks = append(r.blocks, block)
	return 0
}

func collectSearchTestText(w *chat.BlockStream) string {
	var result string
	for _, b := range w.ReadBlocks() {
		if tb, ok := b.(*chat.TextBlock); ok {
			result += tb.Text
		}
	}
	return result
}

func searchContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// mockService 实现 chat.Service 接口，用于测试类型断言失败的场景。
type mockService struct {
	id string
}

func (m *mockService) ChatWithStream(_ context.Context, _ *chat.Messages, _ *chat.BlockStream) error {
	return nil
}

func (m *mockService) ID() string { return m.id }
