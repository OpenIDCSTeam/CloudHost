package service

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
}

// PlatformCapabilities 按平台类型返回能力矩阵。
//
// 说明：网卡一律按「需重启」处理——PVE 侧可以 qm set 热加，
// 但来宾系统是否识别取决于 guest（Windows 常见需要重启），
// 按需重启处理更安全。
func PlatformCapabilities(serverType string) PlatformCaps {
	switch serverType {
	case "PromoxSetup", "vSphereESXi", "VMWareSetup", "VirtualBoxs", "HyperVSetup":
		return PlatformCaps{HotISO: true, HotNIC: false, OfflineOnly: true}
	default:
		return PlatformCaps{HotISO: false, HotNIC: false, OfflineOnly: true}
	}
}
