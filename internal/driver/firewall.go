package driver

import "errors"

// ErrFirewallUnsupported 平台不具备虚拟机级防火墙能力。
//
// 与 GracefulShutdownDriver 相同，防火墙是「可选能力」：
// BaseDriver 给出默认实现（false / 直接报错），只有真正实现了平台防火墙
// API 的驱动（目前是 Proxmox VE）覆盖这些方法并返回 true。
// 前端通过 /api/client/os-images/:hs_name 下发的 capabilities.firewall
// 决定是否展示防火墙页签，未实现该能力的平台连入口都不会出现。
var ErrFirewallUnsupported = errors.New("当前虚拟化平台不支持虚拟机防火墙")

// FirewallRule 平台无关的虚拟机防火墙规则。
//
// 字段语义对齐 PVE 的 firewall/rules：
//   - Type: in（入站）/ out（出站）
//   - Action: ACCEPT / DROP / REJECT
//   - Proto: tcp / udp / icmp / icmpv6 ...，空字符串表示任意协议
//   - Source / Dest: IP 或 CIDR，空表示任意
//   - Sport / Dport: 端口或端口范围（如 "80"、"8000:9000"），空表示任意
type FirewallRule struct {
	Pos     int    `json:"pos"`
	Type    string `json:"type"`
	Action  string `json:"action"`
	Enable  bool   `json:"enable"`
	Proto   string `json:"proto"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Sport   string `json:"sport"`
	Dport   string `json:"dport"`
	Comment string `json:"comment"`
	Macro   string `json:"macro"`
	Iface   string `json:"iface"`
}

// FirewallState 虚拟机防火墙整体状态。
//
// ClusterEnabled / NodeEnabled 用指针表达「未知」：低权限 API 账号可能
// 无权读取集群或节点级防火墙配置，此时不应该误报为未启用。
// Effective 表示三层开关（集群 → 节点 → 虚拟机）是否全部打开。
type FirewallState struct {
	Enabled        bool  `json:"enabled"`
	PolicyIn       string `json:"policy_in"`
	PolicyOut      string `json:"policy_out"`
	ClusterEnabled *bool `json:"cluster_enabled"`
	NodeEnabled    *bool `json:"node_enabled"`
	Effective      bool  `json:"effective"`
	// Platform 平台标识（pve / hyperv），前端据此收敛平台差异：
	// Hyper-V 没有总开关、默认策略与 REJECT，只能通过网卡扩展 ACL 控制流量。
	Platform string `json:"platform"`
}

// FirewallDriver 可选接口：平台提供虚拟机级防火墙。
//
// 调用方必须先用 SupportsFirewall 判断能力，再调用其余方法；
// BaseDriver 的默认实现全部返回 ErrFirewallUnsupported。
type FirewallDriver interface {
	// SupportsFirewall 平台是否支持虚拟机级防火墙
	SupportsFirewall() bool
	// GetFirewallState 读取开关与默认策略
	GetFirewallState(vmUUID string) (*FirewallState, error)
	// SetFirewallOptions 修改开关与默认策略（policy 为空表示保持不变）
	SetFirewallOptions(vmUUID string, enabled bool, policyIn, policyOut string) error
	// GetFirewallRules 读取规则列表（按 pos 升序）
	GetFirewallRules(vmUUID string) ([]FirewallRule, error)
	// AddFirewallRule 新增规则
	AddFirewallRule(vmUUID string, rule FirewallRule) error
	// UpdateFirewallRule 按位置更新规则
	UpdateFirewallRule(vmUUID string, pos int, rule FirewallRule) error
	// DeleteFirewallRule 按位置删除规则
	DeleteFirewallRule(vmUUID string, pos int) error
}

// SupportsFirewallPlatform 静态判断某种主机类型是否提供防火墙。
//
// 这是「平台是否支持防火墙」的唯一事实来源：能力下发
// （service.PlatformCapabilities）与前端页签可见性都据此判断，
// 后续接入新平台时只需在这里与对应驱动同步放开。
func SupportsFirewallPlatform(serverType string) bool {
	switch serverType {
	case "PromoxSetup":
		// PVE 提供 /nodes/{node}/qemu/{vmid}/firewall，规则与开关均由平台保存
		return true
	case "HyperVSetup":
		// Hyper-V 提供虚拟网卡级扩展 ACL（Add-VMNetworkAdapterExtendedAcl），
		// 是宿主机可直接下发的虚拟机级规则
		return true
	default:
		// ESXi 的防火墙是主机级（无法按 VM 配置）、QEMU / Workstation / VirtualBox
		// 没有宿主可管的虚拟机级规则、Docker / LXD 属于容器网络语义（LXD 的
		// network acl 绑定在网卡上且需要额外维护 ACL 对象，不纳入本功能）
		return false
	}
}

// ============================================================
// 基类默认实现：不具备防火墙能力的平台统一走这里
// ============================================================

// SupportsFirewall 平台默认不具备虚拟机防火墙能力
func (b *BaseDriver) SupportsFirewall() bool { return false }

// GetFirewallState 平台不支持时返回错误
func (b *BaseDriver) GetFirewallState(vmUUID string) (*FirewallState, error) {
	return nil, ErrFirewallUnsupported
}

// SetFirewallOptions 平台不支持时返回错误
func (b *BaseDriver) SetFirewallOptions(vmUUID string, enabled bool, policyIn, policyOut string) error {
	return ErrFirewallUnsupported
}

// GetFirewallRules 平台不支持时返回错误
func (b *BaseDriver) GetFirewallRules(vmUUID string) ([]FirewallRule, error) {
	return nil, ErrFirewallUnsupported
}

// AddFirewallRule 平台不支持时返回错误
func (b *BaseDriver) AddFirewallRule(vmUUID string, rule FirewallRule) error {
	return ErrFirewallUnsupported
}

// UpdateFirewallRule 平台不支持时返回错误
func (b *BaseDriver) UpdateFirewallRule(vmUUID string, pos int, rule FirewallRule) error {
	return ErrFirewallUnsupported
}

// DeleteFirewallRule 平台不支持时返回错误
func (b *BaseDriver) DeleteFirewallRule(vmUUID string, pos int) error {
	return ErrFirewallUnsupported
}
