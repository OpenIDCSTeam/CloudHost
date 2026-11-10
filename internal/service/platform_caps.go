package service

import "github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"

// PlatformCaps 描述平台支持哪些「免重启」操作。
//
// 前端据此决定两件事：
//  1. 是否提示「需重启 / 同意强制关机」——PVE、ESXi 等平台换光盘是热生效的，
//     统一提示会让用户误以为必须重启；
//  2. 操作成功后是否自动重启——需要重启才生效的操作，如果前端只提示不执行，
//     就会出现「勾选了强制关机、提示了重启，但虚拟机根本没重启」。
type PlatformCaps struct {
	// HotISO 挂载 / 卸载光盘是否热生效（无需重启）
	HotISO bool `json:"hot_iso"`
	// HotNIC 增删网卡是否热生效
	HotNIC bool `json:"hot_nic"`
	// OfflineOnly 数据盘 / PCI / USB 变更是否必须关机
	OfflineOnly bool `json:"offline_only"`
	// Firewall 平台是否支持虚拟机级防火墙。
	//
	// 前端据此决定是否展示「防火墙」页签：不支持的平台（ESXi、QEMU、
	// Workstation、VirtualBox、Docker、LXD 等）连入口都不出现，
	// 而不是展示一个点进去只会报错的空页面。
	Firewall bool `json:"firewall"`
	// Snapshot 平台是否支持虚拟机快照。
	//
	// 前端据此决定是否展示「快照」页签：Docker、MEmu、SmolVM 不展示。
	Snapshot bool `json:"snapshot"`
}

// PlatformCapabilities 按平台类型返回能力矩阵。
//
// 说明：网卡一律按「需重启」处理——PVE 侧可以 qm set 热加，
// 但来宾系统是否识别取决于 guest（Windows 常见需要重启），
// 按需重启处理更安全。
func PlatformCapabilities(serverType string) PlatformCaps {
	caps := PlatformCaps{
		OfflineOnly: true,
		// 与驱动能力共用同一判断，避免「后端说支持、驱动没实现」的错位
		Firewall: driver.SupportsFirewallPlatform(serverType),
		Snapshot: driver.SupportsSnapshotPlatform(serverType),
	}
	switch serverType {
	case "PromoxSetup", "vSphereESXi", "VMWareSetup", "VirtualBoxs", "HyperVSetup":
		caps.HotISO = true
	}
	return caps
}
