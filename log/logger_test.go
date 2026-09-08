package log

import (
	"sync"
	"testing"
)

// mockLogger 记录调用，用于验证 SetLogger 生效。
type mockLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (m *mockLogger) Debug(msg string, args ...any) { m.record("DEBUG " + msg) }
func (m *mockLogger) Info(msg string, args ...any)  { m.record("INFO " + msg) }
func (m *mockLogger) Warn(msg string, args ...any)  { m.record("WARN " + msg) }
func (m *mockLogger) Error(msg string, args ...any) { m.record("ERROR " + msg) }

func (m *mockLogger) record(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, s)
}

func (m *mockLogger) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.msgs)
}

// 测试 SetLogger 替换全局 logger 后，日志走自定义实现。
func TestSetLogger_Custom(t *testing.T) {
	mock := &mockLogger{}
	SetLogger(mock)
	defer SetLogger(nil) // 恢复默认

	Debug("d1")
	Info("i1")
	Warn("w1")
	Error("e1")

	if got := mock.count(); got != 4 {
		t.Fatalf("expected 4 calls, got %d", got)
	}
}

// 测试 SetLogger(nil) 恢复默认 logger。
func TestSetLogger_NilRestoresDefault(t *testing.T) {
	mock := &mockLogger{}
	SetLogger(mock)
	Debug("before")
	if got := mock.count(); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}

	SetLogger(nil)
	Debug("after") // 应走默认 slog，不走 mock
	if got := mock.count(); got != 1 {
		t.Fatalf("mock should still be 1 after restore, got %d", got)
	}

	// 验证 GetLogger 返回的不再是 mock
	if GetLogger() == mock {
		t.Error("GetLogger should not return mock after SetLogger(nil)")
	}
}

// 测试 GetLogger 返回当前 logger。
func TestGetLogger(t *testing.T) {
	mock := &mockLogger{}
	SetLogger(mock)
	defer SetLogger(nil)

	if GetLogger() != mock {
		t.Error("GetLogger should return the mock")
	}
}

// 并发读写：多个 goroutine 同时调用 Debug 和 SetLogger，不应 panic。
func TestConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup

	// 恢复默认 logger 确保基线
	SetLogger(nil)

	wg.Add(20)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				Debug("concurrent")
				Info("concurrent")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				SetLogger(nil)
			}
		}()
	}
	wg.Wait()
}
