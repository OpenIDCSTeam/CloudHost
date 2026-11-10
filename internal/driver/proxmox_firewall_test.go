package driver

import "testing"

func TestSupportsFirewallPlatform(t *testing.T) {
	cases := map[string]bool{
		"PromoxSetup":   true,
		"vSphereESXi":   false,
		"HyperVSetup":   true,
		"QEMUServer":    false,
		"VMWareSetup":   false,
		"VirtualBoxs":   false,
		"LxContainer":   false,
		"OCInterface":   false,
		"SmolVM":        false,
		"MemuAndroid":   false,
		"UnknownEngine": false,
	}
	for serverType, want := range cases {
		if got := SupportsFirewallPlatform(serverType); got != want {
			t.Errorf("SupportsFirewallPlatform(%q) = %v, want %v", serverType, got, want)
		}
	}
}

func TestNormalizeFirewallRule(t *testing.T) {
	rule, err := normalizeFirewallRule(FirewallRule{
		Type:    " IN ",
		Action:  "accept",
		Proto:   "ANY",
		Source:  " 192.168.1.0/24 ",
		Sport:   " 1024:65535 ",
		Dport:   "22,80",
		Comment: " 放行 SSH ",
	})
	if err != nil {
		t.Fatalf("normalizeFirewallRule 返回错误: %v", err)
	}
	if rule.Type != "in" || rule.Action != "ACCEPT" {
		t.Errorf("方向/动作归一化失败: %+v", rule)
	}
	if rule.Proto != "" {
		t.Errorf("any 协议应归一化为空，实际 %q", rule.Proto)
	}
	if rule.Source != "192.168.1.0/24" || rule.Sport != "1024:65535" || rule.Dport != "22,80" {
		t.Errorf("字段去空格失败: %+v", rule)
	}

	invalid := []FirewallRule{
		{Type: "forward", Action: "ACCEPT"},
		{Type: "in", Action: "ALLOW"},
		{Type: "in", Action: "ACCEPT", Proto: "bogus"},
		{Type: "in", Action: "ACCEPT", Dport: "22;rm -rf"},
		{Type: "in", Action: "ACCEPT", Sport: "80-90"},
	}
	for _, item := range invalid {
		if _, err := normalizeFirewallRule(item); err == nil {
			t.Errorf("非法规则未被拒绝: %+v", item)
		}
	}
}

func TestFirewallRuleBodyOmitsEmptyFields(t *testing.T) {
	body := pveFirewallRuleBody(FirewallRule{
		Type:    "in",
		Action:  "DROP",
		Enable:  false,
		Proto:   "tcp",
		Dport:   "22",
		Comment: "管理端口",
	})
	if body["enable"] != 0 {
		t.Errorf("enable 应转为 0/1，实际 %v", body["enable"])
	}
	if body["type"] != "in" || body["action"] != "DROP" || body["proto"] != "tcp" || body["dport"] != "22" {
		t.Errorf("规则请求体字段异常: %+v", body)
	}
	for _, key := range []string{"source", "dest", "sport", "macro", "iface"} {
		if _, ok := body[key]; ok {
			t.Errorf("空字段 %s 不应下发（会覆盖平台已有值）", key)
		}
	}
}

func TestPVEFirewallBoolCompat(t *testing.T) {
	truthy := []interface{}{true, float64(1), 1, int64(1), "1", "true", "YES", " on "}
	for _, value := range truthy {
		if !pveFirewallBool(value) {
			t.Errorf("pveFirewallBool(%#v) 应为 true", value)
		}
	}
	falsy := []interface{}{false, float64(0), 0, int64(0), "0", "false", "", nil}
	for _, value := range falsy {
		if pveFirewallBool(value) {
			t.Errorf("pveFirewallBool(%#v) 应为 false", value)
		}
	}
}

func TestPVEFirewallPolicyFallback(t *testing.T) {
	if got := pveFirewallPolicy("drop", "ACCEPT"); got != "DROP" {
		t.Errorf("策略应转为大写，实际 %q", got)
	}
	if got := pveFirewallPolicy(nil, "ACCEPT"); got != "ACCEPT" {
		t.Errorf("空策略应回退默认值，实际 %q", got)
	}
	if got := pveFirewallPolicy("invalid", "DROP"); got != "DROP" {
		t.Errorf("非法策略应回退默认值，实际 %q", got)
	}
}
