package driver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestParseVBoxVMStateCRLF VBoxManage 在 Windows 上的输出是 CRLF，
// 行尾的 \r 曾让电源态恒为「未知」，这里固化为回归用例。
func TestParseVBoxVMStateCRLF(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"运行中", "Name=\"vm\"\r\nVMState=\"running\"\r\n", "运行中"},
		{"已关机", "VMState=\"poweroff\"\r\n", "已关机"},
		{"已暂停", "VMState=\"paused\"\r\n", "已暂停"},
		{"已保存", "VMState=\"saved\"\r\n", "已暂停"},
		{"未知状态值", "VMState=\"gurumeditation\"\r\n", "未知"},
		{"无状态字段", "Name=\"vm\"\r\n", "未知"},
	}
	for _, tc := range cases {
		if got := parseVBoxVMState(tc.out); got != tc.want {
			t.Fatalf("%s: parseVBoxVMState = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestParseVBoxVRDEPort 运行中的虚拟机会用 -1 表示「未显式指定端口」，
// 真实端口在 vrdeproperty[TCP/Ports] 里。
func TestParseVBoxVRDEPort(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"运行中取 TCP/Ports", "vrdeport=-1\r\nvrdeproperty[TCP/Ports]=\"40001\"\r\n", 40001},
		{"关机态取 vrdeport", "vrdeport=\"6905\"\r\n", 6905},
		{"未配置端口", "vrdeport=\"-1\"\r\n", 0},
		{"无字段", "Name=\"vm\"\r\n", 0},
	}
	for _, tc := range cases {
		if got := parseVBoxVRDEPort(tc.out); got != tc.want {
			t.Fatalf("%s: parseVBoxVRDEPort = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestVBoxOsType 镜像名到 ostype 的映射（影响 Windows 镜像的默认硬件）。
func TestVBoxOsType(t *testing.T) {
	cases := map[string]string{
		"win11-22h2.vdi":   "Windows11_64",
		"Windows10.vdi":    "Windows10_64",
		"win2022.iso":      "Windows2022_64",
		"ubuntu-24.04.vdi": "Linux26_64",
		"":                 "Linux26_64",
	}
	for in, want := range cases {
		if got := vboxOsType(in); got != want {
			t.Fatalf("vboxOsType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNicSignature nicSignature 决定「网卡是否变化」，
// 变化漏判会让改网卡静默失效，误判会让无意义的 modifyvm 失败。
func TestNicSignature(t *testing.T) {
	base := &model.VMConfig{NicAll: map[string]model.NCConfig{
		"nic0": {NicName: "nic0", NicType: "nat", MacAddr: "08:00:27:AA:BB:CC"},
		"nic1": {NicName: "nic1", NicType: "pub", MacAddr: "08:00:27:11:22:33"},
	}}
	same := &model.VMConfig{NicAll: map[string]model.NCConfig{
		"nic1": {NicName: "nic1", NicType: "pub", MacAddr: "08:0027:11:22:33"},
		"nic0": {NicName: "nic0", NicType: "nat", MacAddr: "080027aabbcc"},
	}}
	if nicSignature(base) != nicSignature(same) {
		t.Fatal("MAC 大小写/分隔符差异不应被视为网卡变化")
	}

	changed := &model.VMConfig{NicAll: map[string]model.NCConfig{
		"nic0": {NicName: "nic0", NicType: "pub", MacAddr: "08:00:27:AA:BB:CC"},
	}}
	if nicSignature(base) == nicSignature(changed) {
		t.Fatal("网卡类型或数量变化必须被识别")
	}
	if nicSignature(nil) != "" || nicSignature(&model.VMConfig{}) != "" {
		t.Fatal("无网卡时指纹应为空串")
	}
}

// TestVBoxBackupNaming 备份克隆机的命名规则与归属反解。
//
// 命名必须满足两点：扫描纳管时能被识别为备份（IsVBoxBackupName），
// 还原/删除时能反解出所属虚拟机（防止 A 的备份写进 B）。
func TestVBoxBackupNaming(t *testing.T) {
	name := vboxBackupName("web-01")
	if !IsVBoxBackupName(name) {
		t.Fatalf("备份名 %q 未被识别为备份", name)
	}
	if owner := vboxBackupOwner(name); owner != "web-01" {
		t.Fatalf("vboxBackupOwner(%q) = %q, want web-01", name, owner)
	}
	if IsVBoxBackupName("web-01") {
		t.Fatal("普通虚拟机名不应被识别为备份")
	}
	if IsVBoxBackupName("bak-") {
		// 只有前缀没有主机名时仍算备份名，但反解不出归属
		if vboxBackupOwner("bak-") != "" {
			t.Fatal("缺少归属标记时不应反解出虚拟机名")
		}
	}
	if vboxBackupOwner("bak-web-01-backup-20240101-000000") != "web-01" {
		t.Fatal("带连字符的虚拟机名反解失败")
	}
}

// TestVBoxISOPath ISO 路径解析：前端可能只给文件名。
func TestVBoxISOPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	d := NewVirtualBoxDriver(testConfig(t, "VirtualBoxs"))
	d.Config.ImagesPath = dir
	d.Config.DvdromPath = ""

	path, err := d.vboxISOPath(&model.IMConfig{IsoName: "iso1", IsoFile: "test.iso", IsoPath: "test.iso"})
	if err != nil {
		t.Fatalf("按镜像目录解析失败: %v", err)
	}
	if path != filepath.Join(dir, "test.iso") {
		t.Fatalf("解析结果 = %q", path)
	}

	abs := filepath.Join(dir, "test.iso")
	if path, err := d.vboxISOPath(&model.IMConfig{IsoPath: abs}); err != nil || path != abs {
		t.Fatalf("绝对路径解析失败: %q %v", path, err)
	}

	if _, err := d.vboxISOPath(&model.IMConfig{IsoFile: "missing.iso"}); err == nil {
		t.Fatal("文件不存在时应报错")
	}
}
