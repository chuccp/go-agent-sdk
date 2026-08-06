package tools

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// CommandTool 在本地终端执行 shell 命令的工具。
type CommandTool struct{}

// NewCommandTool 创建本地命令执行工具。
func NewCommandTool() agent.ToolExecutor {
	return &CommandTool{}
}

// Init 实现 agent.Filter 接口；本工具无需会话上下文。
func (t *CommandTool) Init(ctx *agent.SessionContext) {}

// HandleTurn 实现 agent.ResponseFilter 接口；本工具不干预轮次，直接透传。
func (t *CommandTool) HandleTurn(chain agent.ResponseFilterChain, turn *agent.Turn) error {
	return chain.Next(turn)
}

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

func (t *CommandTool) Execute(args map[string]any) (string, error) {
	cmd, ok := args["command"].(string)
	if !ok || strings.TrimSpace(cmd) == "" {
		return "", fmt.Errorf("缺少 command 参数")
	}
	cmd = strings.TrimSpace(cmd)

	if err := validateCommand(cmd); err != nil {
		return err.Error(), nil
	}

	// Windows 下对 GUI 程序自动加 start "" 前缀，防止 CombinedOutput 阻塞
	if isWindows() && needsStartPrefix(cmd) {
		cmd = `start "" ` + cmd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var c *exec.Cmd
	if isWindows() {
		c = exec.CommandContext(ctx, "cmd", "/c", cmd)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmd)
	}

	output, err := c.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("命令执行超时（30s）: %s", cmd)
		}
		// 命令执行失败时也返回输出（如 stderr 信息）
		if len(output) > 0 {
			return fmt.Sprintf("命令退出码非零，输出:\n%s\n错误: %v", string(output), err), nil
		}
		return "", fmt.Errorf("命令执行失败: %w", err)
	}

	if len(output) == 0 {
		return "(无输出)", nil
	}
	return string(output), nil
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}
