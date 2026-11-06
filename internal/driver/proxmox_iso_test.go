package driver

import (
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// PVE 的 ISO 卷 ID 必须是 storage:iso/<file>。
// 主机配置里的 dvdrom_path 可能是 "local"，也可能已经是 "local:iso"；
// 后者若再拼一次 ":iso/" 会得到 "local:iso:iso/x.iso"，PVE 直接拒绝，
// 表现为「点了挂载但没挂上」。
func TestProxmoxISOVolume(t *testing.T) {
	cases := []struct {
		name    string
		storage string
		iso     *model.IMConfig
		want    string
	}{
		{"仅storage名", "local", &model.IMConfig{IsoFile: "a.iso"}, "local:iso/a.iso"},
		{"storage已含iso", "local:iso", &model.IMConfig{IsoFile: "a.iso"}, "local:iso/a.iso"},
		{"未配置storage", "", &model.IMConfig{IsoFile: "a.iso"}, "local:iso/a.iso"},
		{"文件自带iso前缀", "local:iso", &model.IMConfig{IsoFile: "iso/a.iso"}, "local:iso/a.iso"},
		{"文件已是完整卷ID", "local:iso", &model.IMConfig{IsoFile: "local:iso/a.iso"}, "local:iso/a.iso"},
		{"仅iso_path完整卷ID", "local:iso", &model.IMConfig{IsoPath: "local:iso/a.iso"}, "local:iso/a.iso"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := proxmoxISOVolume(c.storage, c.iso); got != c.want {
				t.Errorf("proxmoxISOVolume(%q, %+v) = %q，期望 %q", c.storage, c.iso, got, c.want)
			}
		})
	}
}
