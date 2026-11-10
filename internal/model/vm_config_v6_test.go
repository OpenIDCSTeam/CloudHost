package model

import (
	"strings"
	"testing"
)

// IPv6 后缀必须是地址的后 64 位，且以标准压缩形式输出，
// 爱快的 DHCPv6 静态绑定直接把它写给路由器。
func TestNCConfigIPv6Suffix(t *testing.T) {
	cases := []struct{ ip, want string }{
		{"2408:8207:1234:5600::1000", "::1000"},
		{"2408:8207:1234:5600:0:0:0:100", "::100"},
		// 非压缩写法
		{"2408:8207:1234:5600:abcd:ef01:2345:6789", "::abcd:ef01:2345:6789"},
		// 非法 / IPv4
		{"", ""},
		{"10.2.20.100", ""},
		{"not-an-ip", ""},
	}
	for _, c := range cases {
		nic := NCConfig{IP6Addr: c.ip}
		if got := nic.IPv6Suffix(); got != c.want {
			t.Errorf("IPv6Suffix(%q) = %q，期望 %q", c.ip, got, c.want)
		}
	}
}

// 只有 IPv6 时也必须推导出合法且稳定的 MAC：
// 全 0 前缀属于保留 OUI，网卡可能直接拒绝使用。
func TestNCConfigSendMACFromIPv6(t *testing.T) {
	nic := NCConfig{IP6Addr: "2408:8207:1234:5600::1000"}
	mac := nic.SendMAC()

	if !strings.HasPrefix(mac, "02:1c:") {
		t.Errorf("IPv6 推导的 MAC 应以本地管理前缀 02:1c 开头，实际 %q", mac)
	}
	if got := nic.SendMAC(); got != mac {
		t.Errorf("同一地址必须推导出稳定的 MAC：%q → %q", mac, got)
	}

	other := NCConfig{IP6Addr: "2408:8207:1234:5600::2000"}
	if other.SendMAC() == mac {
		t.Errorf("不同地址不应推导出相同的 MAC：%q", mac)
	}

	// IPv4 优先：双栈网卡的 MAC 继续按 IPv4 推导，保持与既有部署一致
	dual := NCConfig{IP4Addr: "10.2.20.100", IP6Addr: "2408:8207:1234:5600::1000"}
	if !strings.HasPrefix(dual.SendMAC(), "10:f6:") {
		t.Errorf("双栈网卡应优先按 IPv4 推导，实际 %q", dual.SendMAC())
	}
}

func TestNCConfigIPv6PrefixLen(t *testing.T) {
	cases := []struct {
		nic  NCConfig
		want int
	}{
		{NCConfig{}, 64},
		{NCConfig{IP6Prefix: 56}, 56},
		{NCConfig{NicMask: "48"}, 48},
		// 点分十进制掩码属于 IPv4，不能当成前缀长度
		{NCConfig{NicMask: "255.255.255.0"}, 64},
		{NCConfig{IP6Prefix: 200}, 64},
	}
	for _, c := range cases {
		if got := c.nic.IPv6PrefixLen(); got != c.want {
			t.Errorf("IPv6PrefixLen(%+v) = %d，期望 %d", c.nic, got, c.want)
		}
	}
}
