package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

func TestHttpRequestTool_Definition(t *testing.T) {
	tool := NewHttpRequestTool()
	if tool.Name() != "http_request" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "http_request")
	}
	if tool.UsagePrompt() != "" {
		t.Errorf("UsagePrompt() = %q, want empty", tool.UsagePrompt())
	}
	def := tool.Definition()
	if def.Name != "http_request" {
		t.Errorf("Definition().Name = %q, want %q", def.Name, "http_request")
	}
	if def.Description == "" {
		t.Error("Definition().Description should not be empty")
	}
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema should have properties")
	}
	if _, ok := props["method"]; !ok {
		t.Error("missing 'method' property")
	}
	if _, ok := props["url"]; !ok {
		t.Error("missing 'url' property")
	}
}

func TestHttpRequestTool_GET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Query().Get("q") != "hello" {
			t.Errorf("expected q=hello, got %s", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
	}))
	defer srv.Close()

	tool := NewHttpRequestTool()
	args := value.NewObject()
	args.PutAny("method", "GET")
	args.PutAny("url", srv.URL)
	params := value.NewObject()
	params.PutAny("q", "hello")
	args.PutAny("params", params)
	turn := agent.NewTurn(args)

	text := executeHttpTool(t, tool, turn)
	if !httpContains(text, "200") {
		t.Errorf("expected status 200, got: %s", text)
	}
	if !httpContains(text, "ok") {
		t.Errorf("expected 'ok' in body, got: %s", text)
	}
}

func TestHttpRequestTool_POST_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		if body["name"] != "test" {
			t.Errorf("expected name=test, got %s", body["name"])
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()

	tool := NewHttpRequestTool()
	args := value.NewObject()
	args.PutAny("method", "POST")
	args.PutAny("url", srv.URL)
	args.PutAny("body", `{"name":"test"}`)
	turn := agent.NewTurn(args)

	text := executeHttpTool(t, tool, turn)
	if !httpContains(text, "201") {
		t.Errorf("expected status 201, got: %s", text)
	}
}

func TestHttpRequestTool_WithHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "value" {
			t.Errorf("expected X-Custom=value, got %s", r.Header.Get("X-Custom"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tool := NewHttpRequestTool()
	args := value.NewObject()
	args.PutAny("method", "GET")
	args.PutAny("url", srv.URL)
	headers := value.NewObject()
	headers.PutAny("X-Custom", "value")
	args.PutAny("headers", headers)
	turn := agent.NewTurn(args)

	text := executeHttpTool(t, tool, turn)
	if !httpContains(text, "200") {
		t.Errorf("expected status 200, got: %s", text)
	}
}

func TestHttpRequestTool_MissingMethod(t *testing.T) {
	tool := NewHttpRequestTool()
	args := value.NewObject()
	args.PutAny("url", "http://example.com")
	turn := agent.NewTurn(args)

	text := executeHttpTool(t, tool, turn)
	if !httpContains(text, "缺少 method") {
		t.Errorf("expected error about missing method, got: %s", text)
	}
}

func TestHttpRequestTool_MissingURL(t *testing.T) {
	tool := NewHttpRequestTool()
	args := value.NewObject()
	args.PutAny("method", "GET")
	turn := agent.NewTurn(args)

	text := executeHttpTool(t, tool, turn)
	if !httpContains(text, "缺少 url") {
		t.Errorf("expected error about missing url, got: %s", text)
	}
}

func TestHttpRequestTool_ConnectionError(t *testing.T) {
	tool := NewHttpRequestTool()
	args := value.NewObject()
	args.PutAny("method", "GET")
	args.PutAny("url", "http://127.0.0.1:1")
	turn := agent.NewTurn(args)

	text := executeHttpTool(t, tool, turn)
	if !httpContains(text, "HTTP 请求失败") {
		t.Errorf("expected connection error, got: %s", text)
	}
}

// ── 测试辅助 ──

type httpTestRecorder struct {
	blocks []chat.Block
}

func (r *httpTestRecorder) SendBlock(block chat.Block) uint64 {
	r.blocks = append(r.blocks, block)
	return 0
}

func executeHttpTool(t *testing.T, tool agent.ToolExecutor, turn *agent.Turn) string {
	t.Helper()
	rec := &httpTestRecorder{}
	w := chat.NewBlockStream(rec)
	writer := chat.NewToolResultBlockStream(w, "http1")
	tool.Execute(turn, writer)
	var result string
	for _, b := range w.ReadBlocks() {
		if tb, ok := b.(*chat.TextBlock); ok {
			result += tb.Text
		}
	}
	return result
}

func httpContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
