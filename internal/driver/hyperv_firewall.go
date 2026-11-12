package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Hyper-V 防火墙基于虚拟网卡扩展 ACL：
//
//	Get-VM -Id <guid> | Get-VMNetworkAdapterExtendedAcl
//	Get-VM -Id <guid> | Add-VMNetworkAdapterExtendedAcl -Action Allow|Deny ...
//	Remove-VMNetworkAdapterExtendedAcl -VM <vm> -Direction Inbound|Outbound -Weight <n>
//
// 与 PVE 的差异：
//   - 没有虚拟机级总开关与默认策略，默认放行全部流量
//   - 动作只有 Allow/Deny（REJECT 降级为 Deny）
//   - 规则按 Weight 从大到小匹配，Weight 同时是规则唯一标识
//   - 没有描述、单条启停；删除即停用
type hypervACLItem struct {
	Action          string      `json:"Action"`
	Direction       string      `json:"Direction"`
	Weight          interface{} `json:"Weight"`
	LocalIPAddress  interface{} `json:"LocalIPAddress"`
	LocalPort       interface{} `json:"LocalPort"`
	RemoteIPAddress interface{} `json:"RemoteIPAddress"`
	RemotePort      interface{} `json:"RemotePort"`
	Protocol        interface{} `json:"Protocol"`
}

// SupportsFirewall Hyper-V 提供虚拟机网卡级扩展 ACL
func (d *HyperVDriver) SupportsFirewall() bool { return true }

// GetFirewallState Hyper-V 没有全局开关，返回默认放行的静态状态。
//
// Platform=hyperv 让前端隐藏总开关、默认策略与 REJECT 选项。
func (d *HyperVDriver) GetFirewallState(vmUUID string) (*FirewallState, error) {
	return &FirewallState{
		Enabled:   true,
		PolicyIn:  "ACCEPT",
		PolicyOut: "ACCEPT",
		Effective: true,
		Platform:  "hyperv",
	}, nil
}

// SetFirewallOptions Hyper-V 不支持虚拟机级总开关与默认策略
func (d *HyperVDriver) SetFirewallOptions(vmUUID string, enabled bool, policyIn, policyOut string) error {
	return errors.New("Hyper-V 通过虚拟网卡扩展 ACL 控制流量，不支持全局开关或默认策略")
}

// GetFirewallRules 读取 Hyper-V 扩展 ACL 并转换为统一规则模型。
func (d *HyperVDriver) GetFirewallRules(vmUUID string) ([]FirewallRule, error) {
	script := fmt.Sprintf(
		"$vm = %s; if (-not $vm) { throw '虚拟机不存在: %s' }; "+
			"@($vm | Get-VMNetworkAdapterExtendedAcl) | Select-Object Action,Direction,Weight,"+
			"LocalIPAddress,LocalPort,RemoteIPAddress,RemotePort,Protocol | ConvertTo-Json -Compress",
		hvVMExpr(vmUUID), psEscape(vmUUID))

	out, err := d.execPS(script)
	if err != nil {
		return nil, fmt.Errorf("读取防火墙规则失败: %v (%s)", err, strings.TrimSpace(out))
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "[]" || out == "null" {
		return []FirewallRule{}, nil
	}

	var items []hypervACLItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		// 只有一条规则时 ConvertTo-Json 输出对象而不是数组
		var single hypervACLItem
		if err2 := json.Unmarshal([]byte(out), &single); err2 != nil {
			return nil, fmt.Errorf("解析防火墙规则失败: %v", err)
		}
		items = []hypervACLItem{single}
	}

	// 同一 ACL 条目可能同时出现在入站/出站查询结果中，按方向 + Weight 去重
	seen := make(map[string]bool, len(items))
	rules := make([]FirewallRule, 0, len(items))
	for _, item := range items {
		rule := hypervACLToRule(item)
		key := fmt.Sprintf("%s-%d", rule.Type, rule.Pos)
		if seen[key] {
			continue
		}
		seen[key] = true
		rules = append(rules, rule)
	}
	// Weight 越大越先匹配，按匹配顺序返回
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Pos > rules[j].Pos })
	return rules, nil
}

// AddFirewallRule 新增 Hyper-V 扩展 ACL，权重自动分配在末尾。
func (d *HyperVDriver) AddFirewallRule(vmUUID string, rule FirewallRule) error {
	normalized, err := normalizeFirewallRule(rule)
	if err != nil {
		return err
	}
	if err := validateHypervFirewallRule(normalized); err != nil {
		return err
	}
	weight, err := d.hypervNextWeight(vmUUID)
	if err != nil {
		return err
	}
	return d.hypervAddRule(vmUUID, weight, normalized)
}

// UpdateFirewallRule Hyper-V 没有更新 API：按旧权重删除后按同权重重建。
func (d *HyperVDriver) UpdateFirewallRule(vmUUID string, pos int, rule FirewallRule) error {
	if pos <= 0 || pos > 255 {
		return errors.New("规则位置无效")
	}
	normalized, err := normalizeFirewallRule(rule)
	if err != nil {
		return err
	}
	if err := validateHypervFirewallRule(normalized); err != nil {
		return err
	}
	if err := d.hypervRemoveRule(vmUUID, pos); err != nil {
		return err
	}
	return d.hypervAddRule(vmUUID, pos, normalized)
}

// DeleteFirewallRule 删除 Hyper-V 扩展 ACL。
func (d *HyperVDriver) DeleteFirewallRule(vmUUID string, pos int) error {
	if pos <= 0 || pos > 255 {
		return errors.New("规则位置无效")
	}
	return d.hypervRemoveRule(vmUUID, pos)
}

// ============================================================
// PowerShell 拼接与字段映射
// ============================================================

// hypervNextWeight 取现有最大权重 +10；超过 255 时回填最小空位。
func (d *HyperVDriver) hypervNextWeight(vmUUID string) (int, error) {
	rules, err := d.GetFirewallRules(vmUUID)
	if err != nil {
		return 0, err
	}
	used := make(map[int]bool, len(rules))
	maxWeight := 0
	for _, rule := range rules {
		used[rule.Pos] = true
		if rule.Pos > maxWeight {
			maxWeight = rule.Pos
		}
	}
	if weight := maxWeight + 10; weight <= 255 && !used[weight] {
		return weight, nil
	}
	for weight := 1; weight <= 255; weight++ {
		if !used[weight] {
			return weight, nil
		}
	}
	return 0, errors.New("Hyper-V 防火墙规则数量已达上限（255 条）")
}

func (d *HyperVDriver) hypervAddRule(vmUUID string, weight int, rule FirewallRule) error {
	localIP, remoteIP, localPort, remotePort := hypervRuleFields(rule)

	script := fmt.Sprintf(
		"$vm = %s; if (-not $vm) { throw '虚拟机不存在: %s' }; "+
			"$vm | Add-VMNetworkAdapterExtendedAcl -Action %s -Direction %s -Weight %d",
		hvVMExpr(vmUUID), psEscape(vmUUID), hypervAction(rule.Action), hypervDirection(rule.Type), weight)

	if proto := hypervProtocol(rule.Proto); proto != "" {
		script += " -Protocol " + proto
	}
	if localIP != "" {
		script += " -LocalIPAddress '" + psEscape(localIP) + "'"
	}
	if remoteIP != "" {
		script += " -RemoteIPAddress '" + psEscape(remoteIP) + "'"
	}
	if localPort != "" {
		script += " -LocalPort '" + psEscape(localPort) + "'"
	}
	if remotePort != "" {
		script += " -RemotePort '" + psEscape(remotePort) + "'"
	}
	script += " -ErrorAction Stop"

	if out, err := d.execPS(script); err != nil {
		return fmt.Errorf("新增防火墙规则失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// hypervRemoveRule 按 Weight 删除两个方向上可能存在的同权重规则。
//
// Weight 在单台虚拟机上唯一，因此最多只会命中一条，另一个方向会静默跳过。
func (d *HyperVDriver) hypervRemoveRule(vmUUID string, weight int) error {
	script := fmt.Sprintf(
		"$vm = %s; if (-not $vm) { throw '虚拟机不存在: %s' }; "+
			"Remove-VMNetworkAdapterExtendedAcl -VM $vm -Direction Inbound -Weight %d -ErrorAction SilentlyContinue; "+
			"Remove-VMNetworkAdapterExtendedAcl -VM $vm -Direction Outbound -Weight %d -ErrorAction SilentlyContinue",
		hvVMExpr(vmUUID), psEscape(vmUUID), weight, weight)
	if out, err := d.execPS(script); err != nil {
		return fmt.Errorf("删除防火墙规则失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// hypervRuleFields 把统一的源/目的模型映射到 Hyper-V 的本地/远程视角。
//
// Hyper-V 的 Local 指虚拟机自身：
//   - 入站：Local=目的、Remote=源
//   - 出站：Local=源、Remote=目的
func hypervRuleFields(rule FirewallRule) (localIP, remoteIP, localPort, remotePort string) {
	if rule.Type == "out" {
		return rule.Source, rule.Dest, hypervPort(rule.Sport), hypervPort(rule.Dport)
	}
	return rule.Dest, rule.Source, hypervPort(rule.Dport), hypervPort(rule.Sport)
}

// hypervAction 统一动作映射，REJECT 降级为 Deny
func hypervAction(action string) string {
	if strings.EqualFold(action, "ACCEPT") {
		return "Allow"
	}
	return "Deny"
}

// hypervDirection 方向映射
func hypervDirection(ruleType string) string {
	if ruleType == "out" {
		return "Outbound"
	}
	return "Inbound"
}

// hypervProtocol 协议映射：ICMP 用协议号
func hypervProtocol(proto string) string {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "tcp":
		return "TCP"
	case "udp":
		return "UDP"
	case "icmp":
		return "1"
	case "icmpv6":
		return "58"
	default:
		return ""
	}
}

// hypervPort Hyper-V 的端口范围使用 - 分隔
func hypervPort(port string) string {
	return strings.ReplaceAll(strings.TrimSpace(port), ":", "-")
}

// normalizeHypervPort 把 Hyper-V 的端口范围还原为统一格式
func normalizeHypervPort(port string) string {
	return strings.ReplaceAll(strings.TrimSpace(port), "-", ":")
}

// validateHypervFirewallRule 校验 Hyper-V 不支持的字段组合
func validateHypervFirewallRule(rule FirewallRule) error {
	switch strings.ToLower(strings.TrimSpace(rule.Proto)) {
	case "", "tcp", "udp", "icmp", "icmpv6":
		return nil
	default:
		return fmt.Errorf("Hyper-V 防火墙不支持协议 %s", rule.Proto)
	}
}

// hypervACLToRule 扩展 ACL 转统一规则
func hypervACLToRule(item hypervACLItem) FirewallRule {
	rule := FirewallRule{
		Pos:    pveFirewallInt(item.Weight),
		Enable: true,
	}
	if strings.EqualFold(strings.TrimSpace(item.Direction), "Outbound") {
		rule.Type = "out"
	} else {
		rule.Type = "in"
	}
	if strings.EqualFold(strings.TrimSpace(item.Action), "Allow") {
		rule.Action = "ACCEPT"
	} else {
		rule.Action = "DROP"
	}
	rule.Proto = normalizeHypervACLProto(item.Protocol)

	localIP := hypervValueString(item.LocalIPAddress)
	remoteIP := hypervValueString(item.RemoteIPAddress)
	localPort := normalizeHypervPort(hypervValueString(item.LocalPort))
	remotePort := normalizeHypervPort(hypervValueString(item.RemotePort))

	if rule.Type == "out" {
		rule.Source, rule.Dest = localIP, remoteIP
		rule.Sport, rule.Dport = localPort, remotePort
	} else {
		rule.Source, rule.Dest = remoteIP, localIP
		rule.Sport, rule.Dport = remotePort, localPort
	}
	return rule
}

// normalizeHypervACLProto ACL 协议字段转统一协议名
func normalizeHypervACLProto(value interface{}) string {
	switch strings.ToLower(strings.TrimSpace(hypervValueString(value))) {
	case "", "any":
		return ""
	case "tcp":
		return "tcp"
	case "udp":
		return "udp"
	case "1", "icmp", "icmpv4":
		return "icmp"
	case "58", "icmpv6":
		return "icmpv6"
	default:
		return strings.ToLower(strings.TrimSpace(hypervValueString(value)))
	}
}

// hypervValueString 把 PowerShell JSON 的标量/数组统一成逗号分隔字符串。
// "Any" 表示未限制，统一转为空。
func hypervValueString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		s := strings.TrimSpace(v)
		if strings.EqualFold(s, "any") {
			return ""
		}
		return s
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := hypervValueString(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case bool:
		if v {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
