package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestWorkstationSystemDiskName 系统盘文件名必须与模板类型一致：
// 磁盘模板沿用扩展名（.vhd），ISO 模板与缺失模板回退 .vmdk
func TestWorkstationSystemDiskName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"win.vhd", "pe.iso"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := model.NewHSConfig()
	cfg.ImagesPath = dir
	d := NewWorkstationDriver(cfg)

	vm := model.NewVMConfig()
	vm.VmUUID = "vmw-1"

	vm.OsName = "win.vhd"
	if got := d.systemDiskName(vm); got != "vmw-1.vhd" {
		t.Fatalf("磁盘模板扩展名 = %q，期望 vmw-1.vhd", got)
	}
	if got := d.installISO(vm); got != "" {
		t.Fatalf("磁盘模板不应挂光驱，实际 %q", got)
	}

	vm.OsName = "pe.iso"
	if got := d.systemDiskName(vm); got != "vmw-1.vmdk" {
		t.Fatalf("ISO 模板应回退空盘名，实际 %q", got)
	}
	if got := d.installISO(vm); got == "" {
		t.Fatalf("ISO 模板应作为光驱镜像返回")
	}

	vm.OsName = "missing.vhd"
	if got := d.systemDiskName(vm); got != "vmw-1.vmdk" {
		t.Fatalf("模板缺失应回退 .vmdk，实际 %q", got)
	}
}

// TestWorkstationBuildVMXDiskReference VMX 必须引用真实磁盘名，
// 不能再硬编码 <uuid>.vmdk
func TestWorkstationBuildVMXDiskReference(t *testing.T) {
	cfg := model.NewHSConfig()
	d := NewWorkstationDriver(cfg)

	vm := model.NewVMConfig()
	vm.VmUUID = "vmw-1"
	vm.VcPort = "6901"

	vmx := d.buildVMX(vm, "vmw-1.vhd", `E:\images\pe.iso`)
	if !strings.Contains(vmx, `scsi0:0.fileName = "vmw-1.vhd"`) {
		t.Fatalf("VMX 未引用实际磁盘文件：\n%s", vmx)
	}
	// 云镜像必须 UEFI 引导；侧通道缓解需关闭（Hyper-V 宿主上否则明显掉性能）
	for _, want := range []string{
		`firmware = "efi"`,
		`uefi.secureBoot.enabled = "FALSE"`,
		`ulm.disableMitigations = "TRUE"`,
	} {
		if !strings.Contains(vmx, want) {
			t.Fatalf("VMX 缺少 %s：\n%s", want, vmx)
		}
	}
	if strings.Contains(vmx, `scsi0:0.fileName = "vmw-1.vmdk"`) {
		t.Fatalf("VMX 仍残留硬编码的 .vmdk 引用")
	}
	if !strings.Contains(vmx, `ide1:0.fileName = "E:\images\pe.iso"`) {
		t.Fatalf("VMX 未挂载安装 ISO：\n%s", vmx)
	}

	// 无 ISO 时不应出现光驱段
	plain := d.buildVMX(vm, "vmw-1.vmdk", "")
	if strings.Contains(plain, "ide1:0.present") {
		t.Fatalf("未提供 ISO 时不应写光驱配置")
	}
}

// TestUpdateVMXDiskFile 重装后同步 VMX 的磁盘引用
func TestUpdateVMXDiskFile(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "vmw-1.vmx")
	content := ".encoding = \"UTF-8\"\ndisplayName = \"vmw-1\"\nscsi0:0.fileName = \"vmw-1.vmdk\"\n"
	if err := os.WriteFile(vmx, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := readVMXDiskFile(vmx); got != "vmw-1.vmdk" {
		t.Fatalf("读取磁盘引用 = %q，期望 vmw-1.vmdk", got)
	}
	if err := updateVMXDiskFile(vmx, "vmw-1.vhd"); err != nil {
		t.Fatalf("更新磁盘引用失败: %v", err)
	}
	if got := readVMXDiskFile(vmx); got != "vmw-1.vhd" {
		t.Fatalf("更新后引用 = %q，期望 vmw-1.vhd", got)
	}
}
