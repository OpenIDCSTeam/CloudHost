// Package driver Proxmox VE 虚拟机防火墙实现。
//
// 使用 PVE 原生的虚拟机级防火墙 API：
//
//	GET    /nodes/{node}/qemu/{vmid}/firewall/options
//	PUT    /nodes/{node}/qemu/{vmid}/firewall/options
//	GET    /nodes/{node}/qemu/{vmid}/firewall/rules
//	POST   /nodes/{node}/qemu/{vmid}/firewall/rules
//	PUT    /nodes/{node}/qemu/{vmid}/firewall/rules/{pos}
//	DELETE /nodes/{node}/qemu/{vmid}/firewall/rules/{pos}
//
// 规则与开关全部由 PVE 持久化，主控不另存一份，避免出现
// 「控制台显示已放行、平台实际未放行」的双份状态不一致。
package driver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

// pveFirewallTimeout 单次防火墙读写（含集群/节点开关探测）的总超时
const pveFirewallTimeout = 20 * time.Second

// pveFirewallRule PVE 返回的规则原始结构。
//
// pos / enable / 端口等字段使用 interface{} 接收：PVE 在不同版本中
// 既可能返回数字（1/0），也可能返回字符串，写死类型会导致整个列表
// 解析失败（一个字段类型不符就丢光所有规则）。
type pveFirewallRule struct {
	Pos     interface{} `json:"pos"`
	Type    string      `json:"type"`
	Action  string      `json:"action"`
	Enable  interface{} `json:"enable"`
	Proto   interface{} `json:"proto"`
	Source  interface{} `json:"source"`
	Dest    interface{} `json:"dest"`
	Sport   interface{} `json:"sport"`
	Dport   interface{} `json:"dport"`
	Comment interface{} `json:"comment"`
	Macro   interface{} `json:"macro"`
	Iface   interface{} `json:"iface"`
}

// SupportsFirewall PVE 提供虚拟机级防火墙
func (d *ProxmoxDriver) SupportsFirewall() bool { return true }

// pveFirewallPath 拼装虚拟机防火墙 API 路径
func (d *ProxmoxDriver) pveFirewallPath(vmid int64, suffix string) string {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/firewall", d.nodeName(), vmid)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

// pveFirewallContext 返回带超时的调用 context
func (d *ProxmoxDriver) pveFirewallContext(vmUUID string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(d.Ctx(vmUUID), pveFirewallTimeout)
}

// GetFirewallState 读取虚拟机防火墙开关、默认策略以及集群/节点总开关。
func (d *ProxmoxDriver) GetFirewallState(vmUUID string) (*FirewallState, error) {
	client, err := d.apiConn()
	if err != nil {
		return nil, err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	var raw map[string]interface{}
	if err := client.Get(ctx, d.pveFirewallPath(vmid, "options"), &raw); err != nil {
		return nil, fmt.Errorf("读取防火墙配置失败: %w", err)
	}

	state := &FirewallState{
		Enabled:   pveFirewallBool(raw["enable"]),
		PolicyIn:  pveFirewallPolicy(raw["policy_in"], "DROP"),
		PolicyOut: pveFirewallPolicy(raw["policy_out"], "ACCEPT"),
		Platform:  "pve",
	}

	// 集群与节点开关决定虚拟机规则是否真正生效：
	// PVE 要求 datacenter firewall 与 node firewall 都开启后，VM 级规则才被加载。
	// 低权限账号读取失败时保持 nil（未知），不误报为未启用。
	state.ClusterEnabled = d.pveFirewallSwitch(ctx, client, "/cluster/firewall/options")
	state.NodeEnabled = d.pveFirewallSwitch(ctx, client, fmt.Sprintf("/nodes/%s/firewall/options", d.nodeName()))

	state.Effective = state.Enabled &&
		(state.ClusterEnabled == nil || *state.ClusterEnabled) &&
		(state.NodeEnabled == nil || *state.NodeEnabled)
	return state, nil
}

// pveFirewallSwitch 读取集群/节点级防火墙开关，失败返回 nil（未知）
func (d *ProxmoxDriver) pveFirewallSwitch(ctx context.Context, client *proxmox.Client, path string) *bool {
	var raw map[string]interface{}
	if err := client.Get(ctx, path, &raw); err != nil {
		return nil
	}
	enabled := pveFirewallBool(raw["enable"])
	return &enabled
}

// SetFirewallOptions 保存虚拟机防火墙开关与默认策略。
func (d *ProxmoxDriver) SetFirewallOptions(vmUUID string, enabled bool, policyIn, policyOut string) error {
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	body := map[string]interface{}{"enable": pveFirewallEnable(enabled)}
	if p := normalizePVEPolicy(policyIn); p != "" {
		body["policy_in"] = p
	}
	if p := normalizePVEPolicy(policyOut); p != "" {
		body["policy_out"] = p
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	// 必须传入非 nil 的响应接收者：go-proxmox 在 v == nil 时直接跳过响应解析，
	// 平台返回 4xx（除 400/401/403）也会被当成成功，导致前端误报「已保存」。
	var result interface{}
	if err := client.Put(ctx, d.pveFirewallPath(vmid, "options"), body, &result); err != nil {
		return fmt.Errorf("保存防火墙配置失败: %w", err)
	}
	return nil
}

// GetFirewallRules 读取虚拟机防火墙规则（按 pos 升序）。
func (d *ProxmoxDriver) GetFirewallRules(vmUUID string) ([]FirewallRule, error) {
	client, err := d.apiConn()
	if err != nil {
		return nil, err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	var raw []pveFirewallRule
	if err := client.Get(ctx, d.pveFirewallPath(vmid, "rules"), &raw); err != nil {
		return nil, fmt.Errorf("读取防火墙规则失败: %w", err)
	}

	rules := make([]FirewallRule, 0, len(raw))
	for _, item := range raw {
		rules = append(rules, FirewallRule{
			Pos:     pveFirewallInt(item.Pos),
			Type:    strings.ToLower(strings.TrimSpace(item.Type)),
			Action:  strings.ToUpper(strings.TrimSpace(item.Action)),
			Enable:  pveFirewallBool(item.Enable),
			Proto:   strings.ToLower(pveFirewallString(item.Proto)),
			Source:  pveFirewallString(item.Source),
			Dest:    pveFirewallString(item.Dest),
			Sport:   pveFirewallString(item.Sport),
			Dport:   pveFirewallString(item.Dport),
			Comment: pveFirewallString(item.Comment),
			Macro:   pveFirewallString(item.Macro),
			Iface:   pveFirewallString(item.Iface),
		})
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Pos < rules[j].Pos })
	return rules, nil
}

// AddFirewallRule 新增规则，位置由 PVE 自动分配。
func (d *ProxmoxDriver) AddFirewallRule(vmUUID string, rule FirewallRule) error {
	normalized, err := normalizeFirewallRule(rule)
	if err != nil {
		return err
	}
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	body := pveFirewallRuleBody(normalized)
	var result interface{}
	if err := client.Post(ctx, d.pveFirewallPath(vmid, "rules"), body, &result); err != nil {
		return fmt.Errorf("新增防火墙规则失败: %w", err)
	}
	return nil
}

// UpdateFirewallRule 按位置更新规则。
//
// 被清空的字段通过 PVE 的 delete 参数移除：只传非空字段无法表达
// 「把源地址从 1.2.3.4 改回任意」，会把旧值静默保留下来。
func (d *ProxmoxDriver) UpdateFirewallRule(vmUUID string, pos int, rule FirewallRule) error {
	if pos < 0 {
		return errors.New("规则位置无效")
	}
	normalized, err := normalizeFirewallRule(rule)
	if err != nil {
		return err
	}
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	body := pveFirewallRuleBody(normalized)
	cleared := make([]string, 0, 5)
	if normalized.Proto == "" {
		cleared = append(cleared, "proto")
	}
	if normalized.Source == "" {
		cleared = append(cleared, "source")
	}
	if normalized.Dest == "" {
		cleared = append(cleared, "dest")
	}
	if normalized.Sport == "" {
		cleared = append(cleared, "sport")
	}
	if normalized.Dport == "" {
		cleared = append(cleared, "dport")
	}
	if len(cleared) > 0 {
		body["delete"] = strings.Join(cleared, ",")
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	path := fmt.Sprintf("%s/%d", d.pveFirewallPath(vmid, "rules"), pos)
	var result interface{}
	if err := client.Put(ctx, path, body, &result); err != nil {
		return fmt.Errorf("保存防火墙规则失败: %w", err)
	}
	return nil
}

// DeleteFirewallRule 按位置删除规则。
func (d *ProxmoxDriver) DeleteFirewallRule(vmUUID string, pos int) error {
	if pos < 0 {
		return errors.New("规则位置无效")
	}
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	path := fmt.Sprintf("%s/%d", d.pveFirewallPath(vmid, "rules"), pos)
	var result interface{}
	if err := client.Delete(ctx, path, &result); err != nil {
		return fmt.Errorf("删除防火墙规则失败: %w", err)
	}
	return nil
}

// ============================================================
// 规则归一化与字段转换
// ============================================================

// normalizeFirewallRule 校验并归一化规则字段。
//
// 前端可能提交大写方向、空动作、any 协议等写法，这里统一成 PVE 认识的值，
// 避免把明显非法的规则下发到平台后才报一句难以理解的 API 错误。
func normalizeFirewallRule(rule FirewallRule) (FirewallRule, error) {
	rule.Type = strings.ToLower(strings.TrimSpace(rule.Type))
	if rule.Type == "" {
		rule.Type = "in"
	}
	if rule.Type != "in" && rule.Type != "out" {
		return rule, errors.New("规则方向只能是 in 或 out")
	}

	rule.Action = strings.ToUpper(strings.TrimSpace(rule.Action))
	if rule.Action == "" {
		rule.Action = "ACCEPT"
	}
	switch rule.Action {
	case "ACCEPT", "DROP", "REJECT":
	default:
		return rule, errors.New("规则动作只能是 ACCEPT、DROP 或 REJECT")
	}

	rule.Proto = normalizePVEProto(rule.Proto)
	if !validPVEProto(rule.Proto) {
		return rule, fmt.Errorf("不支持的协议：%s", rule.Proto)
	}

	rule.Source = strings.TrimSpace(rule.Source)
	rule.Dest = strings.TrimSpace(rule.Dest)
	rule.Sport = strings.TrimSpace(rule.Sport)
	rule.Dport = strings.TrimSpace(rule.Dport)
	if !validPVEPort(rule.Sport) {
		return rule, errors.New("源端口格式无效，只支持数字、端口范围（a:b）与列表（a,b）")
	}
	if !validPVEPort(rule.Dport) {
		return rule, errors.New("目的端口格式无效，只支持数字、端口范围（a:b）与列表（a,b）")
	}

	rule.Comment = strings.TrimSpace(rule.Comment)
	rule.Macro = strings.TrimSpace(rule.Macro)
	rule.Iface = strings.TrimSpace(rule.Iface)
	return rule, nil
}

// pveFirewallRuleBody 构造 PVE 规则请求体
func pveFirewallRuleBody(rule FirewallRule) map[string]interface{} {
	body := map[string]interface{}{
		"type":   rule.Type,
		"action": rule.Action,
		"enable": pveFirewallEnable(rule.Enable),
	}
	if rule.Proto != "" {
		body["proto"] = rule.Proto
	}
	if rule.Source != "" {
		body["source"] = rule.Source
	}
	if rule.Dest != "" {
		body["dest"] = rule.Dest
	}
	if rule.Sport != "" {
		body["sport"] = rule.Sport
	}
	if rule.Dport != "" {
		body["dport"] = rule.Dport
	}
	if rule.Comment != "" {
		body["comment"] = rule.Comment
	}
	if rule.Macro != "" {
		body["macro"] = rule.Macro
	}
	if rule.Iface != "" {
		body["iface"] = rule.Iface
	}
	return body
}

// normalizePVEPolicy 归一化默认策略
func normalizePVEPolicy(policy string) string {
	policy = strings.ToUpper(strings.TrimSpace(policy))
	switch policy {
	case "ACCEPT", "DROP", "REJECT":
		return policy
	default:
		return ""
	}
}

// normalizePVEProto 归一化协议，any/空 表示任意协议
func normalizePVEProto(proto string) string {
	proto = strings.ToLower(strings.TrimSpace(proto))
	switch proto {
	case "any", "all", "*":
		return ""
	}
	return proto
}

// validPVEProto 校验 PVE 支持的协议名
func validPVEProto(proto string) bool {
	switch proto {
	case "", "tcp", "udp", "icmp", "icmpv6", "igmp", "esp", "ah",
		"sctp", "udplite", "gre", "dccp":
		return true
	default:
		return false
	}
}

// validPVEPort 校验端口写法：单端口、范围（a:b）、列表（a,b）
func validPVEPort(port string) bool {
	if port == "" {
		return true
	}
	for _, r := range port {
		if (r < '0' || r > '9') && r != ':' && r != ',' {
			return false
		}
	}
	return true
}

// pveFirewallEnable 布尔转 PVE 的 0/1
func pveFirewallEnable(enabled bool) int {
	if enabled {
		return 1
	}
	return 0
}

// pveFirewallBool 兼容 bool / 数字 / 字符串三种开关写法
func pveFirewallBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// pveFirewallInt 兼容数字 / 字符串的位置字段
func pveFirewallInt(value interface{}) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

// pveFirewallString 把任意标量转成字符串
func pveFirewallString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case float32:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return fmt.Sprint(value)
}

// pveFirewallPolicy 读取默认策略并回退到 PVE 默认值
func pveFirewallPolicy(value interface{}, fallback string) string {
	if policy := normalizePVEPolicy(pveFirewallString(value)); policy != "" {
		return policy
	}
	return fallback
}
