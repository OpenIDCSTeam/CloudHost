package driver

import (
	"strings"
	"testing"

	"github.com/luthermonson/go-proxmox"
)

// 声卡是「有最好、没有也能跑」的配置：
// 老版本 PVE 拒绝 audio0 时必须能把它剔除后重试，否则整台虚拟机创建失败。
func TestFilterVMOptions(t *testing.T) {
	opts := []proxmox.VirtualMachineOption{
		{Name: "name", Value: "vm1"},
		{Name: "audio0", Value: pveAudioDevice},
		{Name: "boot", Value: "order=ide0;ide2"},
	}

	filtered := filterVMOptions(opts, "audio0")
	if len(filtered) != 2 {
		t.Fatalf("应剩 2 项，实际 %d", len(filtered))
	}
	for _, o := range filtered {
		if o.Name == "audio0" {
			t.Error("audio0 应被剔除")
		}
	}
	if filtered[0].Name != "name" || filtered[1].Name != "boot" {
		t.Errorf("其余项顺序应保持不变，实际 %v", filtered)
	}

	// 剔除不存在的项不应改动原切片内容
	if got := filterVMOptions(opts, "hostpci0"); len(got) != len(opts) {
		t.Errorf("剔除不存在的项后长度应不变，实际 %d", len(got))
	}
}

// 声卡配置必须同时包含型号与后端，否则 PVE 会以参数校验失败拒绝。
func TestPVEAudioDeviceFormat(t *testing.T) {
	if !strings.Contains(pveAudioDevice, "device=") {
		t.Errorf("缺少 device=: %s", pveAudioDevice)
	}
	if !strings.Contains(pveAudioDevice, "driver=") {
		t.Errorf("缺少 driver=: %s", pveAudioDevice)
	}
}
