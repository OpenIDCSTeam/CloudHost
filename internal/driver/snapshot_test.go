package driver

import (
	"strings"
	"testing"
)

func TestSupportsSnapshotPlatform(t *testing.T) {
	cases := map[string]bool{
		"PromoxSetup":   true,
		"vSphereESXi":   true,
		"HyperVSetup":   true,
		"QEMUServer":    true,
		"LxContainer":   true,
		"VirtualBoxs":   true,
		"VMWareSetup":   true,
		"OCInterface":   false,
		"MemuAndroid":   false,
		"SmolVM":        false,
		"UnknownEngine": false,
	}
	for serverType, want := range cases {
		if got := SupportsSnapshotPlatform(serverType); got != want {
			t.Errorf("SupportsSnapshotPlatform(%q) = %v, want %v", serverType, got, want)
		}
	}
}

func TestNormalizeSnapshotName(t *testing.T) {
	valid := []string{"snap-20260101-120000", "a", "Snap1"}
	for _, name := range valid {
		if got, err := NormalizeSnapshotName(name); err != nil || got != name {
			t.Errorf("NormalizeSnapshotName(%q) = %q, %v", name, got, err)
		}
	}

	invalid := []string{"-snap", "中文快照", "snap 1", "snap/1", "snap_1", strings.Repeat("a", 41)}
	for _, name := range invalid {
		if _, err := NormalizeSnapshotName(name); err == nil {
			t.Errorf("非法快照名未被拒绝: %q", name)
		}
	}

	if got, err := NormalizeSnapshotName("  "); err != nil || got == "" {
		t.Errorf("空名称应生成默认名，实际 %q, %v", got, err)
	}
}

func TestIsBackupSnapshot(t *testing.T) {
	for _, name := range []string{"backup-20260101-120000", "backup_1700000000", "Backup-1"} {
		if !IsBackupSnapshot(name) {
			t.Errorf("%q 应识别为备份快照", name)
		}
	}
	for _, name := range []string{"snap-1", "mybackup", "backup"} {
		if IsBackupSnapshot(name) {
			t.Errorf("%q 不应识别为备份快照", name)
		}
	}
}

func TestParseVBoxSnapshotList(t *testing.T) {
	output := `
SnapshotName="snap-1"
SnapshotUUID="{11111111-1111-1111-1111-111111111111}"
SnapshotTime="2026-11-08T10:00:00.000000000"
SnapshotDescription="before upgrade"
SnapshotName="backup-20260101-000000"
SnapshotUUID="{22222222-2222-2222-2222-222222222222}"
SnapshotTime="2026-01-01T00:00:00.000000000"
SnapshotName="snap-2"
SnapshotUUID="{33333333-3333-3333-3333-333333333333}"
SnapshotTime="2026-11-08T11:00:00.000000000"
CurrentSnapshotName="snap-2"
`
	snapshots := parseVBoxSnapshotList(output)
	if len(snapshots) != 2 {
		t.Fatalf("应过滤备份快照并保留 2 条，实际 %d: %+v", len(snapshots), snapshots)
	}
	if snapshots[0].Name != "snap-1" || snapshots[0].Description != "before upgrade" {
		t.Errorf("第一条解析异常: %+v", snapshots[0])
	}
	if snapshots[1].Name != "snap-2" || !snapshots[1].Current {
		t.Errorf("当前快照标记异常: %+v", snapshots[1])
	}
	if snapshots[0].Current {
		t.Errorf("snap-1 不应标记为当前快照")
	}
}

func TestParseLibvirtColonFields(t *testing.T) {
	output := `Name:           snap-1
Domain:         vm1
Current:        yes
State:          running
Parent:         (null)
Creation Time:  2026-11-08 10:00:00 +0800
`
	fields := parseLibvirtColonFields(output)
	if fields["Name"] != "snap-1" || fields["Current"] != "yes" {
		t.Errorf("字段解析异常: %+v", fields)
	}
	if cleanLibvirtValue(fields["Parent"]) != "" {
		t.Errorf("(null) 应转空字符串")
	}
	if parseLibvirtTime(fields["Creation Time"]) == 0 {
		t.Errorf("创建时间解析失败")
	}
}

func TestHypervACLToRule(t *testing.T) {
	inbound := hypervACLToRule(hypervACLItem{
		Action:          "Allow",
		Direction:       "Inbound",
		Weight:          float64(100),
		LocalIPAddress:  "10.0.0.5",
		LocalPort:       "22",
		RemoteIPAddress: "1.2.3.4",
		RemotePort:      []interface{}{"1234", "1235"},
		Protocol:        "Tcp",
	})
	if inbound.Type != "in" || inbound.Action != "ACCEPT" || inbound.Pos != 100 {
		t.Errorf("入站基础字段异常: %+v", inbound)
	}
	if inbound.Source != "1.2.3.4" || inbound.Dest != "10.0.0.5" {
		t.Errorf("入站源/目的映射异常: %+v", inbound)
	}
	if inbound.Sport != "1234,1235" || inbound.Dport != "22" {
		t.Errorf("入站端口映射异常: %+v", inbound)
	}
	if inbound.Proto != "tcp" {
		t.Errorf("协议映射异常: %+v", inbound)
	}

	outbound := hypervACLToRule(hypervACLItem{
		Action:          "Deny",
		Direction:       "Outbound",
		Weight:          float64(10),
		LocalIPAddress:  "10.0.0.5",
		LocalPort:       "5000-6000",
		RemoteIPAddress: "8.8.8.8",
		RemotePort:      "53",
		Protocol:        "Udp",
	})
	if outbound.Type != "out" || outbound.Action != "DROP" {
		t.Errorf("出站基础字段异常: %+v", outbound)
	}
	if outbound.Source != "10.0.0.5" || outbound.Dest != "8.8.8.8" {
		t.Errorf("出站源/目的映射异常: %+v", outbound)
	}
	if outbound.Sport != "5000:6000" || outbound.Dport != "53" {
		t.Errorf("出站端口范围应还原为统一格式: %+v", outbound)
	}
}

func TestHypervRuleFields(t *testing.T) {
	localIP, remoteIP, localPort, remotePort := hypervRuleFields(FirewallRule{
		Type: "in", Source: "1.1.1.1", Dest: "2.2.2.2", Sport: "1000", Dport: "80",
	})
	if localIP != "2.2.2.2" || remoteIP != "1.1.1.1" || localPort != "80" || remotePort != "1000" {
		t.Errorf("入站映射异常: %s %s %s %s", localIP, remoteIP, localPort, remotePort)
	}

	localIP, remoteIP, localPort, remotePort = hypervRuleFields(FirewallRule{
		Type: "out", Source: "1.1.1.1", Dest: "2.2.2.2", Sport: "8000:9000", Dport: "443",
	})
	if localIP != "1.1.1.1" || remoteIP != "2.2.2.2" || localPort != "8000-9000" || remotePort != "443" {
		t.Errorf("出站映射异常: %s %s %s %s", localIP, remoteIP, localPort, remotePort)
	}
}

func TestFirewallPlatformIncludesHyperV(t *testing.T) {
	if !SupportsFirewallPlatform("HyperVSetup") {
		t.Error("Hyper-V 应支持虚拟机防火墙")
	}
	if !SupportsFirewallPlatform("PromoxSetup") {
		t.Error("PVE 应支持虚拟机防火墙")
	}
	if SupportsFirewallPlatform("vSphereESXi") {
		t.Error("ESXi 的防火墙是主机级，不应声称支持虚拟机防火墙")
	}
}
