package driver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

func TestNormalizeVMac(t *testing.T) {
	cases := map[string]string{
		"00155DFF0A06":        "00:15:5d:ff:0a:06",
		"00-15-5D-FF-0A-06":   "00:15:5d:ff:0a:06",
		`"00:0c:29:db:9b:e6"`: "00:0c:29:db:9b:e6",
		" 00:0C:29:DB:9B:E6 ": "00:0c:29:db:9b:e6",
		"":                    "",
		"00:15:5d:ff:0a":      "",
		"zz:15:5d:ff:0a:06":   "",
	}
	for in, want := range cases {
		if got := normalizeVMac(in); got != want {
			t.Errorf("normalizeVMac(%q) = %q, want %q", in, got, want)
		}
	}
}

// VMware 首次开机后才会把 generatedAddress 写进 vmx，创建流程依赖这次回写
// 把真实 MAC 存进 vm_saving，否则 Agent 上报的 nic 参数匹配不到虚拟机。
func TestWorkstationSyncRealMACs(t *testing.T) {
	dir := t.TempDir()
	vmxPath := filepath.Join(dir, "hpv-test.vmx")
	vmx := "" +
		"ethernet0.present = \"TRUE\"\n" +
		"ethernet0.addressType = \"generated\"\n" +
		"ethernet0.generatedAddress = \"00:0c:29:db:9b:e6\"\n" +
		"ethernet1.present = \"TRUE\"\n" +
		"ethernet1.addressType = \"static\"\n" +
		"ethernet1.address = \"00:50:56:aa:bb:cc\"\n" +
		"ethernet1.generatedAddress = \"00:0c:29:11:22:33\"\n"
	if err := os.WriteFile(vmxPath, []byte(vmx), 0o644); err != nil {
		t.Fatalf("写入 vmx 失败: %v", err)
	}

	nics := map[string]model.NCConfig{
		"ethernet0": {MacAddr: "10:f6:0a:02:1e:64"},
		"ethernet1": {MacAddr: "10:f6:0a:02:1e:65"},
	}
	d := &WorkstationDriver{}
	d.wsSyncRealMACs(vmxPath, nics)

	if got := nics["ethernet0"].MacAddr; got != "00:0c:29:db:9b:e6" {
		t.Errorf("ethernet0 应取生成的 MAC，实际 %q", got)
	}
	// 显式 address 优先于 generatedAddress
	if got := nics["ethernet1"].MacAddr; got != "00:50:56:aa:bb:cc" {
		t.Errorf("ethernet1 应取静态 MAC，实际 %q", got)
	}
}

func TestWorkstationSyncRealMACsMissingFile(t *testing.T) {
	nics := map[string]model.NCConfig{"ethernet0": {MacAddr: "10:f6:0a:02:1e:64"}}
	d := &WorkstationDriver{}
	// 文件不存在时保持原值，不能把配置清空
	d.wsSyncRealMACs(filepath.Join(t.TempDir(), "nope.vmx"), nics)
	if got := nics["ethernet0"].MacAddr; got != "10:f6:0a:02:1e:64" {
		t.Errorf("文件缺失时不应改动配置，实际 %q", got)
	}
}
