// Package driver VirtualBox 端口映射（NAT 转发规则）。
//
// VirtualBox 自带 NAT 引擎支持端口转发（natpf），这是该平台唯一无需外部
// 路由器即可落地的端口映射方式。因此 VirtualBox 驱动实现 VMPortMapper，
// 由 NATService 把「端口映射」直接下发到虚拟机的 NAT 规则上：
//
//	运行中：VBoxManage controlvm <vm> natpf1 "<name>,tcp,,<wan>,,<lan>"
//	已关机：VBoxManage modifyvm  <vm> --natpf1 "<name>,tcp,,<wan>,,<lan>"
//
// 使用桥接网卡的虚拟机没有 NAT 引擎，端口映射只能由路由器（爱快）完成，
// 此时明确拒绝并给出提示，而不是伪造成功。
package driver

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

const vboxNatRulePrefix = "oidcs-"

// PortsMapVM 端口映射下发（VirtualBox NAT 转发规则）。
func (d *VirtualBoxDriver) PortsMapVM(vmUUID string, nat *model.PortData, add bool) (*model.ZMessage, error) {
	if nat == nil || nat.LanPort <= 0 {
		return &model.ZMessage{Success: false, Action: "PortsMap",
			Message: "端口映射参数不完整（缺少内网端口）"}, nil
	}
	if d.GetPower(vmUUID) == "未知" {
		return &model.ZMessage{Success: false, Action: "PortsMap",
			Message: "虚拟机不存在: " + vmUUID}, nil
	}

	// 仅 NAT 网卡支持平台侧端口映射
	if mode := d.vboxNICMode(vmUUID, 1); mode != "nat" {
		if mode == "" {
			return &model.ZMessage{Success: false, Action: "PortsMap",
				Message: "无法读取虚拟机网卡配置"}, nil
		}
		return &model.ZMessage{Success: false, Action: "PortsMap",
			Message: fmt.Sprintf("该虚拟机使用 %s 网卡，端口映射需在路由器/防火墙上配置", mode)}, nil
	}

	rules := d.vboxNatForwardings(vmUUID)

	if add {
		if nat.WanPort <= 0 {
			port, err := d.vboxAllocateHostPort(rules)
			if err != nil {
				return &model.ZMessage{Success: false, Action: "PortsMap", Message: err.Error()}, nil
			}
			nat.WanPort = port
		} else if rules[nat.WanPort] {
			return &model.ZMessage{Success: false, Action: "PortsMap",
				Message: fmt.Sprintf("外网端口 %d 已被占用", nat.WanPort)}, nil
		}
	} else if nat.WanPort <= 0 {
		return &model.ZMessage{Success: false, Action: "PortsMap",
			Message: "删除端口映射需要指定外网端口"}, nil
	}

	name := vboxNatRuleName(nat.WanPort)
	running := d.GetPower(vmUUID) == "运行中"

	if add {
		spec := fmt.Sprintf("%s,tcp,,%d,,%d", name, nat.WanPort, nat.LanPort)
		var out string
		var err error
		if running {
			out, err = d.execVBox("controlvm", vmUUID, "natpf1", spec)
		} else {
			out, err = d.execVBox("modifyvm", vmUUID, "--natpf1", spec)
		}
		if err != nil {
			return &model.ZMessage{Success: false, Action: "PortsMap",
				Message: fmt.Sprintf("端口映射失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
		return &model.ZMessage{Success: true, Action: "PortsMap",
			Message: fmt.Sprintf("端口 %d 成功映射到虚拟机 %d 端口", nat.WanPort, nat.LanPort)}, nil
	}

	var out string
	var err error
	if running {
		out, err = d.execVBox("controlvm", vmUUID, "natpf1", "delete", name)
	} else {
		out, err = d.execVBox("modifyvm", vmUUID, "--natpf1", "delete", name)
	}
	if err != nil {
		return &model.ZMessage{Success: false, Action: "PortsMap",
			Message: fmt.Sprintf("端口映射删除失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	return &model.ZMessage{Success: true, Action: "PortsMap",
		Message: fmt.Sprintf("端口 %d 映射已删除", nat.WanPort)}, nil
}

// vboxNatRuleName 生成 NAT 转发规则名（删除时按名字定位）。
func vboxNatRuleName(wanPort int) string {
	return fmt.Sprintf("%s%d", vboxNatRulePrefix, wanPort)
}

// vboxNICMode 读取第 index 张网卡的类型（nat / bridged / none 等）。
func (d *VirtualBoxDriver) vboxNICMode(vmUUID string, index int) string {
	out, err := d.execVBox("showvminfo", vmUUID, "--machinereadable")
	if err != nil {
		return ""
	}
	prefix := fmt.Sprintf("nic%d=", index)
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimPrefix(line, prefix), "\"")
		}
	}
	return ""
}

// vboxNatForwardings 返回虚拟机现有 NAT 转发规则占用的外网端口集合。
func (d *VirtualBoxDriver) vboxNatForwardings(vmUUID string) map[int]bool {
	reserved := map[int]bool{}
	out, err := d.execVBox("showvminfo", vmUUID, "--machinereadable")
	if err != nil {
		return reserved
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Forwarding(") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		spec := strings.Trim(line[idx+1:], "\"")
		parts := strings.Split(spec, ",")
		// name,protocol,hostip,hostport,guestip,guestport
		if len(parts) < 6 {
			continue
		}
		if p, convErr := strconv.Atoi(strings.TrimSpace(parts[3])); convErr == nil && p > 0 {
			reserved[p] = true
		}
	}
	return reserved
}

// vboxAllocateHostPort 在主机端口范围内挑选未被任何 NAT 规则占用的外网端口。
func (d *VirtualBoxDriver) vboxAllocateHostPort(current map[int]bool) (int, error) {
	used := map[int]bool{}
	for p := range current {
		used[p] = true
	}
	used[0] = true

	// 其它虚拟机的 NAT 规则同样占用宿主端口
	if out, err := d.execVBox("list", "vms"); err == nil {
		for _, raw := range strings.Split(out, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || !strings.Contains(line, "{") {
				continue
			}
			name := strings.Trim(strings.SplitN(line, " ", 2)[0], "\"")
			for p := range d.vboxNatForwardings(name) {
				used[p] = true
			}
		}
	}

	low, high := d.Config.PortsStart, d.Config.PortsClose
	if low <= 0 || high <= low {
		low, high = 10000, 60000
	}
	if high-low > 20000 {
		high = low + 20000
	}
	for p := low; p <= high; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("主机端口范围 %d-%d 已全部占用", low, high)
}
