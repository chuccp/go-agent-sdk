//go:build !windows

package tools

import (
	"context"
	"os/exec"
)

// newShellCommand 构造非 Windows 平台（sh -c）的命令执行进程。
func newShellCommand(ctx context.Context, cmd string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", cmd)
}
