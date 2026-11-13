package driver

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

func isWindowsRuntime() bool { return runtime.GOOS == "windows" }

// psQuote 生成 PowerShell 单引号字面量
func psQuote(v string) string {
	return "'" + psEscape(v) + "'"
}

// windowsHostOps Windows 主机运维（Hyper-V / VMware Workstation / 逍遥模拟器）
type windowsHostOps struct {
	cfg *model.HSConfig
}

func (o *windowsHostOps) HostOS() string { return "windows" }

// ps 执行 PowerShell 脚本（统一 UTF-8 输出，避免中文描述乱码）
func (o *windowsHostOps) ps(script string, timeout time.Duration) (string, error) {
	full := "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + script
	out, stderr, err := security.SafeExecWithTimeout(
		[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", full}, timeout)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s", msg)
	}
	return out, nil
}

func (o *windowsHostOps) ListProcesses() ([]HostProcessItem, error) {
	out, err := o.ps("Get-Process | Sort-Object CPU -Descending | Select-Object -First 120 Id,ProcessName,CPU,WorkingSet64 | ConvertTo-Json -Compress", 40*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取进程列表失败: %w", err)
	}
	raw, parseErr := parsePowerShellJSONList(strings.TrimSpace(out))
	if parseErr != nil {
		return nil, fmt.Errorf("解析进程列表失败: %w", parseErr)
	}
	items := make([]HostProcessItem, 0, len(raw))
	for _, row := range raw {
		pid := int(numericValue(row["Id"]))
		if pid <= 0 {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(row["ProcessName"]))
		cpuSec := numericValue(row["CPU"])
		memBytes := numericValue(row["WorkingSet64"])
		items = append(items, HostProcessItem{
			PID:     pid,
			User:    "-",
			CPU:     cpuSec,
			CPUDesc: fmt.Sprintf("%.1fs", cpuSec),
			Mem:     memBytes / 1024 / 1024,
			MemDesc: formatStorageSize(int64(memBytes)),
			State:   "运行",
			Command: name,
		})
	}
	return items, nil
}

func (o *windowsHostOps) KillProcess(pid int) error {
	if pid <= 4 {
		return fmt.Errorf("不允许结束该进程")
	}
	if _, err := o.ps(fmt.Sprintf("Stop-Process -Id %d -Force", pid), 20*time.Second); err != nil {
		return fmt.Errorf("结束进程 %d 失败: %w", pid, err)
	}
	return nil
}

func (o *windowsHostOps) ListServices() ([]HostServiceItem, error) {
	out, err := o.ps("Get-Service | Sort-Object Name | Select-Object Name,Status,DisplayName,StartType | ConvertTo-Json -Compress", 40*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取服务列表失败: %w", err)
	}
	raw, parseErr := parsePowerShellJSONList(strings.TrimSpace(out))
	if parseErr != nil {
		return nil, fmt.Errorf("解析服务列表失败: %w", parseErr)
	}
	items := make([]HostServiceItem, 0, len(raw))
	for _, row := range raw {
		name := strings.TrimSpace(fmt.Sprint(row["Name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		status := strings.TrimSpace(fmt.Sprint(row["Status"]))
		load := hvStartTypeLabel(row["StartType"])
		active, sub := "inactive", status
		if strings.EqualFold(status, "Running") {
			active = "active"
			sub = "running"
		}
		items = append(items, HostServiceItem{
			Name:        name,
			Load:        load,
			Active:      active,
			Sub:         sub,
			Description: strings.TrimSpace(fmt.Sprint(row["DisplayName"])),
		})
	}
	return items, nil
}

func (o *windowsHostOps) ControlService(name, action string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("服务名不合法")
	}
	var script string
	switch action {
	case "start":
		script = fmt.Sprintf("Start-Service -Name %s", psQuote(name))
	case "stop":
		script = fmt.Sprintf("Stop-Service -Name %s -Force", psQuote(name))
	case "restart":
		script = fmt.Sprintf("Restart-Service -Name %s -Force", psQuote(name))
	default:
		return fmt.Errorf("不支持的操作: %s", action)
	}
	if _, err := o.ps(script, 90*time.Second); err != nil {
		return fmt.Errorf("服务操作失败: %w", err)
	}
	return nil
}

func (o *windowsHostOps) ListFirewallRules() ([]HostFirewallRule, error) {
	out, err := o.ps("Get-NetFirewallRule -Direction Inbound | Select-Object -First 200 Name,DisplayName,Action,Enabled,Profile | ConvertTo-Json -Compress", 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取防火墙规则失败: %w", err)
	}
	raw, parseErr := parsePowerShellJSONList(strings.TrimSpace(out))
	if parseErr != nil {
		return nil, fmt.Errorf("解析防火墙规则失败: %w", parseErr)
	}
	rules := make([]HostFirewallRule, 0, len(raw))
	for _, row := range raw {
		id := strings.TrimSpace(fmt.Sprint(row["Name"]))
		if id == "" || id == "<nil>" {
			continue
		}
		target := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["Action"])))
		if target == "ALLOW" {
			target = "ACCEPT"
		} else if target == "BLOCK" {
			target = "DROP"
		}
		profile := strings.TrimSpace(fmt.Sprint(row["Profile"]))
		rules = append(rules, HostFirewallRule{
			ID:       id,
			Target:   target,
			Protocol: "-",
			Source:   profile,
			Port:     "-",
			Comment:  strings.TrimSpace(fmt.Sprint(row["DisplayName"])),
		})
	}
	return rules, nil
}

func (o *windowsHostOps) AddFirewallRule(rule HostFirewallRule) error {
	proto := strings.ToUpper(strings.TrimSpace(rule.Protocol))
	switch proto {
	case "TCP", "UDP":
	case "ICMP", "ICMPV4", "ICMPV6":
		proto = "ICMPv4"
	default:
		return fmt.Errorf("Windows 防火墙需指定 TCP / UDP / ICMP 协议")
	}
	target := strings.ToUpper(strings.TrimSpace(rule.Target))
	action := "Allow"
	if target == "DROP" || target == "REJECT" {
		action = "Block"
	}
	comment := strings.TrimSpace(rule.Comment)
	if comment == "" {
		comment = fmt.Sprintf("openidcs %s/%s", proto, strings.TrimSpace(rule.Port))
	}

	parts := []string{
		"New-NetFirewallRule",
		"-DisplayName", psQuote(comment),
		"-Direction", "Inbound",
		"-Action", action,
		"-Protocol", proto,
	}
	if port := strings.TrimSpace(rule.Port); port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("端口必须是数字: %s", port)
		}
		parts = append(parts, "-LocalPort", port)
	}
	if source := strings.TrimSpace(rule.Source); source != "" && source != "0.0.0.0/0" && !strings.EqualFold(source, "-") {
		parts = append(parts, "-RemoteAddress", psQuote(source))
	}
	if _, err := o.ps(strings.Join(parts, " "), 60*time.Second); err != nil {
		return fmt.Errorf("添加防火墙规则失败: %w", err)
	}
	return nil
}

func (o *windowsHostOps) DeleteFirewallRule(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return fmt.Errorf("规则标识不合法")
	}
	if _, err := o.ps(fmt.Sprintf("Remove-NetFirewallRule -Name %s", psQuote(id)), 60*time.Second); err != nil {
		return fmt.Errorf("删除防火墙规则失败: %w", err)
	}
	return nil
}

func (o *windowsHostOps) ListHostDir(dir string) (string, []HostFileItem, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return "", nil, err
	}
	clean := filepath.Clean(dir)
	entries, err := os.ReadDir(clean)
	if err != nil {
		return clean, nil, fmt.Errorf("读取目录失败: %w", err)
	}
	items := make([]HostFileItem, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		items = append(items, HostFileItem{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			Owner:   "-",
			ModTime: info.ModTime().Unix(),
		})
	}
	return clean, items, nil
}

func (o *windowsHostOps) CreateHostDir(dir, name string) error {
	if err := validateHostOpsPath(dir); err != nil {
		return err
	}
	if err := hostFileName(name); err != nil {
		return err
	}
	target := filepath.Join(filepath.Clean(dir), name)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	return nil
}

func (o *windowsHostOps) RenameHostPath(dir, oldName, newName string) error {
	if err := validateHostOpsPath(dir); err != nil {
		return err
	}
	if err := hostFileName(oldName); err != nil {
		return err
	}
	if err := hostFileName(newName); err != nil {
		return err
	}
	base := filepath.Clean(dir)
	if err := os.Rename(filepath.Join(base, oldName), filepath.Join(base, newName)); err != nil {
		return fmt.Errorf("重命名失败: %w", err)
	}
	return nil
}

func (o *windowsHostOps) DeleteHostPath(dir, name string) error {
	if err := validateHostOpsPath(dir); err != nil {
		return err
	}
	if err := hostFileName(name); err != nil {
		return err
	}
	target := filepath.Join(filepath.Clean(dir), name)
	if isRootHostPath(target) {
		return fmt.Errorf("不允许删除根目录")
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}

func (o *windowsHostOps) UploadHostFile(dir, name string, r io.Reader) (int64, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return 0, err
	}
	if err := hostFileName(name); err != nil {
		return 0, err
	}
	base := filepath.Clean(dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return 0, fmt.Errorf("创建目录失败: %w", err)
	}
	tmp := filepath.Join(base, name+".uploading")
	f, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("创建文件失败: %w", err)
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("写入文件失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("写入文件失败: %w", closeErr)
	}
	if err := os.Rename(tmp, filepath.Join(base, name)); err != nil {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("保存文件失败: %w", err)
	}
	return n, nil
}

func (o *windowsHostOps) DownloadHostFile(dir, name string) (io.ReadCloser, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return nil, err
	}
	if err := hostFileName(name); err != nil {
		return nil, err
	}
	target := filepath.Join(filepath.Clean(dir), name)
	f, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("目标是目录，无法下载")
	}
	return f, nil
}

// hvStartTypeLabel 服务启动类型：枚举在 JSON 中可能是数字（2=Automatic）
func hvStartTypeLabel(value interface{}) string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return v
		}
	case float64:
		switch int(v) {
		case 0:
			return "Boot"
		case 1:
			return "System"
		case 2:
			return "Automatic"
		case 3:
			return "Manual"
		case 4:
			return "Disabled"
		}
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return "-"
	}
	return text
}

// numericValue 从 JSON 数值（可能为 null）取值
func numericValue(v interface{}) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case json.Number:
		f, _ := value.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(value, 64)
		return f
	}
	return 0
}
