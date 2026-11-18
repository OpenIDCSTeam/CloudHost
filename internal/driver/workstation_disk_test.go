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

	// VHD 走 SATA（Hyper-V 导出的镜像只带 AHCI 驱动）
	vmx := d.buildVMX(vm, "vmw-1.vhd", `E:\images\pe.iso`)
	if !strings.Contains(vmx, `sata0:0.fileName = "vmw-1.vhd"`) {
		t.Fatalf("VHD 应使用 SATA 控制器：\n%s", vmx)
	}
	if strings.Contains(vmx, "scsi0:0.present") {
		t.Fatalf("VHD 不应同时挂 SCSI 控制器：\n%s", vmx)
	}

	// VMDK 走 SCSI
	vmxVmdk := d.buildVMX(vm, "vmw-1.vmdk", "")
	if !strings.Contains(vmxVmdk, `scsi0:0.fileName = "vmw-1.vmdk"`) {
		t.Fatalf("VMDK 应使用 SCSI 控制器：\n%s", vmxVmdk)
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

	// 无 ISO 时仍常驻空光驱（虚拟硬件里必须有 CD/DVD 驱动器）
	plain := d.buildVMX(vm, "vmw-1.vmdk", "")
	if !strings.Contains(plain, "ide1:0.present") {
		t.Fatalf("VMX 缺少 CD/DVD 驱动器：\n%s", plain)
	}
	if strings.Contains(plain, "ide1:0.fileName") {
		t.Fatalf("未提供 ISO 时空光驱不应写 fileName：\n%s", plain)
	}
	if !strings.Contains(plain, `ide1:0.deviceType = "cdrom-raw"`) {
		t.Fatalf("空光驱应使用 cdrom-raw：\n%s", plain)
	}

	// 声卡必须常驻，否则客户机没有声音设备
	for _, want := range []string{
		`sound.present = "TRUE"`,
		`sound.virtualDev = "hdaudio"`,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("VMX 缺少 %s：\n%s", want, plain)
		}
	}

	// USB 控制器必须常驻，否则 USB 直通没有承载设备
	for _, want := range []string{
		`usb.present = "TRUE"`,
		`usb_xhci.present = "TRUE"`,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("VMX 缺少 %s：\n%s", want, plain)
		}
	}
}

// TestWorkstationTemplateDiskBus 镜像条目可显式覆盖控制器（镜像来源特殊时使用）
func TestWorkstationTemplateDiskBus(t *testing.T) {
	cfg := model.NewHSConfig()
	cfg.ImagesMaps = []model.OSConfig{
		{SysName: "Win10", SysFile: "win.vhd", SysFlag: true, DiskBus: "scsi"},
	}
	d := NewWorkstationDriver(cfg)

	vm := model.NewVMConfig()
	vm.VmUUID = "vmw-1"
	vm.OsName = "win.vhd"

	if got := d.diskBus(vm, "vmw-1.vhd"); got != "scsi" {
		t.Fatalf("模板显式配置 scsi 时应覆盖扩展名推断，实际 %q", got)
	}
	cfg.ImagesMaps[0].DiskBus = ""
	if got := d.diskBus(vm, "vmw-1.vhd"); got != "sata" {
		t.Fatalf("未配置时应按扩展名推断为 sata，实际 %q", got)
	}
}

// TestSwitchVMXDiskBus 控制器迁移：保留磁盘引用与其它配置，可双向切换
func TestSwitchVMXDiskBus(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "vmw-1.vmx")
	content := strings.Join([]string{
		".encoding = \"UTF-8\"",
		"displayName = \"vmw-1\"",
		"firmware = \"efi\"",
		"scsi0.present = \"TRUE\"",
		"scsi0.virtualDev = \"lsilogic\"",
		"scsi0:0.present = \"TRUE\"",
		"scsi0:0.fileName = \"vmw-1.vhd\"",
		"scsi0:0.deviceType = \"scsi-hardDisk\"",
		"ethernet0.present = \"TRUE\"",
	}, "\n") + "\n"
	if err := os.WriteFile(vmx, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := readVMXDiskBus(vmx); got != "scsi" {
		t.Fatalf("初始控制器 = %q，期望 scsi", got)
	}
	if err := switchVMXDiskBus(vmx, "sata"); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if got := readVMXDiskBus(vmx); got != "sata" {
		t.Fatalf("切换后控制器 = %q，期望 sata", got)
	}
	if got := readVMXDiskFile(vmx); got != "vmw-1.vhd" {
		t.Fatalf("磁盘引用应保持 vmw-1.vhd，实际 %q", got)
	}

	data, _ := os.ReadFile(vmx)
	text := string(data)
	if strings.Contains(text, "scsi0:0.present") {
		t.Fatalf("切换后不应残留 SCSI 磁盘条目：\n%s", text)
	}
	for _, want := range []string{`firmware = "efi"`, `ethernet0.present`, `sata0:0.fileName = "vmw-1.vhd"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("切换后丢失配置 %s：\n%s", want, text)
		}
	}

	// 切回 SCSI
	if err := switchVMXDiskBus(vmx, "scsi"); err != nil {
		t.Fatalf("切回失败: %v", err)
	}
	if got := readVMXDiskBus(vmx); got != "scsi" {
		t.Fatalf("切回后控制器 = %q，期望 scsi", got)
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
