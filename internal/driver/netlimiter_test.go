package driver

import (
	"encoding/binary"
	"testing"
	"unsafe"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestNLRuleLayout 校验规则结构与驱动侧 64 字节结构逐字段对齐。
//
// 字段偏移来自 NetLimiter.sys 反汇编（set rule 处理函数按 0x40 定长拷贝，
// 限速判定分别读取 +0x18 上行速率与 +0x38 下行速率）。偏移写错会导致
// 限速不生效或误判为「不限速」，因此单独固化用例。
func TestNLRuleLayout(t *testing.T) {
	if got := unsafe.Sizeof(nlRule{}); got != nlRuleSize {
		t.Fatalf("nlRule 大小 = %d，期望 %d", got, nlRuleSize)
	}
	if got := unsafe.Offsetof(nlRule{}.MAC); got != 4 {
		t.Fatalf("MAC 偏移 = %d，期望 4", got)
	}
	if got := unsafe.Offsetof(nlRule{}.InUse); got != 12 {
		t.Fatalf("InUse 偏移 = %d，期望 12", got)
	}
	if got := unsafe.Offsetof(nlRule{}.RateTotal); got != 0x10 {
		t.Fatalf("RateTotal 偏移 = %d，期望 0x10", got)
	}
	if got := unsafe.Offsetof(nlRule{}.RateUp); got != 0x18 {
		t.Fatalf("RateUp 偏移 = %d，期望 0x18", got)
	}
	if got := unsafe.Offsetof(nlRule{}.QuotaDown); got != 0x20 {
		t.Fatalf("QuotaDown 偏移 = %d，期望 0x20", got)
	}
	if got := unsafe.Offsetof(nlRule{}.QuotaTotal); got != 0x28 {
		t.Fatalf("QuotaTotal 偏移 = %d，期望 0x28", got)
	}
	if got := unsafe.Offsetof(nlRule{}.QuotaUp); got != 0x30 {
		t.Fatalf("QuotaUp 偏移 = %d，期望 0x30", got)
	}
	if got := unsafe.Offsetof(nlRule{}.RateDown); got != 0x38 {
		t.Fatalf("RateDown 偏移 = %d，期望 0x38", got)
	}
}

// TestNLRuleMarshal 校验序列化后的字节布局与反序列化往返一致。
func TestNLRuleMarshal(t *testing.T) {
	rule := nlRule{
		Tag:        nlRuleTag,
		MAC:        [6]byte{0x00, 0x15, 0x5D, 0x0A, 0x1B, 0x2C},
		InUse:      1,
		RateTotal:  nlUnlimited,
		RateUp:     mbpsToBytesPerSec(100),
		QuotaDown:  nlUnlimited,
		QuotaTotal: nlUnlimited,
		QuotaUp:    nlUnlimited,
		RateDown:   mbpsToBytesPerSec(200),
	}
	b := rule.marshal()
	if len(b) != nlRuleSize {
		t.Fatalf("序列化长度 = %d，期望 %d", len(b), nlRuleSize)
	}
	if got := binary.LittleEndian.Uint32(b[0:]); got != nlRuleTag {
		t.Fatalf("Tag = %#x，期望 %#x", got, nlRuleTag)
	}
	if want := []byte{0x00, 0x15, 0x5D, 0x0A, 0x1B, 0x2C}; string(b[4:10]) != string(want) {
		t.Fatalf("MAC 字节 = % X，期望 % X", b[4:10], want)
	}
	if got := binary.LittleEndian.Uint32(b[12:]); got != 1 {
		t.Fatalf("InUse = %d，期望 1", got)
	}
	if got := binary.LittleEndian.Uint64(b[0x18:]); got != rule.RateUp {
		t.Fatalf("RateUp = %d，期望 %d", got, rule.RateUp)
	}
	if got := binary.LittleEndian.Uint64(b[0x38:]); got != rule.RateDown {
		t.Fatalf("RateDown = %d，期望 %d", got, rule.RateDown)
	}

	back, ok := unmarshalNLRule(b)
	if !ok {
		t.Fatal("反序列化失败")
	}
	if back.MAC != rule.MAC || back.RateUp != rule.RateUp || back.RateDown != rule.RateDown {
		t.Fatalf("往返结果不一致: %+v", back)
	}
}

// TestMbpsToBytesPerSec 校验带宽换算与「不限速」约定。
func TestMbpsToBytesPerSec(t *testing.T) {
	cases := []struct {
		mbps int
		want uint64
	}{
		{0, nlUnlimited},
		{-1, nlUnlimited},
		{1, 131072},
		{100, 13107200},
	}
	for _, c := range cases {
		if got := mbpsToBytesPerSec(c.mbps); got != c.want {
			t.Fatalf("mbpsToBytesPerSec(%d) = %d，期望 %d", c.mbps, got, c.want)
		}
	}
}

// TestParseMAC 校验 MAC 解析：兼容 PowerShell 输出与常见分隔写法。
func TestParseMAC(t *testing.T) {
	want := [6]byte{0x00, 0x15, 0x5D, 0x0A, 0x1B, 0x2C}
	for _, raw := range []string{
		"00155D0A1B2C",
		"00-15-5D-0A-1B-2C",
		"00:15:5d:0a:1b:2c",
		"0015.5D0A.1B2C",
		" {00-15-5D-0A-1B-2C} ",
	} {
		got, err := parseMAC(raw)
		if err != nil {
			t.Fatalf("parseMAC(%q) 意外失败: %v", raw, err)
		}
		if got != want {
			t.Fatalf("parseMAC(%q) = % X，期望 % X", raw, got, want)
		}
	}
	for _, raw := range []string{"", "00155D0A1B", "000000000000", "zz155D0A1B2C"} {
		if _, err := parseMAC(raw); err == nil {
			t.Fatalf("parseMAC(%q) 应失败", raw)
		}
	}
}

// TestHvMACsOf 校验按 VmUUID(GUID)/OsName 匹配网卡 MAC。
func TestHvMACsOf(t *testing.T) {
	macA := [6]byte{0x00, 0x15, 0x5D, 0x00, 0x00, 0x01}
	macB := [6]byte{0x00, 0x15, 0x5D, 0x00, 0x00, 0x02}
	adapters := map[string][][6]byte{
		"3f2b0d0a-1c2d-4e5f-8a9b-0c1d2e3f4a5b": {macA},
		"web-01":                               {macA},
		"db-01":                                {macB},
	}

	got := hvMACsOf(adapters, &model.VMConfig{
		VmUUID: "3F2B0D0A-1C2D-4E5F-8A9B-0C1D2E3F4A5B",
		OsName: "web-01",
	})
	if len(got) != 1 || got[0] != macA {
		t.Fatalf("GUID 匹配结果 = %v，期望 [% X]", got, macA)
	}

	got = hvMACsOf(adapters, &model.VMConfig{VmUUID: "unknown", OsName: "db-01"})
	if len(got) != 1 || got[0] != macB {
		t.Fatalf("名称匹配结果 = %v，期望 [% X]", got, macB)
	}

	if got = hvMACsOf(adapters, &model.VMConfig{VmUUID: "unknown", OsName: "missing"}); len(got) != 0 {
		t.Fatalf("未匹配应返回空，实际 %v", got)
	}
}
