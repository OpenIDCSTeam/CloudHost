package driver

import (
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestESXiDatastoreName 覆盖主机配置里各种存储路径写法的数据存储名解析。
//
// ESXi 主机状态此前完全不采集磁盘，前端「系统磁盘 / 数据磁盘」恒为未配置；
// 采集时按数据存储名匹配容量，解析错一个字符就会静默丢数据。
func TestESXiDatastoreName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"仅数据存储名", "datastore1", "datastore1"},
		{"数据存储根目录", "[datastore1]", "datastore1"},
		{"子目录", "[datastore1] images/", "datastore1"},
		{"已指向文件", "[datastore1] images/a.vmdk", "datastore1"},
		{"数据存储名含空格", "[My Datastore] ISO/", "My Datastore"},
		{"Shell 绝对路径", "/vmfs/volumes/datastore1/images", "datastore1"},
		{"空值", "  ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := esxiDatastoreName(tc.in); got != tc.want {
				t.Fatalf("esxiDatastoreName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestESXiRoleDatastores 主机配置的存储用途必须各自映射到正确的数据存储。
func TestESXiRoleDatastores(t *testing.T) {
	cfg := model.NewHSConfig()
	cfg.SystemPath = "[datastore1]"
	cfg.ExternPath = "datastore2"
	cfg.ImagesPath = "[datastore1] images/"
	cfg.DvdromPath = "[datastore1] images/"
	cfg.BackupPath = ""

	roles := esxiRoleDatastores(cfg)
	want := map[string]string{
		"system": "datastore1",
		"data":   "datastore2",
		"images": "datastore1",
		"iso":    "datastore1",
	}
	if len(roles) != len(want) {
		t.Fatalf("解析出 %d 个用途 %v, want %v", len(roles), roles, want)
	}
	for role, ds := range want {
		if roles[role] != ds {
			t.Fatalf("用途 %s 解析为 %q, want %q", role, roles[role], ds)
		}
	}
}

// TestESXiMapDatastoreUsage 只有数据存储实际存在时才写入容量，避免伪造读数。
func TestESXiMapDatastoreUsage(t *testing.T) {
	roles := map[string]string{
		"system": "datastore1",
		"images": "datastore1",
		"data":   "missing",
	}
	byName := map[string][]int{
		"datastore1": {1024 * 1024, 512 * 1024},
	}

	usage := esxiMapDatastoreUsage(roles, byName)
	if len(usage) != 2 {
		t.Fatalf("应映射 2 个用途，实际 %v", usage)
	}
	if got := usage["system"]; len(got) != 2 || got[0] != 1024*1024 || got[1] != 512*1024 {
		t.Fatalf("system 容量 = %v, want [1048576 524288]", got)
	}
	if got := usage["images"]; len(got) != 2 || got[0] != 1024*1024 {
		t.Fatalf("images 容量 = %v, want 与 system 相同", got)
	}
	if _, ok := usage["data"]; ok {
		t.Fatalf("不存在的数据存储不应写入容量: %v", usage["data"])
	}
}
