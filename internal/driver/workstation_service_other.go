//go:build !windows

package driver

import "os/exec"

// hideCommandWindow 非 Windows 平台无需隐藏控制台窗口
func hideCommandWindow(cmd *exec.Cmd) {}
