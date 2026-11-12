//go:build windows

package driver

import (
	"os/exec"
	"syscall"
)

// createNoWindow CREATE_NO_WINDOW：以服务/无控制台方式运行时避免弹出控制台窗口
const createNoWindow = 0x08000000

// hideCommandWindow 让子进程在后台运行且不弹窗
func hideCommandWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
