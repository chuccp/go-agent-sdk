package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// blockRecorder 记录 BlockStream 推送的 block。
type blockRecorder struct {
	blocks []chat.Block
}

func (r *blockRecorder) SendBlock(block chat.Block) uint64 {
	r.blocks = append(r.blocks, block)
	return 0
}

// ctxReceiver 适配 SessionContext 的 Store 到 BlockReceiver 接口。
type ctxReceiver struct {
	ctx *agent.SessionContext
}

func (r *ctxReceiver) SendBlock(block chat.Block) uint64 {
	return r.ctx.AgentStore().SendBlock(block)
}

// collectText 从 BlockStream 的已组装 blocks 中提取全部文本。
func collectText(w *chat.BlockStream) string {
	var sb strings.Builder
	for _, b := range w.ReadBlocks() {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// readEventsUntilIdle 读取 client 事件直到空闲（ReadEvents 无事件时会阻塞，
// 故在协程中读取并以超时判定流已排空）。
func readEventsUntilIdle(client *agent.Client, idle time.Duration) []*agent.Event {
	var events []*agent.Event
	for {
		ch := make(chan []*agent.Event, 1)
		go func() {
			evts, _ := client.ReadEvents()
			ch <- evts
		}()
		select {
		case evts := <-ch:
			if len(evts) == 0 {
				return events
			}
			events = append(events, evts...)
		case <-time.After(idle):
			return events
		}
	}
}

// TestCommand_StreamingOutput 验证命令输出逐行流式回显：
// 执行过程中实时推送 block，结束后完整输出也被收集进 tool_result。
func TestCommand_StreamingOutput(t *testing.T) {
	rec := &blockRecorder{}
	w := chat.NewBlockStream(rec)
	tool := NewCommandTool()
	tool.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"command": "echo streaming-test"})), chat.NewToolResultBlockStream(w, "cmd"))

	// 实时收到了携带输出的 block
	text := collectText(w)
	if !strings.Contains(text, "streaming-test") {
		t.Errorf("输出未被收集: %q", text)
	}
}

// TestCommand_WithSessionContext 验证有 SessionContext 时工具正常执行：
// 完整输出进入 tool_result blocks，同时 SessionContext 收到事件。
func TestCommand_WithSessionContext(t *testing.T) {
	manager := agent.NewAgent()
	ctx := manager.SessionContext("cmd-s1")
	client := manager.GetOrCreateSession("cmd-s1").CreateClient(context.Background(), 0)
	defer client.Close()

	// 使用 SessionContext 作为 receiver，模拟 runTool 的行为
	w := chat.NewBlockStream(&ctxReceiver{ctx: ctx})
	tool := NewCommandTool()
	tool.Execute(agent.NewTurnWithContext(ctx, value.NewObjectFromMap(map[string]any{"command": "echo event-test"})), chat.NewToolResultBlockStream(w, "cmd"))

	// 收到事件（包含 StartBlock/DeltaBlock 等流式事件）
	events := readEventsUntilIdle(client, 300*time.Millisecond)
	if len(events) == 0 {
		t.Error("未收到任何事件")
	}

	// 完整输出同时进入 tool_result
	text := collectText(w)
	if !strings.Contains(text, "event-test") {
		t.Errorf("输出未被收集: %q", text)
	}
}

// TestCommand_StreamingMultiline 验证多行输出逐行推送且顺序完整。
func TestCommand_StreamingMultiline(t *testing.T) {
	if isWindows() {
		t.Skip("printf 非 Windows cmd 内建命令，仅 POSIX 平台验证")
	}
	rec := &blockRecorder{}
	w := chat.NewBlockStream(rec)
	tool := NewCommandTool()
	// printf 在 sh 与 cmd 下均可用；两行输出应产生 delta blocks
	tool.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"command": "printf \"aaa\\nbbb\\n\""})), chat.NewToolResultBlockStream(w, "cmd"))

	text := collectText(w)
	if !strings.Contains(text, "aaa") || !strings.Contains(text, "bbb") {
		t.Errorf("流式内容不完整: %q", text)
	}
}
