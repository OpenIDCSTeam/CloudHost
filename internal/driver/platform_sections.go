// Package driver 主机平台配置信息（按平台展示不同的可查看项目）。
//
// 「管理主机」面板除了镜像文件，还需要按平台展示各自的配置项：
// PVE 的节点网络与存储、QEMU 的网桥与 libvirt 网络、Hyper-V 的虚拟交换机……
// 各驱动按需实现 PlatformInfoProvider，未实现的平台前端不展示该页签。
package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// HostPlatformSection 平台配置分组
type HostPlatformSection struct {
	Key     string     `json:"key"`
	Title   string     `json:"title"`
	Hint    string     `json:"hint"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Error 该分组的读取错误（其他分组仍可正常展示）
	Error string `json:"error,omitempty"`
}

// PlatformInfoProvider 可选接口：按主机平台返回配置项目
type PlatformInfoProvider interface {
	PlatformSections() []HostPlatformSection
}

// formatStorageSize 把字节数格式化为人类可读容量
func formatStorageSize(bytes int64) string {
	if bytes <= 0 {
		return "-"
	}
	const unit = 1024
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value := float64(bytes)
	index := 0
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%.0f %s", value, units[index])
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

// ============================================================
// QEMU / KVM
// ============================================================

func (d *QEMUDriver) PlatformSections() []HostPlatformSection {
	return []HostPlatformSection{
		qemuBridgeSection(d.Config),
		qemuLibvirtNetworkSection(d.Config),
	}
}

// qemuBridgeSection 列出宿主机 Linux 网桥（即 QEMU 可用的虚拟交换机）
func qemuBridgeSection(cfg *model.HSConfig) HostPlatformSection {
	section := HostPlatformSection{
		Key:     "bridge",
		Title:   "虚拟交换机 / 网桥",
		Hint:    "宿主机 Linux 网桥；创建虚拟机时作为桥接网络目标",
		Columns: []string{"网桥", "接入端口"},
		Rows:    [][]string{},
	}
	// 直接读 sysfs，不依赖 iproute2 / brctl 是否安装
	script := `for i in /sys/class/net/*; do n=${i##*/}; if [ -d "$i/bridge" ]; then p=$(ls "$i/brif" 2>/dev/null | tr '\n' ' '); echo "$n|$p"; fi; done`
	out, err := newHostCommandRunner(cfg).runShell(script, 30*time.Second)
	if err != nil {
		section.Error = "读取网桥失败: " + err.Error()
		return section
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		ports := "-"
		if len(parts) == 2 {
			if v := strings.TrimSpace(parts[1]); v != "" {
				ports = v
			}
		}
		section.Rows = append(section.Rows, []string{name, ports})
	}
	return section
}

// qemuLibvirtNetworkSection 列出 libvirt 虚拟网络
func qemuLibvirtNetworkSection(cfg *model.HSConfig) HostPlatformSection {
	section := HostPlatformSection{
		Key:     "libvirt-net",
		Title:   "libvirt 虚拟网络",
		Hint:    "virsh net-list --all",
		Columns: []string{"名称", "状态", "自动启动", "持久化"},
		Rows:    [][]string{},
	}
	out, err := newHostCommandRunner(cfg).runShell("virsh net-list --all", 30*time.Second)
	if err != nil {
		section.Error = "读取 libvirt 网络失败: " + err.Error()
		return section
	}
	for idx, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// 前两行是表头与分隔线
		if idx < 2 || line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		auto, persist := "-", "-"
		if len(fields) > 2 {
			auto = fields[2]
		}
		if len(fields) > 3 {
			persist = fields[3]
		}
		section.Rows = append(section.Rows, []string{fields[0], fields[1], auto, persist})
	}
	return section
}

// ============================================================
// Proxmox VE
// ============================================================

func (d *ProxmoxDriver) PlatformSections() []HostPlatformSection {
	return []HostPlatformSection{
		d.pveNetworkSection(),
		d.pveStorageSection(),
	}
}

// pveNetworkSection 列出 PVE 节点网络接口（vmbr 即虚拟交换机）
func (d *ProxmoxDriver) pveNetworkSection() HostPlatformSection {
	section := HostPlatformSection{
		Key:     "network",
		Title:   "虚拟交换机 / 网桥",
		Hint:    "PVE 节点网络接口，类型为 bridge 的接口即虚拟交换机（vmbr）",
		Columns: []string{"接口", "类型", "地址", "状态", "备注"},
		Rows:    [][]string{},
	}
	client, err := d.apiConn()
	if err != nil {
		section.Error = err.Error()
		return section
	}
	ctx, cancel := context.WithTimeout(d.Ctx(""), 20*time.Second)
	defer cancel()

	var items []struct {
		Iface       string `json:"iface"`
		Type        string `json:"type"`
		Address     string `json:"address"`
		CIDR        string `json:"cidr"`
		Active      int    `json:"active"`
		Comments    string `json:"comments"`
		BridgePorts string `json:"bridge_ports"`
	}
	if err := client.Get(ctx, fmt.Sprintf("/nodes/%s/network", d.nodeName()), &items); err != nil {
		section.Error = "读取节点网络失败: " + err.Error()
		return section
	}
	for _, it := range items {
		addr := strings.TrimSpace(it.Address)
		if addr == "" {
			addr = strings.TrimSpace(it.CIDR)
		}
		if addr == "" {
			addr = "-"
		}
		state := "未启用"
		if it.Active == 1 {
			state = "已启用"
		}
		note := strings.TrimSpace(strings.Join([]string{it.Comments, it.BridgePorts}, " "))
		if note == "" {
			note = "-"
		}
		section.Rows = append(section.Rows, []string{it.Iface, it.Type, addr, state, note})
	}
	return section
}

// pveStorageSection 列出 PVE 存储池
func (d *ProxmoxDriver) pveStorageSection() HostPlatformSection {
	section := HostPlatformSection{
		Key:     "storage",
		Title:   "存储池",
		Hint:    "PVE 存储，content 表示该存储可用于磁盘、ISO 还是备份",
		Columns: []string{"名称", "类型", "支持内容", "可用 / 总量", "状态"},
		Rows:    [][]string{},
	}
	client, err := d.apiConn()
	if err != nil {
		section.Error = err.Error()
		return section
	}
	ctx, cancel := context.WithTimeout(d.Ctx(""), 20*time.Second)
	defer cancel()

	var items []struct {
		Storage string `json:"storage"`
		Type    string `json:"type"`
		Content string `json:"content"`
		Avail   int64  `json:"avail"`
		Total   int64  `json:"total"`
		Active  *int   `json:"active"`
	}
	if err := client.Get(ctx, fmt.Sprintf("/nodes/%s/storage", d.nodeName()), &items); err != nil {
		section.Error = "读取存储列表失败: " + err.Error()
		return section
	}
	for _, it := range items {
		content := strings.ReplaceAll(strings.TrimSpace(it.Content), ",", " / ")
		if content == "" {
			content = "-"
		}
		capacity := "-"
		if it.Total > 0 {
			capacity = fmt.Sprintf("%s / %s", formatStorageSize(it.Avail), formatStorageSize(it.Total))
		}
		state := "-"
		if it.Active != nil {
			if *it.Active == 1 {
				state = "已启用"
			} else {
				state = "未启用"
			}
		}
		section.Rows = append(section.Rows, []string{it.Storage, it.Type, content, capacity, state})
	}
	return section
}

// ============================================================
// Hyper-V
// ============================================================

func (d *HyperVDriver) PlatformSections() []HostPlatformSection {
	return []HostPlatformSection{hyperVSwitchSection()}
}

// hyperVSwitchSection 列出 Hyper-V 虚拟交换机
func hyperVSwitchSection() HostPlatformSection {
	section := HostPlatformSection{
		Key:     "vswitch",
		Title:   "虚拟交换机",
		Hint:    "Hyper-V 虚拟交换机（Get-VMSwitch），虚拟机网络与宿主机共享设置",
		Columns: []string{"名称", "类型", "绑定网卡", "管理操作系统共享"},
		Rows:    [][]string{},
	}
	out, _, err := security.SafeExecWithTimeout([]string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-VMSwitch | Select-Object Name,SwitchType,NetAdapterInterfaceDescription,AllowManagementOS | ConvertTo-Json -Compress"},
		30*time.Second)
	if err != nil {
		section.Error = "读取虚拟交换机失败: " + err.Error()
		return section
	}
	items, parseErr := parsePowerShellJSONList(strings.TrimSpace(out))
	if parseErr != nil {
		section.Error = "解析虚拟交换机列表失败: " + parseErr.Error()
		return section
	}
	for _, item := range items {
		name := strings.TrimSpace(fmt.Sprint(item["Name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		adapter := strings.TrimSpace(fmt.Sprint(item["NetAdapterInterfaceDescription"]))
		if adapter == "" || adapter == "<nil>" {
			adapter = "-"
		}
		section.Rows = append(section.Rows, []string{
			name,
			hvSwitchTypeLabel(item["SwitchType"]),
			adapter,
			yesNoLabel(item["AllowManagementOS"]),
		})
	}
	return section
}

// parsePowerShellJSONList 解析 ConvertTo-Json 输出：单个对象或数组
func parsePowerShellJSONList(out string) ([]map[string]interface{}, error) {
	if out == "" || out == "null" {
		return nil, nil
	}
	var list []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &list); err == nil {
		return list, nil
	}
	var single map[string]interface{}
	if err := json.Unmarshal([]byte(out), &single); err != nil {
		return nil, err
	}
	return []map[string]interface{}{single}, nil
}

// hvSwitchTypeLabel Hyper-V 交换机类型（枚举在不同 PowerShell 版本下可能是数字或字符串）
func hvSwitchTypeLabel(value interface{}) string {
	switch v := value.(type) {
	case float64:
		switch int(v) {
		case 1:
			return "外部"
		case 2:
			return "内部"
		case 3:
			return "专用"
		}
	case string:
		switch strings.ToLower(v) {
		case "external":
			return "外部"
		case "internal":
			return "内部"
		case "private":
			return "专用"
		}
		return v
	}
	label := fmt.Sprint(value)
	if label == "" || label == "<nil>" {
		return "-"
	}
	return label
}

// yesNoLabel 把布尔值转成「是 / 否」
func yesNoLabel(value interface{}) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "是"
		}
		return "否"
	case string:
		if strings.EqualFold(v, "true") {
			return "是"
		}
		if strings.EqualFold(v, "false") {
			return "否"
		}
	}
	return "-"
}
