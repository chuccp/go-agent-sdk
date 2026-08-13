package agent_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/tools"
)

// fakeProvider 按调用次序返回不同响应：
// 第 1 次 → tool_use（触发工具执行）；第 2 次 → 普通文本 end_turn。
type fakeProvider struct {
	calls int
}

func (f *fakeProvider) ChatWithStream(_ context.Context, req *chat.Request, w *chat.StreamWriter) error {
	f.calls++
	if f.calls == 1 {
		w.Write(&chat.ToolUseBlockStart{Id: "tu_1", Name: "fake_tool"})
		w.Write(&chat.Delta{Content: `{"command":"echo hi"}`})
		w.StopReason(chat.StopReasonToolUse)
		return nil
	}
	w.Write(&chat.TextBlockStart{})
	w.Write(&chat.Delta{Content: "工具结果已收到"})
	w.StopReason(chat.StopReasonEndTurn)
	return nil
}

// fakeTool 简单回显工具。
type fakeTool struct{}

func (t *fakeTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{Name: "fake_tool", Description: "fake", InputSchema: map[string]any{"type": "object"}}
}
func (t *fakeTool) Name() string { return "fake_tool" }
func (t *fakeTool) UsagePrompt() string { return "" }
func (t *fakeTool) Execute(_ *agent.Turn, writer *agent.BlockStream) error {
	return writer.WriteBlock(chat.NewTextBlock("fake tool output"))
}

// waitForDone 读到 done 返回 true；超时 dump 全部协程栈后 fail。
func waitForDone(t *testing.T, client *agent.Client, label string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	type result struct {
		evt *chat.ClientEvent
	}
	ch := make(chan result, 1)
	go func() {
		for {
			evt := client.ReadEvent()
			if evt == nil {
				ch <- result{}
				return
			}
			if evt.EventType == chat.EventTypeDone {
				ch <- result{evt}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.evt == nil {
			t.Fatalf("[%s] ReadEvent 返回 nil，未等到 done", label)
		}
	case <-deadline:
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("[%s] 10s 超时未收到 done，goroutine dump:\n%s", label, filterStack(string(buf[:n])))
	}
}

// filterStack 只保留本项目相关协程，减小输出。
func filterStack(s string) string {
	var out []string
	for _, g := range strings.Split(s, "\n\n") {
		if strings.Contains(g, "go-agent-sdk") {
			out = append(out, g)
		}
	}
	return strings.Join(out, "\n\n")
}

// TestTwoRoundsWithTool 复现：第一轮工具调用 + 第二轮普通对话。
func TestTwoRoundsWithTool(t *testing.T) {
	manager := agent.NewAgent()
	manager.AddTools(&fakeTool{})
	manager.RegisterChat("fake", &fakeProvider{}, true)

	client, err := manager.GetClient("session-1", 0)
	if err != nil {
		t.Fatal(err)
	}

	// ── 第一轮：触发 tool_use → executeTools → tool_result → 第二轮 LLM → done ──
	if err := client.SendText("请使用 fake_tool 工具"); err != nil {
		t.Fatal(err)
	}
	waitForDone(t, client, "round-1(tool)")

	// ── 第三轮（同会话第二次用户消息）：纯文本 end_turn ──
	if err := client.SendText("再来一轮普通对话"); err != nil {
		t.Fatal(err)
	}
	waitForDone(t, client, "round-2(plain)")

	fmt.Println("两轮对话均正常收到 done")
}

// ── CommandTool 中文输出编码测试（Windows）──

// runCommand 用 CommandTool 执行命令并返回输出文本（工具专用 BlockStream 收集）。
func runCommand(t *testing.T, cmd string) string {
	t.Helper()
	tool := tools.NewCommandTool()
	writer := agent.NewBlockStream(nil)
	// CommandTool.Execute 仅使用 turn.Args()，用独立 Turn 即可
	err := tool.Execute(agent.NewTurn(map[string]any{"command": cmd}), writer)
	if err != nil {
		t.Fatalf("执行命令 %q 失败: %v", cmd, err)
	}
	writer.Close()
	var sb strings.Builder
	blocks, _ := writer.ReadBlocks()
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// TestCommandChineseOutput 验证中文命令输出不乱码（chcp 65001 + GBK 兜底解码）。
func TestCommandChineseOutput(t *testing.T) {
	if os.Getenv("OS") != "Windows_NT" {
		t.Skip("仅 Windows 平台验证")
	}

	// 1. cmd 内建命令的中文输出（chcp 生效路径）
	out := runCommand(t, "echo 中文输出测试")
	if !strings.Contains(out, "中文输出测试") {
		t.Errorf("echo 中文输出乱码: %q", out)
	}

	// 2. systeminfo 本地化资源输出（可能走 GBK 兜底解码路径）
	out = runCommand(t, "systeminfo")
	if !strings.Contains(out, "系统") && !strings.Contains(out, "OS") {
		t.Errorf("systeminfo 输出疑似乱码，前 200 字符: %q", truncate(out, 200))
	}
	t.Logf("systeminfo 输出片段:\n%s", truncate(out, 300))

	// 3. 管道过滤 + 带引号关键字（验证引号不被命令行转义损坏）
	out = runCommand(t, `systeminfo | findstr /i "version"`)
	if !strings.Contains(out, "Version") {
		t.Errorf("管道过滤输出异常: %q", truncate(out, 200))
	}
	t.Logf("findstr 输出片段:\n%s", truncate(out, 200))

	// 4. 强制 GBK 输出（chcp 936 覆盖），由 decodeOutput 兜底解码为中文
	out = runCommand(t, `cmd /c "chcp 936>nul && systeminfo | findstr /i build"`)
	if !strings.Contains(out, "Build") || !strings.Contains(out, "版本") {
		t.Errorf("GBK 兜底解码失败: %q", truncate(out, 200))
	}
	t.Logf("GBK 兜底输出片段:\n%s", truncate(out, 200))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
