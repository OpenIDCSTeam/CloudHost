// Package version 保存 opidcs 版本信息。
package version

// Version 由发布流水线通过 -ldflags 注入。
var Version = "dev"

// Commit 构建提交（可选）。
var Commit = ""

// String 返回可读版本。
func String() string {
	if Commit != "" {
		return Version + " (" + Commit + ")"
	}
	return Version
}
