package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// CommandTool 在本地终端执行 shell 命令的工具。
type CommandTool struct{}

// NewCommandTool 创建本地命令执行工具。
func NewCommandTool() agent.ToolExecutor {
	return &CommandTool{}
}

// Name 返回工具名称。
func (t *CommandTool) Name() string { return t.Definition().Name }

// UsagePrompt 实现 ToolExecutor 接口，返回空字符串（本工具无引导提示词）。
func (t *CommandTool) UsagePrompt() string { return "" }

func (t *CommandTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name:        "execute_command",
		Description: "在本地终端执行命令并返回输出。可用于：查看文件、列出目录、运行脚本、打开应用程序等。打开 GUI 程序（浏览器、记事本等）时必须使用 start 命令，例如：start \"\" chrome、start \"\" notepad。命令有 30 秒超时限制。禁止执行破坏性命令（如 rm -rf、mkfs、shutdown 等）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "要执行的命令。例如：dir、type file.txt、go version、start \"\" chrome、start \"\" notepad、start \"\" calc",
				},
			},
			"required": []string{"command"},
		},
	}
}

// dangerousPatterns 匹配危险的命令模式。
var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+(-[rRf]+\s+)*[/~]`),     // rm -rf / 或 rm -rf ~
	regexp.MustCompile(`\bmkfs\b`),                     // 格式化文件系统
	regexp.MustCompile(`\b(mkswap|swapon|swapoff)\b`),  // swap 操作
	regexp.MustCompile(`\bshutdown\b`),                 // 关机
	regexp.MustCompile(`\breboot\b`),                   // 重启
	regexp.MustCompile(`\bdd\s+if=`),                   // dd 磁盘写入
	regexp.MustCompile(`\bchmod\s+(-R\s+)?777\s+[/~]`), // chmod 777 / 或 ~
	regexp.MustCompile(`:\(\)\s*\{`),                   // fork 炸弹
	regexp.MustCompile(`>\s*/dev/(sd|hd|nvme|mmcblk)`), // 写入块设备
	regexp.MustCompile(`\bfdisk\b`),                    // 磁盘分区
	regexp.MustCompile(`\bparted\b`),                   // 磁盘分区
}

// validateCommand 检查命令是否包含危险操作。
func validateCommand(cmd string) error {
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(cmd) {
			return fmt.Errorf("命令被安全策略拒绝（匹配危险模式: %s）", pattern.String())
		}
	}
	return nil
}

// guiApps 列出常见的 Windows GUI 程序名（小写），用于自动添加 start "" 前缀。
var guiApps = map[string]bool{
	"chrome": true, "msedge": true, "firefox": true, "iexplore": true,
	"notepad": true, "notepad++": true, "calc": true, "explorer": true,
	"winword": true, "excel": true, "powerpnt": true,
	"code": true, "devenv": true, "paint": true, "mspaint": true,
}

// needsStartPrefix 判断命令是否是直接启动 GUI 程序（没有 start 前缀）。
func needsStartPrefix(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	// 已经有 start 前缀则不需要
	if strings.HasPrefix(lower, "start") {
		return false
	}
	// 提取第一个 token（程序名）
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return false
	}
	prog := fields[0]
	// 去掉路径和扩展名，只保留程序名
	if idx := strings.LastIndexAny(prog, "/\\"); idx >= 0 {
		prog = prog[idx+1:]
	}
	prog = strings.TrimSuffix(prog, ".exe")
	return guiApps[prog]
}

// Execute 实现 agent.ToolExecutor 接口：在本地终端执行命令，输出逐行流式写入 writer
// （WriteEvent 实时回显 chunk 事件，兼容长耗时命令不是一次性出结果的场景）；
// 执行错误直接写入 writer，不再向外返回。
func (t *CommandTool) Execute(turn *agent.Turn, writer *agent.BlockStream) {
	args := turn.Args()
	cmd := args.GetString("command")
	if strings.TrimSpace(cmd) == "" {
		writer.WriteBlock(chat.NewTextBlock("缺少 command 参数"))
		return
	}
	cmd = strings.TrimSpace(cmd)

	if err := validateCommand(cmd); err != nil {
		writer.WriteBlock(chat.NewTextBlock(err.Error()))
		return
	}

	// Windows 下对 GUI 程序自动加 start "" 前缀，防止阻塞
	if isWindows() && needsStartPrefix(cmd) {
		cmd = `start "" ` + cmd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newShellCommand(ctx, cmd)
	stdout, err := c.StdoutPipe()
	if err != nil {
		writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("命令执行失败: %v", err)))
		return
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("命令执行失败: %v", err)))
		return
	}
	if err := c.Start(); err != nil {
		writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("命令执行失败: %v", err)))
		return
	}

	// 流式排空 stdout/stderr：逐行实时推送（两个协程并发写入，BlockStream 内部已加锁）
	var gotOutput atomic.Bool
	var wg sync.WaitGroup
	streamLines := func(rd io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(rd)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := string(decodeOutput(scanner.Bytes()))
			writer.WriteEvent(line + "\n")
			gotOutput.Store(true)
		}
	}
	wg.Add(2)
	go streamLines(stdout)
	go streamLines(stderr)
	wg.Wait()

	err = c.Wait()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("命令执行超时（30s）: %s", cmd)))
			return
		}
		// 命令执行失败：已流式写入的输出保留，补充错误说明
		if gotOutput.Load() {
			writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("命令退出码非零，错误: %v", err)))
			return
		}
		writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("命令执行失败: %v", err)))
		return
	}

	if !gotOutput.Load() {
		writer.WriteBlock(chat.NewTextBlock("(无输出)"))
	}
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}

// decodeOutput 兜底处理非 UTF-8 输出：部分程序直接按系统 ANSI 代码页
// （简体中文 Windows 为 GBK）输出，chcp 无法影响其已编码的字节流，
// 此时按 GBK 解码为 UTF-8；合法 UTF-8 输出原样返回。
func decodeOutput(output []byte) []byte {
	if len(output) == 0 || utf8.Valid(output) {
		return output
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(output); err == nil {
		return decoded
	}
	return output
}
