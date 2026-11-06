package driver

import (
	"strings"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// 启动顺序必须包含光驱（ide2）：
// 否则用户挂载 ISO 后，PVE 的启动顺序里没有光盘，表现为“挂了盘却引导不到”。
func TestProxmoxBootOrderKeepsISO(t *testing.T) {
	d := NewProxmoxDriver(&model.HSConfig{})

	cases := []struct {
		name string
		cfg  *model.VMConfig
	}{
		{
			name: "已挂载光盘",
			cfg: &model.VMConfig{
				VmUUID: "vm-1",
				IsoAll: map[string]model.IMConfig{"firpe": {IsoName: "firpe", IsoFile: "a.iso"}},
				EfiAll: []model.BootOpts{{EfiType: false, EfiName: "vm-1"}},
			},
		},
		{
			name: "未挂载光盘（保留空光驱启动位）",
			cfg: &model.VMConfig{
				VmUUID: "vm-1",
				EfiAll: []model.BootOpts{{EfiType: false, EfiName: "vm-1"}},
			},
		},
		{
			name: "efi_all 为空",
			cfg:  &model.VMConfig{VmUUID: "vm-1"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := d.proxmoxBootOrder(c.cfg)
			if !strings.HasPrefix(got, "order=") {
				t.Fatalf("格式应为 order=...，实际 %q", got)
			}
			if !strings.Contains(got, "ide2") {
				t.Errorf("启动顺序应包含光驱 ide2，实际 %q", got)
			}
			if !strings.Contains(got, "ide0") {
				t.Errorf("启动顺序应包含系统盘 ide0，实际 %q", got)
			}
		})
	}
}
