//go:build windows

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// memuVMGroupSid Hyper-V 虚拟机账户 SID（NT VIRTUAL MACHINE\Virtual Machines）
const memuVMGroupSid = "*S-1-5-83-0"

// memuACLTarget 需要授权的目录
type memuACLTarget struct {
	Path   string
	Kind   string
	Rights string // R 只读 / M 读写
}

// repairRuntime 执行修复；fix=false 时只检查。
func (d *MemuDriver) repairRuntime(fix bool) *MemuRepairReport {
	report := &MemuRepairReport{Root: memuRootOf(d.Config)}
	report.Firewall = d.ensureFirewallService(fix, report)
	for _, item := range d.ensureACLTargets(fix, report, d.aclTargets()) {
		report.Items = append(report.Items, item)
	}
	return report
}

// ensureVMDiskACL 只修复指定实例相关的目录（创建/启动失败时调用）。
func (d *MemuDriver) ensureVMDiskACL(index string) *MemuRepairReport {
	report := &MemuRepairReport{Root: memuRootOf(d.Config)}

	var targets []memuACLTarget
	seen := map[string]bool{}

	if code := d.instanceAndroidCode(index); code != "" {
		dir := d.imageDir(code)
		if dir != "" && !seen[dir] {
			seen[dir] = true
			targets = append(targets, memuACLTarget{Path: dir, Kind: "基础镜像(只读)", Rights: "R"})
		}
	}
	// 实例目录一定需要读写权限
	if dir := d.instanceDir(index); dir != "" && !seen[dir] {
		seen[dir] = true
		targets = append(targets, memuACLTarget{Path: dir, Kind: "实例磁盘(读写)", Rights: "M"})
	}
	// 实例磁盘尚未生成时（刚 create 完），把全部基础镜像目录补一遍
	if len(targets) == 1 {
		for _, t := range d.aclTargets() {
			if strings.Contains(t.Kind, "基础镜像") && !seen[t.Path] {
				seen[t.Path] = true
				targets = append(targets, t)
			}
		}
	}

	if len(targets) == 0 {
		return report
	}
	for _, item := range d.ensureACLTargets(true, report, targets) {
		report.Items = append(report.Items, item)
	}
	return report
}

// aclTargets 枚举逍遥模拟器的镜像目录与实例目录
func (d *MemuDriver) aclTargets() []memuACLTarget {
	root := memuRootOf(d.Config)
	if root == "" {
		return nil
	}
	var targets []memuACLTarget

	imageRoot := filepath.Join(root, "MEmu", "image")
	if entries, err := os.ReadDir(imageRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(imageRoot, e.Name())
			if matches, _ := filepath.Glob(filepath.Join(dir, "*.vmdk")); len(matches) > 0 {
				targets = append(targets, memuACLTarget{Path: dir, Kind: "基础镜像(只读)", Rights: "R"})
			}
		}
	}

	vmRoot := filepath.Join(root, "MEmu", "MemuHyperv VMs")
	if entries, err := os.ReadDir(vmRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			targets = append(targets, memuACLTarget{
				Path:   filepath.Join(vmRoot, e.Name()),
				Kind:   "实例磁盘(读写)",
				Rights: "M",
			})
		}
	}
	return targets
}

// ensureACLTargets 检查并补齐 ACL
func (d *MemuDriver) ensureACLTargets(fix bool, report *MemuRepairReport, targets []memuACLTarget) []MemuRepairItem {
	items := make([]MemuRepairItem, 0, len(targets))
	for _, t := range targets {
		item := MemuRepairItem{Path: t.Path, Kind: t.Kind, Rights: t.Rights}

		if memuHasVMAce(t.Path) {
			item.Status = "ok"
			items = append(items, item)
			continue
		}

		if !fix {
			item.Status = "warn"
			item.Detail = "缺少 Hyper-V 虚拟机账户权限"
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s 缺少权限：%s", t.Kind, t.Path))
			items = append(items, item)
			continue
		}

		spec := memuVMGroupSid + ":(OI)(CI)" + t.Rights
		_, _, err := security.SafeExecWithTimeout(
			[]string{"icacls.exe", t.Path, "/grant", spec, "/T", "/C", "/Q"}, 60*time.Second)
		if err != nil {
			item.Status = "fail"
			item.Detail = "授权失败: " + err.Error()
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("%s 授权失败（需要管理员权限）：%s", t.Kind, t.Path))
			items = append(items, item)
			continue
		}
		if !memuHasVMAce(t.Path) {
			item.Status = "fail"
			item.Detail = "授权命令已执行但权限未生效（可能被安全软件拦截）"
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("%s 授权未生效：%s", t.Kind, t.Path))
			items = append(items, item)
			continue
		}

		item.Status = "fixed"
		report.Fixed++
		items = append(items, item)
	}
	return items
}

// memuHasVMAce 判断目录 ACL 中是否已包含 Hyper-V 虚拟机账户
func memuHasVMAce(path string) bool {
	out, _, err := security.SafeExecWithTimeout([]string{"icacls.exe", path}, 30*time.Second)
	if err != nil {
		return false
	}
	return strings.Contains(out, "S-1-5-83-0") || strings.Contains(out, "Virtual Machines")
}

// ensureFirewallService 确保 MpsSvc 为自动启动且正在运行。
//
// 使用 PowerShell 的 Service cmdlet（与主机运维面板一致）；
// 不用 sc.exe 是因为部分安全策略会直接拦截 sc.exe 的执行。
func (d *MemuDriver) ensureFirewallService(fix bool, report *MemuRepairReport) string {
	status, _ := runPowerShell("(Get-Service -Name MpsSvc -ErrorAction Stop).Status", 30*time.Second)
	startType, _ := runPowerShell("(Get-Service -Name MpsSvc -ErrorAction Stop).StartType", 30*time.Second)
	running := strings.EqualFold(strings.TrimSpace(status), "Running")
	auto := strings.EqualFold(strings.TrimSpace(startType), "Automatic")

	if running && auto {
		return "MpsSvc 已运行，启动类型为自动"
	}
	if !fix {
		report.Warnings = append(report.Warnings, "MpsSvc 未运行或非自动启动")
		return "MpsSvc 状态异常"
	}

	script := "Set-Service -Name MpsSvc -StartupType Automatic -ErrorAction Stop; " +
		"if ((Get-Service -Name MpsSvc).Status -ne 'Running') { Start-Service -Name MpsSvc -ErrorAction Stop }"
	if _, err := runPowerShell(script, 90*time.Second); err != nil {
		report.Warnings = append(report.Warnings,
			"启动/配置 MpsSvc 失败（需要管理员权限）: "+err.Error())
	}

	status, _ = runPowerShell("(Get-Service -Name MpsSvc -ErrorAction Stop).Status", 30*time.Second)
	if strings.EqualFold(strings.TrimSpace(status), "Running") {
		return "MpsSvc 已启动"
	}
	return "MpsSvc 仍未运行，请重启系统后重试"
}

// runPowerShell 执行一段 PowerShell 脚本并返回标准输出
func runPowerShell(script string, timeout time.Duration) (string, error) {
	out, _, err := security.SafeExecWithTimeout(
		[]string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script},
		timeout)
	if err != nil {
		return out, err
	}
	return strings.TrimSpace(decodeMemucOutput(out)), nil
}
