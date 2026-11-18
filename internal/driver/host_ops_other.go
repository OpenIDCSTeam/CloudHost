//go:build !windows

// Package driver 非 Windows 平台的主机运维占位实现。
//
// host_ops_windows.go 只在 GOOS=windows 时参与编译，而 NewHostOps 中
// 对 windowsHostOps 的引用是无条件的；缺少本文件时 Linux/macOS 交叉编译
// 会直接报 “undefined: windowsHostOps / isWindowsRuntime”，
// 导致 install.sh 所依赖的 Linux 产物无法构建。
package driver

import "runtime"

// windowsHostOps 在非 Windows 平台不可用：Hyper-V / VMware Workstation /
// 逍遥模拟器只能在 Windows 控制端管理。这里用别名保证编译通过，
// 实际不会被选中（hostOSFor 走 windows 分支时也会被 isWindowsRuntime 拦截）。
type windowsHostOps = linuxHostOps

// isWindowsRuntime 当前构建目标是否为 Windows
func isWindowsRuntime() bool { return runtime.GOOS == "windows" }
