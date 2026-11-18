// Package driver VMware Workstation 系统盘准备。
//
// VMX 里的 scsi0:0.fileName 是对系统盘的硬引用：文件不存在时 VMware 会直接
// 拒绝开机并提示
//
//	File not found: <uuid>.vmdk
//	This file is required to power on this virtual machine.
//
// 因此创建/重装虚拟机时必须先把系统盘落到 system_path：
//  1. 配置了模板镜像（images_path/<os_name>）时复制模板，沿用模板扩展名（.vhd/.vmdk）；
//  2. 没有模板（或模板是 .iso 安装镜像）时创建等容量空 VMDK。
package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// defaultVMDiskSizeMB 未指定容量时的系统盘大小
const defaultVMDiskSizeMB = 20480

// vmwareToolBin 解析 VMware 自带命令行工具的路径（vmware-vdiskmanager 等）。
// 与 vmrest 一致：优先 launch_path，其次 PATH，最后默认安装目录。
func (d *WorkstationDriver) vmwareToolBin(name string) string {
	launch := strings.TrimSpace(d.Config.LaunchPath)
	if launch != "" {
		if info, err := os.Stat(launch); err == nil {
			dir := launch
			if !info.IsDir() {
				dir = filepath.Dir(launch)
			}
			if p := findExecutable(dir, name); p != "" {
				return p
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range vmwareInstallDirs() {
		if p := findExecutable(dir, name); p != "" {
			return p
		}
	}
	return ""
}

// templateImage 返回系统模板镜像的绝对路径（不存在时返回空串）
func (d *WorkstationDriver) templateImage(osName string) string {
	images := strings.TrimSpace(d.Config.ImagesPath)
	osName = strings.TrimSpace(osName)
	if images == "" || osName == "" {
		return ""
	}
	p := filepath.Join(images, osName)
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p
	}
	return ""
}

// isDiskTemplate 判断模板是否可以当作系统盘直接复制（ISO 安装镜像不算）
func isDiskTemplate(path string) bool {
	return path != "" && !strings.EqualFold(filepath.Ext(path), ".iso")
}

// systemDiskName 返回系统盘文件名（相对 VMX 所在目录）。
// 有磁盘模板时沿用模板扩展名，否则用 .vmdk。
func (d *WorkstationDriver) systemDiskName(cfg *model.VMConfig) string {
	if template := d.templateImage(cfg.OsName); isDiskTemplate(template) {
		if ext := strings.ToLower(filepath.Ext(template)); ext != "" {
			return cfg.VmUUID + ext
		}
	}
	return cfg.VmUUID + ".vmdk"
}

// installISO 返回需要挂载到光驱的安装镜像（仅当配置的模板就是 ISO 时）
func (d *WorkstationDriver) installISO(cfg *model.VMConfig) string {
	template := d.templateImage(cfg.OsName)
	if template != "" && strings.EqualFold(filepath.Ext(template), ".iso") {
		return template
	}
	return ""
}

// prepareSystemDisk 准备系统盘，返回 VMX 中应引用的磁盘文件名。
// force=true 表示重建（重装系统），会先删除旧盘再复制/创建。
func (d *WorkstationDriver) prepareSystemDisk(cfg *model.VMConfig, dir string, force bool) (diskName string, warn string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}

	template := d.templateImage(cfg.OsName)
	diskName = cfg.VmUUID + ".vmdk"
	if isDiskTemplate(template) {
		diskName = cfg.VmUUID + strings.ToLower(filepath.Ext(template))
	}
	dst := filepath.Join(dir, diskName)

	// 幂等：盘已存在且非重建时直接复用
	if !force {
		if info, statErr := os.Stat(dst); statErr == nil && !info.IsDir() {
			return diskName, "", nil
		}
	}

	if isDiskTemplate(template) {
		if force {
			_ = os.Remove(dst)
		}
		if copyErr := copyVMDisk(template, dst); copyErr != nil {
			return "", "", fmt.Errorf("复制系统模板失败: %w", copyErr)
		}
		if extendErr := d.extendVMDisk(dst, cfg.HddNum); extendErr != nil {
			// 扩容失败不阻断：模板本身可启动，容量可后续手动扩展
			warn = "系统盘已复制，但未能扩容: " + extendErr.Error()
		}
		return diskName, warn, nil
	}

	// 没有磁盘模板（含模板是 ISO 的情况）：创建等容量空盘，
	// 保证虚拟机至少能进入固件/安装界面，而不是开机即报缺盘。
	if force {
		_ = os.Remove(dst)
	}
	if createErr := d.createBlankVMDisk(dst, cfg.HddNum); createErr != nil {
		return "", "", fmt.Errorf("创建系统盘失败: %w", createErr)
	}
	if template == "" {
		warn = "未配置可用的系统模板镜像，已创建空盘"
	}
	return diskName, warn, nil
}

// copyVMDisk 复制虚拟磁盘文件（大文件走系统复制命令，避免内存中转）
func copyVMDisk(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		ps := fmt.Sprintf("Copy-Item -LiteralPath '%s' -Destination '%s' -Force -ErrorAction Stop",
			psEscape(src), psEscape(dst))
		if out, _, err := security.SafeExecWithTimeout(
			[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", ps}, 60*time.Minute); err != nil {
			return fmt.Errorf("%v (%s)", err, strings.TrimSpace(out))
		}
		return nil
	}
	if out, _, err := security.SafeExecWithTimeout([]string{"cp", "-f", src, dst}, 60*time.Minute); err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// createBlankVMDisk 创建空 VMDK（-t 0 单文件稀疏盘，-a lsilogic 与 VMX 控制器一致）
func (d *WorkstationDriver) createBlankVMDisk(path string, sizeMB int) error {
	if sizeMB <= 0 {
		sizeMB = defaultVMDiskSizeMB
	}
	bin := d.vmwareToolBin("vmware-vdiskmanager")
	if bin == "" {
		return fmt.Errorf("未找到 vmware-vdiskmanager，请检查 VMware Workstation 安装路径或 launch_path 配置")
	}
	out, _, err := security.SafeExecWithTimeout(
		[]string{bin, "-c", "-s", fmt.Sprintf("%dMB", sizeMB), "-a", "lsilogic", "-t", "0", path},
		30*time.Minute)
	if err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// extendVMDisk 把系统盘扩展到目标容量。模板镜像通常远小于申请容量。
// VMDK 走 vmware-vdiskmanager；VHD/VHDX 走 Windows 自身的 Hyper-V cmdlet 或 diskpart。
func (d *WorkstationDriver) extendVMDisk(path string, sizeMB int) error {
	if sizeMB <= 0 {
		return nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".vhd", ".vhdx":
		return d.resizeVirtualHardDisk(path, sizeMB)
	case ".vmdk":
		return d.expandVMDK(path, sizeMB)
	default:
		return fmt.Errorf("%s 格式不支持扩容", filepath.Ext(path))
	}
}

// expandVMDK 用 vmware-vdiskmanager -x 扩展 VMDK
func (d *WorkstationDriver) expandVMDK(path string, sizeMB int) error {
	sizeGB := sizeMB / 1024
	if sizeGB <= 0 {
		return nil
	}
	bin := d.vmwareToolBin("vmware-vdiskmanager")
	if bin == "" {
		return fmt.Errorf("未找到 vmware-vdiskmanager")
	}
	out, _, err := security.SafeExecWithTimeout(
		[]string{bin, "-x", fmt.Sprintf("%dGB", sizeGB), path}, 30*time.Minute)
	if err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// resizeVirtualHardDisk 扩展 VHD/VHDX。
//
// vmware-vdiskmanager 的 -x 明确不支持 VHD（报 "One of the parameters supplied
// is invalid"），必须使用 Windows 自己的能力：
//  1. Hyper-V PowerShell 的 Resize-VHD（-SizeBytes 为目标总容量）；
//  2. 回退系统自带的 diskpart：select vdisk + expand vdisk maximum。
func (d *WorkstationDriver) resizeVirtualHardDisk(path string, sizeMB int) error {
	targetBytes := int64(sizeMB) * 1024 * 1024

	// 已经不小于目标容量时直接返回，避免 Resize-VHD 报「不能缩小」
	if cur, ok := d.vhdSizeBytes(path); ok && cur >= targetBytes {
		return nil
	}

	ps := fmt.Sprintf("Resize-VHD -Path '%s' -SizeBytes %d -ErrorAction Stop",
		psEscape(path), targetBytes)
	psOut, _, psErr := security.SafeExecWithTimeout(
		[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", ps}, 30*time.Minute)
	if psErr == nil {
		return nil
	}

	// 回退 diskpart（系统自带，不依赖 Hyper-V 模块）
	script := fmt.Sprintf("select vdisk file=\"%s\"\r\nexpand vdisk maximum=%d\r\nexit\r\n", path, sizeMB)
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("opidcs-vhd-%d.txt", time.Now().UnixNano()))
	if writeErr := os.WriteFile(tmp, []byte(script), 0o600); writeErr != nil {
		return fmt.Errorf("Resize-VHD 失败（%s），写 diskpart 脚本也失败: %v",
			strings.TrimSpace(psOut), writeErr)
	}
	defer os.Remove(tmp)

	dpOut, _, dpErr := security.SafeExecWithTimeout([]string{"diskpart", "/s", tmp}, 30*time.Minute)
	if dpErr != nil {
		return fmt.Errorf("Resize-VHD 失败（%s），diskpart 也失败（%v: %s）",
			strings.TrimSpace(psOut), dpErr, strings.TrimSpace(dpOut))
	}
	// diskpart 脚本出错时退出码可能仍为 0，只能按输出关键字判断
	lower := strings.ToLower(dpOut)
	if strings.Contains(lower, "error") || strings.Contains(dpOut, "错误") {
		return fmt.Errorf("diskpart 扩容失败: %s", strings.TrimSpace(dpOut))
	}
	return nil
}

// vhdSizeBytes 查询 VHD/VHDX 的逻辑容量（失败时 ok=false）
func (d *WorkstationDriver) vhdSizeBytes(path string) (int64, bool) {
	out, _, err := security.SafeExecWithTimeout([]string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("(Get-VHD -Path '%s').Size", psEscape(path))}, 2*time.Minute)
	if err != nil {
		return 0, false
	}
	n, parseErr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if parseErr != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// diskBus 返回系统盘应使用的控制器。
//
// 镜像不一定是在 VMware 里制作的：Windows 精简镜像通常只保留安装时的存储驱动，
// 控制器选错会在开机时报 INACCESSIBLE_BOOT_DEVICE(0x7B) 蓝屏。因此：
//   - 镜像条目显式配置了 disk_bus 时以配置为准；
//   - 否则按模板扩展名推断：.vhd/.vhdx（Hyper-V/Azure 导出）→ sata（AHCI，
//     Windows 内置 storahci 驱动）；.vmdk（VMware 制作）→ scsi；
//   - 空盘/ISO 安装 → sata（Windows 安装介质自带 AHCI 驱动，兼容性最好）。
func (d *WorkstationDriver) diskBus(cfg *model.VMConfig, diskName string) string {
	if bus := d.templateDiskBus(cfg.OsName); bus != "" {
		return bus
	}
	switch strings.ToLower(filepath.Ext(diskName)) {
	case ".vhd", ".vhdx":
		return "sata"
	case ".vmdk":
		return "scsi"
	default:
		return "sata"
	}
}

// templateDiskBus 读取镜像条目中显式配置的磁盘控制器
func (d *WorkstationDriver) templateDiskBus(osName string) string {
	name := strings.TrimSpace(osName)
	if name == "" {
		return ""
	}
	for _, item := range d.Config.ImagesMaps {
		if !strings.EqualFold(strings.TrimSpace(item.SysFile), name) &&
			!strings.EqualFold(strings.TrimSpace(item.SysName), name) {
			continue
		}
		switch bus := strings.ToLower(strings.TrimSpace(item.DiskBus)); bus {
		case "sata", "scsi", "ide":
			return bus
		}
	}
	return ""
}

// diskControllerLines 生成系统盘控制器与磁盘条目的 VMX 片段
func diskControllerLines(bus, diskName string) string {
	switch bus {
	case "sata":
		// AHCI：Windows 7+ 内置 storahci，Hyper-V/Azure 导出的 VHD 走这条
		return fmt.Sprintf("sata0.present = \"TRUE\"\n"+
			"sata0:0.present = \"TRUE\"\n"+
			"sata0:0.fileName = \"%s\"\n", diskName)
	case "ide":
		return fmt.Sprintf("ide0:0.present = \"TRUE\"\n"+
			"ide0:0.fileName = \"%s\"\n"+
			"ide0:0.deviceType = \"disk\"\n", diskName)
	default: // scsi（lsilogic）：VMware 制作的 VMDK
		return fmt.Sprintf("scsi0.present = \"TRUE\"\n"+
			"scsi0.virtualDev = \"lsilogic\"\n"+
			"scsi0:0.present = \"TRUE\"\n"+
			"scsi0:0.fileName = \"%s\"\n"+
			"scsi0:0.deviceType = \"scsi-hardDisk\"\n", diskName)
	}
}

// diskFileKey 返回指定控制器下系统盘文件名在 VMX 中的键名
func diskFileKey(bus string) string {
	switch bus {
	case "sata":
		return "sata0:0.fileName"
	case "ide":
		return "ide0:0.fileName"
	default:
		return "scsi0:0.fileName"
	}
}

// readVMXDiskBus 读取 VMX 当前系统盘使用的控制器（无法判断时按 scsi）
func readVMXDiskBus(vmxPath string) string {
	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return "scsi"
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		i := strings.Index(trimmed, "=")
		if i <= 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(trimmed[:i])) {
		case "sata0:0.filename":
			return "sata"
		case "ide0:0.filename":
			return "ide"
		case "scsi0:0.filename":
			return "scsi"
		}
	}
	return "scsi"
}

// readVMXDiskFile 读取 VMX 当前引用的系统盘文件名
func readVMXDiskFile(vmxPath string) string {
	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		if key == "scsi0:0.filename" || key == "sata0:0.filename" || key == "ide0:0.filename" {
			return strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		}
	}
	return ""
}

// updateVMXDiskFile 把 VMX 中当前控制器的系统盘引用更新为指定磁盘
func updateVMXDiskFile(vmxPath, diskName string) error {
	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return err
	}
	key := diskFileKey(readVMXDiskBus(vmxPath))
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		i2 := strings.Index(trimmed, "=")
		if i2 <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(trimmed[:i2]), key) {
			lines[i] = fmt.Sprintf("%s = \"%s\"", key, diskName)
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, fmt.Sprintf("%s = \"%s\"", key, diskName))
	}
	return os.WriteFile(vmxPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// SetDiskBus 切换系统盘控制器（sata/scsi/ide）。
//
// 镜像与控制器不匹配会让 Windows 在开机时报 INACCESSIBLE_BOOT_DEVICE(0x7B)
// 蓝屏；此时不必重建虚拟机，切一次控制器即可。流程：关机 → 改写 VMX → 开机。
//
// 注意不能用 vmrest 的 DELETE 来「刷新」：DELETE /api/vms/{id} 会把 VMX 与
// 磁盘文件一起删掉（等于删除虚拟机），只能在关机状态下直接改 VMX 再开机，
// vmrest 的 power on 会重新读取配置文件。
func (d *WorkstationDriver) SetDiskBus(vmUUID, bus string) (*model.ZMessage, error) {
	bus = strings.ToLower(strings.TrimSpace(bus))
	switch bus {
	case "sata", "scsi", "ide":
	default:
		return &model.ZMessage{Success: false, Action: "VMDiskBus",
			Message: fmt.Sprintf("不支持的磁盘控制器: %s（可选 sata/scsi/ide）", bus)}, nil
	}

	vmx := d.wsVMXPath(vmUUID)
	if vmx == "" {
		vmx = d.vmxPath(vmUUID)
	}
	if _, err := os.Stat(vmx); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDiskBus",
			Message: "虚拟机配置文件不存在: " + vmx}, nil
	}
	if readVMXDiskBus(vmx) == bus {
		return &model.ZMessage{Success: true, Action: "VMDiskBus",
			Message: fmt.Sprintf("系统盘已经使用 %s 控制器", bus)}, nil
	}

	// 运行中的虚拟机必须先关机，否则 VMware 不释放磁盘锁
	if state := d.GetPower(vmUUID); state == "运行中" || state == "已暂停" {
		if _, err := d.PowerControl(vmUUID, model.VMPowerHClose); err != nil {
			return &model.ZMessage{Success: false, Action: "VMDiskBus",
				Message: "关闭虚拟机失败: " + err.Error()}, nil
		}
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if d.GetPower(vmUUID) == "已关机" {
				break
			}
			time.Sleep(time.Second)
		}
	}

	if err := switchVMXDiskBus(vmx, bus); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDiskBus",
			Message: "修改 VMX 失败: " + err.Error()}, nil
	}

	if _, err := d.PowerControl(vmUUID, model.VMPowerSStart); err != nil {
		return &model.ZMessage{Success: true, Action: "VMDiskBus",
			Message: fmt.Sprintf("已切换到 %s 控制器，但自动开机失败: %v", bus, err)}, nil
	}
	return &model.ZMessage{Success: true, Action: "VMDiskBus",
		Message: fmt.Sprintf("已切换到 %s 控制器并重新开机", bus)}, nil
}

// switchVMXDiskBus 把系统盘从当前控制器迁移到目标控制器（保留其它 VMX 配置）
func switchVMXDiskBus(vmxPath, bus string) error {
	diskName := readVMXDiskFile(vmxPath)
	if diskName == "" {
		return fmt.Errorf("VMX 中未找到系统盘配置: %s", vmxPath)
	}
	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return err
	}

	// 只清理系统盘（*:0）相关的键，保留数据盘与其它控制器
	skip := map[string]bool{
		"scsi0.present": true, "scsi0.virtualdev": true,
		"scsi0:0.present": true, "scsi0:0.filename": true, "scsi0:0.devicetype": true,
		"sata0.present":   true,
		"sata0:0.present": true, "sata0:0.filename": true, "sata0:0.devicetype": true,
		"ide0:0.present": true, "ide0:0.filename": true, "ide0:0.devicetype": true,
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if i := strings.Index(trimmed, "="); i > 0 && skip[strings.ToLower(strings.TrimSpace(trimmed[:i]))] {
			continue
		}
		kept = append(kept, line)
	}
	text := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	text += "\n" + diskControllerLines(bus, diskName)
	return os.WriteFile(vmxPath, []byte(text), 0o644)
}
