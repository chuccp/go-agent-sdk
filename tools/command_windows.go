package tools

import (
	"context"
	"os/exec"
	"syscall"
)

// newShellCommand 构造 Windows 下的命令执行进程。
// 通过 SysProcAttr.CmdLine 手工控制命令行：Go 默认会把参数中的双引号
// 转义为 \"（cmd 不认识该转义），含引号的命令（如 findstr "关键字"）
// 会被错误解析；直接传原始命令行可避免。
// chcp 65001 使命令输出按 UTF-8 编码；仍按 GBK 输出的程序由 decodeOutput 兜底。
func newShellCommand(ctx context.Context, cmd string) *exec.Cmd {
	c := exec.CommandContext(ctx, "cmd")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd /c chcp 65001>nul && " + cmd}
	return c
}
