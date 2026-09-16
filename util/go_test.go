package util

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

// captureLog 把标准库 log 的输出接进 buf，测试结束自动还原。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := new(bytes.Buffer)
	original := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(original) })
	return buf
}

// Recover 的返回值是要回给模型的，只带 panic 值；堆栈必须进后台日志。
//
// 两者分开是有意的：堆栈进模型上下文会污染上下文，不进日志则线上只能看到
// 一句 "nil pointer dereference"，工具崩了也不知道崩在哪一行。
func TestRecoverLogsStackAndReturnsPanicValueOnly(t *testing.T) {
	buf := captureLog(t)

	err := Recover(func() error {
		var p *int
		*p = 1 // 故意的空指针写，用来触发 panic
		return nil
	})

	if err == nil {
		t.Fatal("panic 应转成 error 返回")
	}
	if !strings.Contains(err.Error(), "nil pointer dereference") {
		t.Errorf("返回值应保留 panic 值，实际：%v", err)
	}
	if strings.Contains(err.Error(), "goroutine ") {
		t.Errorf("返回值不该带堆栈（会污染模型上下文）：%v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "panic recovered") {
		t.Errorf("后台日志应有 panic 提示，实际：%q", output)
	}
	if !strings.Contains(output, "goroutine ") || !strings.Contains(output, "go_test.go") {
		t.Errorf("后台日志应有堆栈（且能定位到调用点），实际：%q", output)
	}
}

// 没 panic 时不该产生 error，也不该打日志；fn 自己返回的 error 原样透出。
func TestRecoverWithoutPanicIsSilent(t *testing.T) {
	buf := captureLog(t)

	if err := Recover(func() error { return nil }); err != nil {
		t.Errorf("未 panic 不该有 error，实际：%v", err)
	}

	want := errors.New("检索失败")
	if err := Recover(func() error { return want }); !errors.Is(err, want) {
		t.Errorf("fn 返回的 error 应原样透出，实际：%v", err)
	}

	if buf.Len() != 0 {
		t.Errorf("未 panic 不该打日志，实际：%q", buf.String())
	}
}
