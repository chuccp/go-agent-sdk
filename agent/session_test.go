package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/util"
)

func newTestContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func TestSessions_AddAndGet(t *testing.T) {
	sessions := NewSessions()
	ctx := &SessionContext{sessionId: "s1"}
	s := &Session{sessionContext: ctx, lastTime: util.GetSecondTime()}

	sessions.Add(s)

	got, ok := sessions.Get("s1")
	if !ok {
		t.Fatal("expected to find session s1")
	}
	if got != s {
		t.Error("Get returned different session")
	}
}

func TestSessions_GetNotFound(t *testing.T) {
	sessions := NewSessions()

	_, ok := sessions.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}

func TestSessions_Remove(t *testing.T) {
	sessions := NewSessions()
	ctx := &SessionContext{sessionId: "s1"}
	s := &Session{sessionContext: ctx, lastTime: util.GetSecondTime()}

	sessions.Add(s)
	sessions.Remove("s1")

	_, ok := sessions.Get("s1")
	if ok {
		t.Error("session should be removed")
	}
}

func TestSessions_RemoveNonexistent(t *testing.T) {
	sessions := NewSessions()
	// 不应该 panic
	sessions.Remove("nonexistent")
}

func TestSessions_Overwrite(t *testing.T) {
	sessions := NewSessions()
	ctx1 := &SessionContext{sessionId: "s1"}
	s1 := &Session{sessionContext: ctx1, lastTime: util.GetSecondTime()}
	ctx2 := &SessionContext{sessionId: "s1"}
	s2 := &Session{sessionContext: ctx2, lastTime: util.GetSecondTime()}

	sessions.Add(s1)
	sessions.Add(s2)

	got, ok := sessions.Get("s1")
	if !ok {
		t.Fatal("expected to find session s1")
	}
	if got != s2 {
		t.Error("expected second session to overwrite first")
	}
}

func TestSessions_ConcurrentAccess(t *testing.T) {
	sessions := NewSessions()
	var wg sync.WaitGroup

	// 并发写入
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ctx := &SessionContext{sessionId: string(rune('A' + id%26))}
			s := &Session{sessionContext: ctx, lastTime: util.GetSecondTime()}
			sessions.Add(s)
		}(i)
	}

	// 并发读取
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessions.Get("A")
		}()
	}

	wg.Wait()
}

func TestCheckTimeout_NoTimeout(t *testing.T) {
	// sessionTimeout=0 时，checkTimeout 应立即返回（不阻塞）
	ctx, cancel := newTestContext()
	defer cancel()
	s := &Session{
		ctx:            ctx,
		sessionTimeout: 0,
		lastTime:       util.GetSecondTime(),
	}
	// 不应阻塞
	done := make(chan struct{})
	go func() {
		s.checkTimeout()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(1 * time.Second):
		t.Fatal("checkTimeout with timeout=0 should return immediately")
	}
}

func TestCheckTimeout_DestroysOnTimeout(t *testing.T) {
	sessions := NewSessions()
	ctx, cancel := newTestContext()
	defer cancel()

	s := &Session{
		sessionContext: &SessionContext{sessionId: "timeout-test"},
		ctx:            ctx,
		cancel:         cancel,
		sessionTimeout: 1, // 1秒超时
		lastTime:       util.GetSecondTime() - 5, // 5秒前
		sessions:       sessions,
	}
	sessions.Add(s)

	done := make(chan struct{})
	go func() {
		s.checkTimeout()
		close(done)
	}()

	// 等待 ticker 触发（最多2秒）
	select {
	case <-done:
		// ok
	case <-time.After(3 * time.Second):
		t.Fatal("checkTimeout should destroy session on timeout")
	}

	// session 应该被移除
	_, ok := sessions.Get("timeout-test")
	if ok {
		t.Error("session should be removed after timeout")
	}
}

func TestCheckTimeout_ActiveSessionNotDestroyed(t *testing.T) {
	sessions := NewSessions()
	ctx, cancel := newTestContext()
	defer cancel()

	s := &Session{
		sessionContext: &SessionContext{sessionId: "active-test"},
		ctx:            ctx,
		cancel:         cancel,
		sessionTimeout: 2, // 2秒超时
		lastTime:       util.GetSecondTime() - 1, // 1秒前（未超时）
		sessions:       sessions,
	}
	sessions.Add(s)

	s.checkTimeout()

	_, ok := sessions.Get("active-test")
	if !ok {
		t.Error("active session should not be destroyed")
	}
}
