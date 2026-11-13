// Package driver Hyper-V 驱动生命周期实现（PowerShell，仅 Windows）
// 与 Python 版 HostServer/Win64HyperV.py 的功能语义对齐。
package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// execPS 执行 PowerShell 命令
//
// 统一前置 UTF-8 输出设置，避免中文错误信息变成乱码；
// 权限不足时在 error 里追加可执行的处置建议，调用方直接展示即可。
func (d *HyperVDriver) execPS(script string) (string, error) {
	return d.execPSTimeout(script, 2*time.Minute)
}

// execPSTimeout 同 execPS，但允许指定超时。
//
// 镜像转换/复制这类操作按 GB 计，2 分钟远远不够，
// 超时被杀会留下半截系统盘，反而更难排查。
func (d *HyperVDriver) execPSTimeout(script string, timeout time.Duration) (string, error) {
	out, _, err := security.SafeExecWithTimeout(
		[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", psUTF8Prelude + script},
		timeout)
	if err != nil && psHasPermissionDenied(out) {
		return out, fmt.Errorf("%w（HostAgent 进程缺少 Hyper-V 管理权限：请以管理员身份运行，"+
			"或把运行账户加入本地组 Hyper-V Administrators 后重启进程）", err)
	}
	return out, err
}

// hvDiskDir 返回 Hyper-V 磁盘目录
func (d *HyperVDriver) hvDiskDir() string {
	if d.Config.SystemPath != "" {
		return d.Config.SystemPath
	}
	return "C:\\Hyper-V\\Disks"
}

// GetStatus 获取 Hyper-V 主机状态。
//
// 与 Python 版 Win64HyperV.HSStatus 一致：
//  1. 先用本机采集拿到完整基础数据（磁盘、网络等）
//  2. 再通过 Hyper-V 向宿主机查询 CPU / 内存总量并覆盖（虚拟化层视角更准确）
//
// 原实现直接走基类的本机采集，缺少第 2 步的 Hyper-V 覆盖。
// 查询失败时保留本机数据，不阻断状态上报。
func (d *HyperVDriver) GetStatus() (*model.HWStatus, error) {
	hw := localHWStatus(d.Config)

	// 内存单位：MemoryCapacity 为字节；CPU 取逻辑处理器数
	script := `$ErrorActionPreference='SilentlyContinue'; ` +
		`try { $h = Get-VMHost -ErrorAction Stop; ` +
		`[pscustomobject]@{ ` +
		`mem_total=[math]::Round($h.MemoryCapacity/1MB); ` +
		`cpu_total=$h.LogicalProcessorCount ` +
		`} | ConvertTo-Json -Compress } catch { '' }`

	out, err := d.execPS(script)
	if err != nil {
		return hw, nil
	}
	body := strings.TrimSpace(out)
	if body == "" {
		return hw, nil
	}
	var parsed struct {
		MemTotal int `json:"mem_total"`
		CpuTotal int `json:"cpu_total"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return hw, nil
	}
	// 仅在查询到有效值时覆盖（与 Python 一致：连接失败则用本机数据）
	if parsed.MemTotal > 0 {
		hw.MemTotal = parsed.MemTotal
		if hw.MemUsage > hw.MemTotal {
			hw.MemUsage = 0
		}
	}
	if parsed.CpuTotal > 0 {
		hw.CpuTotal = parsed.CpuTotal
	}
	return hw, nil
}

// ScanVMs 扫描并同步虚拟机列表
//
// 与 Python 版 Win64HyperV.VMDetect 对齐：使用主机配置的 filter_name
// 作为名称前缀过滤，只纳管带该前缀的虚拟机。
func (d *HyperVDriver) ScanVMs() (*model.ZMessage, error) {
	vms, err := d.GetVMList()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDetect", Message: err.Error()}, nil
	}

	// 前缀过滤（未配置 filter_name 时不过滤）
	prefix := d.Config.FilterName
	d.mu.Lock()
	scanned := 0
	added := 0
	for id, cfg := range vms {
		if prefix != "" && (cfg == nil || !strings.HasPrefix(cfg.OsName, prefix)) {
			continue
		}
		scanned++
		if _, exists := d.VmSaving[id]; !exists {
			d.VmSaving[id] = cfg
			added++
		} else {
			d.VmSaving[id].VmFlag = cfg.VmFlag
		}
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDetect",
		Message: fmt.Sprintf("扫描完成：匹配 %d 个，新增 %d 个", scanned, added)}, nil
}

// PowerControl 电源控制
func (d *HyperVDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	var verb string
	switch action {
	case model.VMPowerSStart:
		verb = "Start-VM"
	case model.VMPowerSClose:
		verb = "Stop-VM"
	case model.VMPowerHClose:
		verb = "Stop-VM -Force -TurnOff"
	case model.VMPowerSReset, model.VMPowerHReset:
		verb = "Restart-VM -Force"
	case model.VMPowerAPause:
		verb = "Suspend-VM"
	case model.VMPowerAWaked:
		verb = "Resume-VM"
	default:
		return d.BaseDriver.PowerControl(vmUUID, action)
	}

	script := fmt.Sprintf(
		"$vm = %s; if (-not $vm) { throw '虚拟机不存在: %s' }; $vm | %s -ErrorAction Stop",
		hvVMExpr(vmUUID), psEscape(vmUUID), verb)

	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("电源操作失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	if state, ok := PowerStatusMap[d.GetPower(vmUUID)]; ok {
		d.mu.Lock()
		if cfg, exists := d.VmSaving[vmUUID]; exists {
			cfg.VmFlag = state
		}
		d.mu.Unlock()
	}
	return &model.ZMessage{Success: true, Action: "VMPowers", Message: "电源操作已执行"}, nil
}

// SupportsGracefulShutdown Stop-VM（不带 -TurnOff）通过集成服务优雅关机。
func (d *HyperVDriver) SupportsGracefulShutdown() bool { return true }

// hvVMExpr 返回「取单个虚拟机对象」的 PowerShell 表达式。
//
// 本项目里 vmUUID 有两种来源：创建时写入的虚拟机名（hpv-xxxx），
// 以及早期扫描落库的 GUID。Hyper-V 的 Stop-VM / Remove-VM / Start-VM
// 等 cmdlet 都没有 -Id 参数（只有 -Name / -VM），直接拼 -Id 会报
// "A parameter cannot be found that matches parameter name 'Id'"，
// 因此统一按名字或 ID 过滤，再用对象管道调用 cmdlet。
func hvVMExpr(vmUUID string) string {
	q := psEscape(vmUUID)
	return fmt.Sprintf(
		"@(Get-VM -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq '%s' -or [string]$_.Id -eq '%s' } | Select-Object -First 1)",
		q, q)
}

// hvSystemDiskPath 返回虚拟机系统盘路径（与主机配置的 system_path 同目录）
func (d *HyperVDriver) hvSystemDiskPath(vmName string) string {
	return filepath.Join(d.hvDiskDir(), vmName+".vhdx")
}

// hvVMDir 返回虚拟机的配置文件目录。
//
// New-VM 默认把 .vmcx 放到 %ProgramData%\Microsoft\Windows\Hyper-V，
// 传 -Path 后才会落在主机配置的系统路径下，与磁盘保持同一位置。
func (d *HyperVDriver) hvVMDir(vmName string) string {
	return filepath.Join(d.hvDiskDir(), vmName)
}

// hvCreateSystemDisk 从镜像库复制模板并扩容到目标容量。
//
// 直接用 New-VHD 建出来的是空盘，Gen2 启动只会停在
// "The boot loader did not load an operating system"。
func (d *HyperVDriver) hvCreateSystemDisk(cfg *model.VMConfig, diskPath string) *model.ZMessage {
	imagesPath := strings.TrimSpace(d.Config.ImagesPath)
	osName := strings.TrimSpace(cfg.OsName)
	if imagesPath == "" {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "主机未配置系统镜像路径（images_path）"}
	}
	if osName == "" {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "未选择操作系统镜像"}
	}

	src := filepath.Join(imagesPath, osName)
	st, err := os.Stat(src)
	if err != nil || st.IsDir() {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("系统镜像不存在: %s", src)}
	}

	// 实例系统盘统一是 .vhdx。
	// 镜像库里的模板常见为 .vhd（Gen1 时代格式，动态盘最大 2TB），
	// 直接把文件改名成 .vhdx 会因文件头不符被 Hyper-V 拒绝，必须真正转换。
	switch strings.ToLower(filepath.Ext(src)) {
	case ".vhdx":
		if err := copyLocalFile(src, diskPath); err != nil {
			return &model.ZMessage{Success: false, Action: "VMCreate",
				Message: fmt.Sprintf("复制系统镜像失败: %v", err)}
		}
	case ".vhd":
		_ = os.Remove(diskPath)
		script := fmt.Sprintf(
			"Convert-VHD -Path '%s' -DestinationPath '%s' -VHDType Dynamic -ErrorAction Stop",
			psEscape(src), psEscape(diskPath))
		if out, err := d.execPSTimeout(script, 30*time.Minute); err != nil {
			_ = os.Remove(diskPath)
			return &model.ZMessage{Success: false, Action: "VMCreate",
				Message: fmt.Sprintf("系统镜像转换为 VHDX 失败: %v (%s)", err, strings.TrimSpace(out))}
		}
	default:
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("不支持的镜像格式（需要 .vhd / .vhdx）: %s", src)}
	}

	sizeMB := cfg.HddNum
	if sizeMB <= 0 {
		sizeMB = 8192
	}
	sizeBytes := int64(sizeMB) * 1024 * 1024

	// Resize-VHD 只能扩容，模板本身已大于目标容量时保持原样
	script := fmt.Sprintf(
		"$v = Get-VHD -Path '%s' -ErrorAction Stop; "+
			"if ($v.Size -lt %d) { Resize-VHD -Path '%s' -SizeBytes %d -ErrorAction Stop }",
		psEscape(diskPath), sizeBytes, psEscape(diskPath), sizeBytes)
	if out, err := d.execPS(script); err != nil {
		_ = os.Remove(diskPath)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("系统盘扩容失败: %v (%s)", err, strings.TrimSpace(out))}
	}
	return nil
}

// hvRollbackCreate 清理创建失败留下的半成品：
// 不清理会让同名虚拟机在下次创建时直接冲突，磁盘也会一直占着空间。
func (d *HyperVDriver) hvRollbackCreate(vmName, vmDir, diskPath string) {
	script := fmt.Sprintf(
		"Stop-VM -Name '%s' -Force -TurnOff -ErrorAction SilentlyContinue; "+
			"Remove-VM -Name '%s' -Force -ErrorAction SilentlyContinue",
		psEscape(vmName), psEscape(vmName))
	_, _ = d.execPS(script)

	if vmDir != "" {
		_ = os.RemoveAll(vmDir)
	}
	if diskPath != "" {
		_ = os.Remove(diskPath)
	}
}

// hvConnectNics 按 nic_all 把网卡接到虚拟交换机。
//
// Hyper-V 的交换机名沿用主机的 network_nat / network_pub 字段
// （与 QEMU 驱动里的 bridge 名同一语义）。未配置时保持网卡未连接，
// 不阻断创建流程，只把原因带回消息里。
func (d *HyperVDriver) hvConnectNics(vmName string, nics map[string]model.NCConfig) string {
	if len(nics) == 0 {
		return ""
	}

	names := make([]string, 0, len(nics))
	for name := range nics {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := []string{"$ErrorActionPreference='SilentlyContinue';"}
	added := 0
	for _, name := range names {
		nic := nics[name]
		sw := strings.TrimSpace(d.Config.NetworkPub)
		if nic.EffectiveNicType() != "pub" {
			sw = strings.TrimSpace(d.Config.NetworkNat)
		}
		if sw == "" {
			continue
		}

		if added == 0 {
			// New-VM 已经创建了一张未连接的适配器，优先复用它
			parts = append(parts, fmt.Sprintf(
				"$nic = Get-VMNetworkAdapter -VMName '%s' | Select-Object -First 1; "+
					"if ($nic) { Connect-VMNetworkAdapter -VMNetworkAdapter $nic -SwitchName '%s' }",
				psEscape(vmName), psEscape(sw)))
		} else {
			parts = append(parts, fmt.Sprintf(
				"Add-VMNetworkAdapter -VMName '%s' -Name '%s'; "+
					"Connect-VMNetworkAdapter -VMName '%s' -Name '%s' -SwitchName '%s'",
				psEscape(vmName), psEscape(name), psEscape(vmName), psEscape(name), psEscape(sw)))
		}
		added++
	}
	if added == 0 {
		return ""
	}

	if out, err := d.execPS(strings.Join(parts, " ")); err != nil {
		return fmt.Sprintf("（网卡连接失败: %s）", strings.TrimSpace(out))
	}
	return ""
}

// hvSyncRealMACs 读取 Hyper-V 为网卡实际分配的 MAC，并按网卡顺序回写配置。
//
// Hyper-V 的网卡默认使用动态 MAC（00:15:5D 前缀），与 IP 池按 IP 推导出来的
// MAC 不一致。创建任务结束前必须把真实 MAC 写回 vm_saving：
//   - 虚拟机内 CloudInit 用真实 MAC 上报状态，findVMByMAC 才能匹配到本机；
//   - 上下行限速按 MAC 下发，用错的 MAC 会限速到不存在的网卡上。
//
// 读取失败只影响状态展示与限速，不阻断创建，因此不返回错误。
func (d *HyperVDriver) hvSyncRealMACs(vmName string, nics map[string]model.NCConfig) {
	if len(nics) == 0 {
		return
	}
	out, err := d.execPS(fmt.Sprintf(
		"Get-VMNetworkAdapter -VMName '%s' | Select-Object -ExpandProperty MacAddress",
		psEscape(vmName)))
	if err != nil {
		return
	}
	macs := strings.Fields(strings.TrimSpace(out))
	if len(macs) == 0 {
		return
	}

	// cfg.NicAll 以 map 存放，遍历顺序随机；排序后与适配器创建顺序一一对应。
	names := make([]string, 0, len(nics))
	for name := range nics {
		names = append(names, name)
	}
	sort.Strings(names)

	for i, name := range names {
		if i >= len(macs) {
			break
		}
		mac := normalizeVMac(macs[i])
		if mac == "" {
			continue
		}
		nic := nics[name]
		nic.MacAddr = mac
		nics[name] = nic
	}
}

// CreateVM 创建虚拟机
//
// 与旧实现的差异：
//   - 系统盘从镜像库复制并扩容，不再 New-VHD 建空盘
//   - 虚拟机配置文件跟随 system_path，不再落到 C:\ProgramData\Microsoft\Windows\Hyper-V
//   - 关闭自动检查点，关闭安全启动但保留 TPM
//   - 启动顺序优先系统盘，避免每次先走网络引导超时
func (d *HyperVDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmName := cfg.VmUUID
	mem := cfg.MemNum
	if mem <= 0 {
		mem = 1024
	}
	cpu := cfg.CpuNum
	if cpu <= 0 {
		cpu = 1
	}

	diskPath := d.hvSystemDiskPath(vmName)
	vmDir := d.hvVMDir(vmName)

	if msg := d.hvCreateSystemDisk(cfg, diskPath); msg != nil {
		return msg, nil
	}

	script := fmt.Sprintf(
		"New-Item -ItemType Directory -Force -Path '%s' | Out-Null; "+
			"New-VM -Name '%s' -MemoryStartupBytes %dMB -Generation 2 -Path '%s' -ErrorAction Stop; "+
			"Set-VM -Name '%s' -AutomaticCheckpointsEnabled $false -ErrorAction Stop; "+
			"Set-VMFirmware -VMName '%s' -EnableSecureBoot Off -ErrorAction Stop; "+
			"Set-VMKeyProtector -VMName '%s' -NewLocalKeyProtector -ErrorAction Stop; "+
			"Enable-VMTPM -VMName '%s' -ErrorAction Stop; "+
			"Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ErrorAction Stop; "+
			"Set-VMProcessor -VMName '%s' -Count %d -ErrorAction Stop; "+
			"$disk = Get-VMHardDiskDrive -VMName '%s' | Select-Object -First 1; "+
			"Set-VMFirmware -VMName '%s' -FirstBootDevice $disk -ErrorAction SilentlyContinue",
		psEscape(vmDir),
		psEscape(vmName), mem, psEscape(vmDir),
		psEscape(vmName),
		psEscape(vmName),
		psEscape(vmName),
		psEscape(vmName),
		psEscape(vmName), psEscape(diskPath),
		psEscape(vmName), cpu,
		psEscape(vmName),
		psEscape(vmName))

	if out, err := d.execPS(script); err != nil {
		d.hvRollbackCreate(vmName, vmDir, diskPath)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("创建虚拟机失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	nicWarn := d.hvConnectNics(vmName, cfg.NicAll)

	if out, err := d.execPS(fmt.Sprintf("Start-VM -Name '%s' -ErrorAction Stop", psEscape(vmName))); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("虚拟机已创建但启动失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	cfg.VmFlag = model.VMPowerStarted
	// 回写平台实际分配的 MAC，必须在下发限速与落库之前：Hyper-V 动态网卡的
	// MAC（00:15:5D 前缀）与 IP 池按 IP 推导出来的 MAC 并不相同。
	d.hvSyncRealMACs(vmName, cfg.NicAll)
	d.mu.Lock()
	d.VmSaving[vmName] = cfg
	d.mu.Unlock()

	// 按虚拟机网卡 MAC 下发上下行限速（失败不影响创建结果，仅提示原因）
	limitErr := d.hvApplyVMBandwidth(cfg)

	return &model.ZMessage{Success: true, Action: "VMCreate",
		Message: "虚拟机创建成功" + nicWarn + hvLimitWarn(limitErr)}, nil
}

// DeleteVM 删除虚拟机
func (d *HyperVDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	// 删除前记录网卡 MAC：虚拟机不存在后无法再枚举，限速规则会残留在驱动表里
	var staleMACs [][6]byte
	if netLimiterAvailable() {
		if adapters, err := d.hvNetworkAdapters(); err == nil {
			d.mu.RLock()
			cfg := d.VmSaving[vmUUID]
			d.mu.RUnlock()
			if cfg == nil {
				cfg = &model.VMConfig{VmUUID: vmUUID}
			}
			staleMACs = hvMACsOf(adapters, cfg)
		}
	}

	script := fmt.Sprintf(
		"$vm = %s; if (-not $vm) { throw '虚拟机不存在: %s' }; "+
			"$vm | Stop-VM -Force -TurnOff -ErrorAction SilentlyContinue; "+
			"$vm | Remove-VM -Force -ErrorAction Stop",
		hvVMExpr(vmUUID), psEscape(vmUUID))

	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: fmt.Sprintf("删除失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	for _, mac := range staleMACs {
		_ = netLimiterClearRule(mac)
	}

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDelete", Message: "虚拟机删除成功"}, nil
}

// UpdateVM 更新虚拟机配置
//
// CPU / 内存属于硬件规格，Hyper-V 要求关机后修改；
// 带宽限速由 WFP 驱动在网卡 MAC 层实时生效，可在线调整、无需关机。
func (d *HyperVDriver) UpdateVM(cfg *model.VMConfig, oldCfg *model.VMConfig) (*model.ZMessage, error) {
	if oldCfg == nil {
		oldCfg = cfg
	}
	cpuChanged := cfg.CpuNum != oldCfg.CpuNum && cfg.CpuNum > 0
	memChanged := cfg.MemNum != oldCfg.MemNum && cfg.MemNum > 0
	if (cpuChanged || memChanged) && d.GetPower(cfg.VmUUID) == "运行中" {
		return &model.ZMessage{Success: false, Action: "VMUpdate",
			Message: "虚拟机必须关机后才能修改CPU/内存配置"}, nil
	}

	var parts []string
	if cpuChanged {
		parts = append(parts, fmt.Sprintf(
			"Set-VMProcessor -VMName '%s' -Count %d -ErrorAction Stop", psEscape(cfg.VmUUID), cfg.CpuNum))
	}
	if memChanged {
		parts = append(parts, fmt.Sprintf(
			"Set-VMMemory -VMName '%s' -StartupBytes %dMB -ErrorAction Stop", psEscape(cfg.VmUUID), cfg.MemNum))
	}
	if len(parts) > 0 {
		if out, err := d.execPS(strings.Join(parts, "; ")); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("更新失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	// 上下行带宽热更新
	limitErr := d.hvApplyVMBandwidth(cfg)

	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMUpdate",
		Message: "虚拟机配置更新成功" + hvLimitWarn(limitErr)}, nil
}

// ReinstallVM 重装系统（断开旧盘、从镜像库重建系统盘并启动）
func (d *HyperVDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmName := cfg.VmUUID
	_, _ = d.PowerControl(vmName, model.VMPowerHClose)

	diskPath := d.hvSystemDiskPath(vmName)

	// 先摘除并删除旧盘：文件被挂载时无法覆盖
	detach := fmt.Sprintf(
		"Get-VMHardDiskDrive -VMName '%s' | Where-Object { $_.Path -like '*%s*' } | "+
			"Remove-VMHardDiskDrive -ErrorAction SilentlyContinue; "+
			"Remove-Item -LiteralPath '%s' -Force -ErrorAction SilentlyContinue",
		psEscape(vmName), psEscape(diskPath), psEscape(diskPath))
	if out, err := d.execPS(detach); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: fmt.Sprintf("卸载旧系统盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	if msg := d.hvCreateSystemDisk(cfg, diskPath); msg != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: "重装失败: " + msg.Message}, nil
	}

	script := fmt.Sprintf(
		"Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ErrorAction Stop; "+
			"$disk = Get-VMHardDiskDrive -VMName '%s' | Select-Object -First 1; "+
			"Set-VMFirmware -VMName '%s' -FirstBootDevice $disk -ErrorAction SilentlyContinue; "+
			"Start-VM -Name '%s' -ErrorAction Stop",
		psEscape(vmName), psEscape(diskPath),
		psEscape(vmName), psEscape(vmName), psEscape(vmName))

	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: fmt.Sprintf("重装失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	return &model.ZMessage{Success: true, Action: "VMReinstall", Message: "系统重装成功"}, nil
}

// GetVNCInfo 获取远程控制台信息（Hyper-V 使用 VMConnect）
func (d *HyperVDriver) GetVNCInfo(vmUUID string, ipAddr string) (*model.ZMessage, error) {
	host := d.Config.ServerAddr
	if host == "" {
		host = "127.0.0.1"
	}
	return &model.ZMessage{Success: true, Action: "VCRemote", Message: "远程控制台信息获取成功",
		Results: map[string]interface{}{
			"type": "vmconnect", "host": host, "vm_uuid": vmUUID, "password": "",
		}}, nil
}

// MountHDD 挂载 / 卸载数据盘
func (d *HyperVDriver) MountHDD(vmUUID string, hdd *model.SDConfig, mount bool) (*model.ZMessage, error) {
	if !mount {
		script := fmt.Sprintf(
			"Get-VMHardDiskDrive -VMName '%s' | Where-Object { $_.Path -like '*%s*' } | Remove-VMHardDiskDrive",
			psEscape(vmUUID), psEscape(hdd.HddName))
		_, _ = d.execPS(script)
		d.mu.Lock()
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			if h, exists := cfg.HddAll[hdd.HddName]; exists {
				h.HddFlag = 0
				cfg.HddAll[hdd.HddName] = h
			}
		}
		d.mu.Unlock()
		return &model.ZMessage{Success: true, Action: "HDDMount", Message: "磁盘卸载成功"}, nil
	}

	path := filepath.Join(d.hvDiskDir(), hdd.HddName+".vhdx")
	sizeMB := hdd.HddSize
	if sizeMB <= 0 {
		sizeMB = 10240
	}
	sizeBytes := int64(sizeMB) * 1024 * 1024

	script := fmt.Sprintf(
		"New-VHD -Path '%s' -SizeBytes %d -Dynamic -ErrorAction SilentlyContinue; "+
			"Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ErrorAction Stop",
		psEscape(path), sizeBytes, psEscape(vmUUID), psEscape(path))
	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "HDDMount",
			Message: fmt.Sprintf("挂载失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	hdd.HddFlag = 1
	hdd.HddPath = path
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.HddAll[hdd.HddName] = *hdd
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "HDDMount", Message: "磁盘挂载成功"}, nil
}

// MountISO 挂载 / 卸载 ISO
func (d *HyperVDriver) MountISO(vmUUID string, iso *model.IMConfig, mount bool) (*model.ZMessage, error) {
	if mount {
		path := iso.IsoPath
		if path == "" {
			dir := d.Config.DvdromPath
			if dir == "" {
				dir = d.Config.ImagesPath
			}
			path = filepath.Join(dir, iso.IsoFile)
		}
		script := fmt.Sprintf("Add-VMDvdDrive -VMName '%s' -Path '%s' -ErrorAction Stop",
			psEscape(vmUUID), psEscape(path))
		if out, err := d.execPS(script); err != nil {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: fmt.Sprintf("挂载ISO失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
		d.mu.Lock()
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			cfg.IsoAll[iso.IsoName] = *iso
		}
		d.mu.Unlock()
		return &model.ZMessage{Success: true, Action: "ISOMount", Message: "ISO挂载成功"}, nil
	}

	script := fmt.Sprintf(
		"Get-VMDvdDrive -VMName '%s' | Remove-VMDvdDrive -ErrorAction SilentlyContinue", psEscape(vmUUID))
	_, _ = d.execPS(script)
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		delete(cfg.IsoAll, iso.IsoName)
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "ISOMount", Message: "ISO卸载成功"}, nil
}

// CreateBackup 创建备份（检查点）
func (d *HyperVDriver) CreateBackup(vmUUID string, tips string) (*model.ZMessage, error) {
	name := fmt.Sprintf("backup-%s", time.Now().Format("20060102-150405"))
	script := fmt.Sprintf("Checkpoint-VM -VMName '%s' -SnapshotName '%s' -ErrorAction Stop",
		psEscape(vmUUID), psEscape(name))
	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: fmt.Sprintf("创建检查点失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.Backups = append(cfg.Backups, model.VMBackup{
			BackupName: name, BackupTime: time.Now(), BackupTips: tips,
		})
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMBackup", Message: "备份创建成功",
		Results: map[string]interface{}{"backup_name": name}}, nil
}

// RestoreBackup 还原备份（应用检查点）
func (d *HyperVDriver) RestoreBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	script := fmt.Sprintf(
		"Restore-VMSnapshot -VMName '%s' -Name '%s' -Confirm:$false -ErrorAction Stop",
		psEscape(vmUUID), psEscape(backupName))
	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("还原失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	return &model.ZMessage{Success: true, Action: "Restores", Message: "备份还原成功"}, nil
}

// DeleteBackup 删除备份（删除检查点）
func (d *HyperVDriver) DeleteBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	script := fmt.Sprintf(
		"Remove-VMSnapshot -VMName '%s' -Name '%s' -Confirm:$false -ErrorAction Stop",
		psEscape(vmUUID), psEscape(backupName))
	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "RMBackup",
			Message: fmt.Sprintf("删除备份失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		kept := cfg.Backups[:0]
		for _, b := range cfg.Backups {
			if b.BackupName != backupName {
				kept = append(kept, b)
			}
		}
		cfg.Backups = kept
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "RMBackup", Message: "备份删除成功"}, nil
}

// SetupPCI PCI/GPU 直通（需关机）
// 与 Python 版 Win64HyperV.PCISetup 一致：
//   - PV（GPU 分区虚拟化）：Add-VMGpuPartitionAdapter + 分区资源设置
//   - DDA（离散设备分配）：Disable-PnpDevice + Dismount-VMHostAssignableDevice + Add-VMAssignableDevice
func (d *HyperVDriver) SetupPCI(vmUUID string, cfg *model.VFConfig, pciKey string, add bool) (*model.ZMessage, error) {
	if d.GetPower(vmUUID) == "运行中" {
		return &model.ZMessage{Success: false, Action: "PCISetup",
			Message: "PCI直通需要先关闭虚拟机"}, nil
	}

	deviceType := cfg.GpuMdev // "PV" 或 "DDA"
	var script string

	if add {
		switch deviceType {
		case "PV":
			script = fmt.Sprintf(
				"Add-VMGpuPartitionAdapter -VMName '%s' -ErrorAction Stop; "+
					"Set-VMGpuPartitionAdapter -VMName '%s' "+
					"-MinPartitionVRAM 1000000000 -MaxPartitionVRAM 1000000000 -OptimalPartitionVRAM 1000000000",
				psEscape(vmUUID), psEscape(vmUUID))
		case "DDA":
			script = fmt.Sprintf(
				"$lp = '%s'; "+
					"$dev = Get-PnpDevice | Where-Object { (Get-PnpDeviceProperty -InstanceId $_.InstanceId -KeyName DEVPKEY_Device_LocationPaths).Data -contains $lp }; "+
					"if ($dev) { "+
					"Disable-PnpDevice -InstanceId $dev.InstanceId -Confirm:$false -ErrorAction SilentlyContinue; "+
					"Dismount-VMHostAssignableDevice -LocationPath $lp -Force -ErrorAction Stop; "+
					"Add-VMAssignableDevice -VMName '%s' -LocationPath $lp -ErrorAction Stop "+
					"} else { Write-Error '未找到DDA设备' }",
				psEscape(cfg.GpuUUID), psEscape(vmUUID))
		default:
			return &model.ZMessage{Success: false, Action: "PCISetup",
				Message: fmt.Sprintf("未知设备类型: %s", deviceType)}, nil
		}
	} else {
		switch deviceType {
		case "PV":
			script = fmt.Sprintf(
				"Remove-VMGpuPartitionAdapter -VMName '%s' -ErrorAction Stop", psEscape(vmUUID))
		case "DDA":
			script = fmt.Sprintf(
				"Remove-VMAssignableDevice -VMName '%s' -LocationPath '%s' -ErrorAction Stop",
				psEscape(vmUUID), psEscape(cfg.GpuUUID))
		default:
			script = fmt.Sprintf(
				"Remove-VMAssignableDevice -VMName '%s' -LocationPath '%s' -ErrorAction SilentlyContinue",
				psEscape(vmUUID), psEscape(cfg.GpuUUID))
		}
	}

	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "PCISetup",
			Message: fmt.Sprintf("PCI直通失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if vmCfg, ok := d.VmSaving[vmUUID]; ok {
		if vmCfg.PciAll == nil {
			vmCfg.PciAll = make(map[string]model.VFConfig)
		}
		if add {
			vmCfg.PciAll[pciKey] = *cfg
		} else {
			delete(vmCfg.PciAll, pciKey)
		}
	}
	d.mu.Unlock()

	text := "添加"
	if !add {
		text = "移除"
	}
	return &model.ZMessage{Success: true, Action: "PCISetup", Message: "PCI 设备" + text + "成功"}, nil
}

// SetPassword 设置虚拟机密码（PowerShell Direct + Set-LocalUser）
func (d *HyperVDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	if d.GetPower(vmUUID) != "运行中" {
		return &model.ZMessage{Success: false, Action: "VMPasswd",
			Message: "虚拟机未运行，无法设置密码"}, nil
	}

	user := "Administrator"
	d.mu.RLock()
	if cfg, ok := d.VmSaving[vmUUID]; ok && cfg.OsUser != "" {
		user = cfg.OsUser
	}
	d.mu.RUnlock()

	// 用户名与口令必须转义：两者都会拼入 -Command 脚本文本，
	// 未转义的引号可以提前闭合字面量并执行附加语句。
	script := fmt.Sprintf(
		"Invoke-Command -VMName '%s' -ScriptBlock { param($u,$p) "+
			"$s = ConvertTo-SecureString $p -AsPlainText -Force; "+
			"Get-LocalUser -Name $u | Set-LocalUser -Password $s } "+
			"-ArgumentList '%s','%s'",
		psEscape(vmUUID), psEscape(user), psEscape(password))

	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "VMPasswd",
			Message: fmt.Sprintf("密码修改失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.OsPass = password
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "VMPasswd", Message: "密码修改成功"}, nil
}

// Crontab 定时任务：同步电源状态
func (d *HyperVDriver) Crontab() bool {
	d.mu.RLock()
	ids := make([]string, 0, len(d.VmSaving))
	for id := range d.VmSaving {
		ids = append(ids, id)
	}
	d.mu.RUnlock()

	for _, id := range ids {
		state := d.GetPower(id)
		d.mu.Lock()
		if cfg, ok := d.VmSaving[id]; ok {
			if s, ok2 := PowerStatusMap[state]; ok2 {
				cfg.VmFlag = s
			}
		}
		d.mu.Unlock()
	}

	// 周期同步上下行限速规则：驱动重载 / 主机重启后内核规则表会清空，
	// 这里按当前配置自动补回（内部有节流，不会每轮都枚举网卡）。
	_ = d.hvSyncBandwidth()

	return true
}
