// Package driver ESXi 主机运维实现。
//
// 与普通 Linux 主机的区别：
//   - server_port 是 vSphere API 的 443，SSH 实际走 22（或 server_addr 中显式端口）
//   - 没有 systemd / iptables / GNU find，进程、服务、防火墙都要走 esxcli
//   - 文件列表用 busybox 的 ls（不支持 find -printf）
package driver

import (
	"encoding/csv"
	"fmt"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// esxiHostOps ESXi 主机运维（复用基类的文件上传下载与删除，覆盖系统类操作）
type esxiHostOps struct {
	*linuxHostOps
}

func newESXiHostOps(cfg *model.HSConfig) *esxiHostOps {
	return &esxiHostOps{linuxHostOps: &linuxHostOps{cfg: cfg, sshPort: esxiSSHPort(cfg)}}
}

// esxiSSHPort 解析 SSH 端口：优先 server_addr 中的显式端口，否则 22
func esxiSSHPort(cfg *model.HSConfig) int {
	if cfg == nil {
		return 22
	}
	addr := stripSSHScheme(strings.TrimSpace(cfg.ServerAddr))
	if host, portStr, err := net.SplitHostPort(addr); err == nil && host != "" {
		if p, convErr := strconv.Atoi(portStr); convErr == nil && p > 0 {
			return p
		}
	}
	return 22
}

// SSHUnreachableError 判断错误是否为 SSH 连接层失败
func isSSHUnreachable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, key := range []string{
		"connection refused", "i/o timeout", "handshake failed",
		"connect:", "no route to host", "connection reset",
	} {
		if strings.Contains(msg, key) {
			return true
		}
	}
	return false
}

// run 覆盖基类：把 SSH 连接失败翻译成 ESXi 专属提示
func (o *esxiHostOps) run(cmd string, timeout time.Duration) (string, error) {
	out, err := o.linuxHostOps.run(cmd, timeout)
	if err != nil && isSSHUnreachable(err) {
		return out, o.sshUnavailableError(err)
	}
	return out, err
}

func (o *esxiHostOps) sshUnavailableError(err error) error {
	addr := stripSSHScheme(strings.TrimSpace(o.cfg.ServerAddr))
	host := addr
	if h, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
		host = h
	}
	return fmt.Errorf(
		"无法通过 SSH 连接 ESXi（%s:%d）：ESXi 默认未开启 SSH，请在 vSphere/DCUI 的「故障排除选项 → 启用 SSH」中开启后重试（%v）",
		host, o.sshPort, err)
}

// parseCSVTable 解析 esxcli --formatter=csv 输出（首行为表头）。
//
// 用标准库 csv 解析：CommandLine 等字段可能含逗号，按逗号硬切会错位。
func parseCSVTable(out string) []map[string]string {
	reader := csv.NewReader(strings.NewReader(strings.ReplaceAll(out, "\r", "")))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return nil
	}
	headers := records[0]
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		if len(record) == 0 {
			continue
		}
		row := make(map[string]string, len(headers))
		for i, header := range headers {
			if header == "" {
				continue
			}
			if i < len(record) {
				row[header] = strings.TrimSpace(record[i])
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// ListProcesses ESXi 进程列表。
//
// esxcli 的 CSV 列是 Id / Name / CommandLine / WorldState，
// 与 Linux 的 ps 字段完全不同，且不提供 CPU、内存百分比。
func (o *esxiHostOps) ListProcesses() ([]HostProcessItem, error) {
	out, err := o.run("esxcli --formatter=csv system process list", 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取进程列表失败: %w", err)
	}
	items := make([]HostProcessItem, 0, 128)
	for _, row := range parseCSVTable(out) {
		pid, convErr := strconv.Atoi(strings.TrimSpace(row["Id"]))
		if convErr != nil || pid <= 0 {
			continue
		}
		name := strings.TrimSpace(row["Name"])
		command := strings.TrimSpace(row["CommandLine"])
		if command == "" {
			command = name
		}
		state := strings.TrimSpace(row["WorldState"])
		if state == "" {
			state = "-"
		}
		items = append(items, HostProcessItem{
			PID:     pid,
			User:    "-",
			CPUDesc: "-",
			MemDesc: "-",
			State:   state,
			Command: command,
		})
		if len(items) >= 300 {
			break
		}
	}
	return items, nil
}

// ListServices ESXi 服务列表。
//
// ESXi 7.x 的 esxcli 没有 system service 命名空间，服务靠 chkconfig 管理开机自启、
// /etc/init.d/<name> 控制运行状态。这里一次性在远端跑循环，避免逐服务往返。
func (o *esxiHostOps) ListServices() ([]HostServiceItem, error) {
	// 不能用 timeout 包装：ESXi 的 /bin/timeout 不接受 "timeout <秒> <命令>" 写法，
	// status 脚本本身都很快返回，直接执行即可。
	script := `chkconfig --list 2>/dev/null | while read name state; do ` +
		`st=stopped; /etc/init.d/$name status >/dev/null 2>&1 && st=running; ` +
		`echo "$name|$st|$state"; done`
	out, err := o.run(script, 90*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取服务列表失败: %w", err)
	}
	items := make([]HostServiceItem, 0, 48)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		active, sub := "inactive", "stopped"
		if len(parts) > 1 && strings.EqualFold(strings.TrimSpace(parts[1]), "running") {
			active, sub = "active", "running"
		}
		load := "Disabled"
		if len(parts) > 2 && strings.EqualFold(strings.TrimSpace(parts[2]), "on") {
			load = "Automatic"
		}
		items = append(items, HostServiceItem{
			Name:   name,
			Load:   load,
			Active: active,
			Sub:    sub,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// ControlService ESXi 服务启停（/etc/init.d/<name>）
func (o *esxiHostOps) ControlService(name, action string) error {
	if err := hostServiceName(name); err != nil {
		return err
	}
	var verb string
	switch action {
	case "start":
		verb = "start"
	case "stop":
		verb = "stop"
	case "restart":
		verb = "restart"
	default:
		return fmt.Errorf("不支持的操作: %s", action)
	}
	if _, err := o.run(fmt.Sprintf("/etc/init.d/%s %s", name, verb), 90*time.Second); err != nil {
		return fmt.Errorf("服务操作失败: %w", err)
	}
	return nil
}

// ListFirewallRules ESXi 防火墙按规则集管理，这里展示规则集的启用状态
func (o *esxiHostOps) ListFirewallRules() ([]HostFirewallRule, error) {
	out, err := o.run("esxcli --formatter=csv network firewall ruleset list", 40*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取防火墙规则集失败: %w", err)
	}
	rules := make([]HostFirewallRule, 0, 32)
	for _, row := range parseCSVTable(out) {
		name := strings.TrimSpace(row["Name"])
		if name == "" {
			continue
		}
		enabled := strings.EqualFold(strings.TrimSpace(row["Enabled"]), "true")
		target := "DROP"
		if enabled {
			target = "ACCEPT"
		}
		rules = append(rules, HostFirewallRule{
			ID:       name,
			Target:   target,
			Protocol: "-",
			Source:   "-",
			Port:     "-",
			Comment:  "规则集（ruleset）",
		})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	return rules, nil
}

// AddFirewallRule ESXi 不支持逐条规则，需在规则集层面调整
func (o *esxiHostOps) AddFirewallRule(rule HostFirewallRule) error {
	return fmt.Errorf("ESXi 防火墙按规则集（ruleset）管理，请在 vSphere/DCUI 中调整，" +
		"或执行：esxcli network firewall ruleset allowedip add --ruleset-id=<规则集> --ip-address=<地址>")
}

// DeleteFirewallRule ESXi 不支持逐条删除，只能关闭整个规则集
func (o *esxiHostOps) DeleteFirewallRule(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("规则标识不合法")
	}
	return fmt.Errorf("ESXi 不支持逐条删除防火墙规则；如需关闭规则集，请执行："+
		"esxcli network firewall ruleset set --enabled=false --ruleset-id=%s", id)
}

// ListHostDir ESXi 使用 busybox ls（不支持 find -printf）
func (o *esxiHostOps) ListHostDir(dir string) (string, []HostFileItem, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return "", nil, err
	}
	clean := path.Clean(dir)
	// -L 解引用符号链接：/vmfs/volumes 下的数据存储都是指向 UUID 目录的链接，
	// 不解引用会被当成普通文件，界面上无法点进去。
	out, err := o.run(fmt.Sprintf("ls -lanL %s", shellQuote(clean)), 30*time.Second)
	if err != nil {
		return clean, nil, fmt.Errorf("读取目录失败: %w", err)
	}
	return clean, parseBusyboxLS(out), nil
}

// parseBusyboxLS 解析 busybox `ls -lan` 输出。
//
// 形如：
//
//	drwxr-xr-x    1 0        0             512 Nov 13 12:00 .
//	-rw-r--r--    1 0        0            1234 Nov 13 12:00 vm.qcow2
//
// 文件名可能含空格，按固定 8 个前置字段切分后合并剩余部分。
func parseBusyboxLS(out string) []HostFileItem {
	items := make([]HostFileItem, 0, 32)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		mode := fields[0]
		if mode == "" {
			continue
		}
		isDir := strings.HasPrefix(mode, "d")
		if !isDir && !strings.HasPrefix(mode, "-") && !strings.HasPrefix(mode, "l") {
			continue
		}
		name := strings.Join(fields[8:], " ")
		// 符号链接显示为 "name -> target"，只保留名称本身
		if idx := strings.Index(name, " -> "); idx > 0 {
			name = strings.TrimSpace(name[:idx])
		}
		if name == "" || name == "." || name == ".." {
			continue
		}
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		items = append(items, HostFileItem{
			Name:  name,
			IsDir: isDir,
			Size:  size,
			Mode:  mode,
			Owner: fields[2],
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	return items
}
