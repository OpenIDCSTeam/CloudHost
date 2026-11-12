// Package driver Hyper-V 驱动扩展实现。
//
// 补齐 Python 版 HostServer/Win64HyperV.py 中存在、而 Go 版此前缺失的能力：
//   - 数据盘删除 / 移交（使用 VHDX 路径）
//   - PCI / GPU 设备枚举（Get-PnpDevice，含 DDA 位置路径）
//   - USB 设备枚举
//   - USB 存储挂载（宿主物理磁盘直通）
//   - 屏幕截图（Hyper-V WMI 缩略图接口）
package driver

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// hvDataDiskPath 返回数据盘的 VHDX 路径（与 MountHDD 保持一致）
func (d *HyperVDriver) hvDataDiskPath(hdd *model.SDConfig) string {
	if hdd != nil && strings.TrimSpace(hdd.HddPath) != "" {
		return hdd.HddPath
	}
	name := ""
	if hdd != nil {
		name = hdd.HddName
	}
	return filepath.Join(d.hvDiskDir(), name+".vhdx")
}

// DeleteHDD 删除数据盘：移除虚拟磁盘挂载 → 删除 VHDX 文件
func (d *HyperVDriver) DeleteHDD(vmUUID string, hddName string) (*model.ZMessage, error) {
	if strings.TrimSpace(hddName) == "" {
		return &model.ZMessage{Success: false, Action: "RMMounts", Message: "磁盘名称不能为空"}, nil
	}

	d.mu.RLock()
	cfg, ok := d.VmSaving[vmUUID]
	var hdd model.SDConfig
	if ok {
		hdd = cfg.HddAll[hddName]
	}
	d.mu.RUnlock()
	if !ok {
		return &model.ZMessage{Success: false, Action: "RMMounts", Message: "虚拟机不存在"}, nil
	}
	if hdd.HddName == "" {
		hdd.HddName = hddName
	}

	path := d.hvDataDiskPath(&hdd)

	// 1) 移除挂在虚拟机上的该磁盘
	detach := fmt.Sprintf(
		"Get-VMHardDiskDrive -VMName '%s' | Where-Object { $_.Path -like '*%s*' } | "+
			"Remove-VMHardDiskDrive -ErrorAction SilentlyContinue",
		psEscape(vmUUID), psEscape(hdd.HddName))
	if out, err := d.execPS(detach); err != nil {
		return &model.ZMessage{Success: false, Action: "RMMounts",
			Message: fmt.Sprintf("卸载数据盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	// 2) 删除真实 VHDX 文件
	if path != "" {
		remove := fmt.Sprintf("Remove-Item -LiteralPath '%s' -Force -ErrorAction SilentlyContinue", psEscape(path))
		if out, err := d.execPS(remove); err != nil {
			return &model.ZMessage{Success: false, Action: "RMMounts",
				Message: fmt.Sprintf("删除磁盘文件失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	d.mu.Lock()
	delete(cfg.HddAll, hddName)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "RMMounts", Message: "数据盘删除成功"}, nil
}

// TransferHDD 移交数据盘：从源移除并挂到目标虚拟机
func (d *HyperVDriver) TransferHDD(vmUUID string, hdd *model.SDConfig, targetVM string) (*model.ZMessage, error) {
	if hdd == nil || strings.TrimSpace(hdd.HddName) == "" {
		return &model.ZMessage{Success: false, Action: "HDDTrans", Message: "磁盘信息不完整"}, nil
	}
	if vmUUID == targetVM {
		return &model.ZMessage{Success: false, Action: "HDDTrans", Message: "源与目标虚拟机相同"}, nil
	}

	d.mu.RLock()
	srcCfg, srcOK := d.VmSaving[vmUUID]
	dstCfg, dstOK := d.VmSaving[targetVM]
	d.mu.RUnlock()
	if !srcOK {
		return &model.ZMessage{Success: false, Action: "HDDTrans", Message: "原始虚拟机不存在"}, nil
	}
	if !dstOK {
		return &model.ZMessage{Success: false, Action: "HDDTrans", Message: "目标虚拟机不存在"}, nil
	}

	path := d.hvDataDiskPath(hdd)
	if path == "" {
		return &model.ZMessage{Success: false, Action: "HDDTrans", Message: "无法定位磁盘路径"}, nil
	}

	// 1) 源移除
	detach := fmt.Sprintf(
		"Get-VMHardDiskDrive -VMName '%s' | Where-Object { $_.Path -like '*%s*' } | "+
			"Remove-VMHardDiskDrive -ErrorAction SilentlyContinue",
		psEscape(vmUUID), psEscape(hdd.HddName))
	if out, err := d.execPS(detach); err != nil {
		return &model.ZMessage{Success: false, Action: "HDDTrans",
			Message: fmt.Sprintf("源虚拟机卸载失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	// 2) 目标挂载
	attach := fmt.Sprintf("Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ErrorAction Stop",
		psEscape(targetVM), psEscape(path))
	if out, err := d.execPS(attach); err != nil {
		// 回滚
		rollback := fmt.Sprintf("Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ErrorAction SilentlyContinue",
			psEscape(vmUUID), psEscape(path))
		_, _ = d.execPS(rollback)
		return &model.ZMessage{Success: false, Action: "HDDTrans",
			Message: fmt.Sprintf("目标虚拟机挂载失败（已回滚）: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	delete(srcCfg.HddAll, hdd.HddName)
	moved := *hdd
	moved.HddFlag = 1
	moved.HddPath = path
	if dstCfg.HddAll == nil {
		dstCfg.HddAll = make(map[string]model.SDConfig)
	}
	dstCfg.HddAll[hdd.HddName] = moved
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "HDDTrans", Message: "数据盘移交成功"}, nil
}

// hvDeviceSnapshot 一次 PowerShell 批量枚举得到的宿主机设备快照。
//
// 设备枚举曾是本驱动最慢的一段：旧实现按设备逐个 Get-PnpDeviceProperty，
// 每台设备一次 CIM 往返，二十几个设备就要 50 秒以上，创建弹窗一打开就卡住。
// 这里改成批量查询（-InstanceId 接受数组）并把结果做成带 TTL 的快照，
// 让 gpu-list / pci-list / usb-list 三个接口共享同一次枚举。
type hvDeviceSnapshot struct {
	gpus map[string]*model.VFConfig
	pcis map[string]*model.VFConfig
	usbs map[string]*model.USBInfos
}

// hvDeviceCacheTTL 设备快照有效期。热插拔不需要秒级可见，短 TTL 足以挡掉弹窗反复打开。
const hvDeviceCacheTTL = 30 * time.Second

// hvDeviceScript 一次输出 GPU / PCI / USB 三类设备。
const hvDeviceScript = `$ErrorActionPreference='SilentlyContinue';
$devs = @(Get-PnpDevice);
$pci = @($devs | Where-Object { $_.InstanceId -like 'PCI\*' -and $_.Status -eq 'OK' });
$gpu = @($devs | Where-Object { $_.Class -eq 'Display' -and $_.Status -eq 'OK' });
$usb = @($devs | Where-Object { $_.Class -eq 'USB' -and $_.Status -eq 'OK' });
$lp = @{};
$ids = @(@($pci | Select-Object -ExpandProperty InstanceId) + @($gpu | Select-Object -ExpandProperty InstanceId));
if ($ids.Count -gt 0) {
  Get-PnpDeviceProperty -InstanceId $ids -KeyName 'DEVPKEY_Device_LocationPaths' | ForEach-Object { $lp[$_.InstanceId] = @($_.Data)[0] }
};
$hw = @{};
$uids = @($usb | Select-Object -ExpandProperty InstanceId);
if ($uids.Count -gt 0) {
  Get-PnpDeviceProperty -InstanceId $uids -KeyName 'DEVPKEY_Device_HardwareIds' | ForEach-Object { $hw[$_.InstanceId] = (@($_.Data) -join ',') }
};
ConvertTo-Json -InputObject ([pscustomobject]@{
  gpu = @($gpu | ForEach-Object { [pscustomobject]@{ id=$_.InstanceId; lp=$lp[$_.InstanceId]; name=$_.FriendlyName } });
  pci = @($pci | ForEach-Object { [pscustomobject]@{ id=$_.InstanceId; lp=$lp[$_.InstanceId]; name=$_.FriendlyName } });
  usb = @($usb | ForEach-Object { [pscustomobject]@{ id=$_.InstanceId; name=$_.FriendlyName; hw=$hw[$_.InstanceId] } })
}) -Compress -Depth 4`

// hvDevices 返回设备快照。
//
// 缓存策略与列表接口一致（stale-while-revalidate）：
//   - 有快照立刻返回，即使已过期，刷新放到后台
//   - 同一时刻只有一个枚举在飞，避免并发打开弹窗时反复启动 PowerShell
//   - 首次枚举最多等 8 秒，超时返回空快照
func (d *HyperVDriver) hvDevices() *hvDeviceSnapshot {
	d.hvDevMu.Lock()

	if d.hvDevData != nil {
		if time.Since(d.hvDevAt) >= hvDeviceCacheTTL && !d.hvDevLoading {
			d.hvDevLoading = true
			go func() {
				snap := d.hvQueryDevices()
				d.hvDevMu.Lock()
				if snap != nil {
					d.hvDevData = snap
					d.hvDevAt = time.Now()
				}
				d.hvDevLoading = false
				d.hvDevMu.Unlock()
			}()
		}
		data := d.hvDevData
		d.hvDevMu.Unlock()
		return data
	}

	// 首次：持锁枚举，并发请求在此合并
	snap := d.hvQueryDevices()
	d.hvDevMu.Unlock()

	if snap == nil {
		snap = &hvDeviceSnapshot{
			gpus: make(map[string]*model.VFConfig),
			pcis: make(map[string]*model.VFConfig),
			usbs: make(map[string]*model.USBInfos),
		}
		return snap
	}

	d.hvDevMu.Lock()
	d.hvDevData = snap
	d.hvDevAt = time.Now()
	d.hvDevMu.Unlock()
	return snap
}

// hvQueryDevices 执行一次批量枚举，失败（含超时）返回 nil。
func (d *HyperVDriver) hvQueryDevices() *hvDeviceSnapshot {
	snap := &hvDeviceSnapshot{
		gpus: make(map[string]*model.VFConfig),
		pcis: make(map[string]*model.VFConfig),
		usbs: make(map[string]*model.USBInfos),
	}

	// 单次枚举限制在 8 秒：设备信息允许旧，接口不能被 PowerShell 拖住
	out, err := d.execPSTimeout(hvDeviceScript, 8*time.Second)
	if err != nil {
		return nil
	}
	body := strings.TrimSpace(out)
	if body == "" {
		return snap
	}
	var parsed hvDeviceJSON
	if err := jsonUnmarshalLoose(body, &parsed); err != nil {
		return snap
	}

	for _, it := range parsed.Gpu {
		key := it.LocationPath
		if key == "" {
			key = it.InstanceID
		}
		if key == "" {
			continue
		}
		snap.gpus[key] = &model.VFConfig{
			PciKey:  key,
			GpuUUID: it.LocationPath,
			GpuMdev: "PV",
			GpuHint: it.Name,
		}
	}
	for _, it := range parsed.Pci {
		if it.LocationPath == "" {
			continue
		}
		snap.pcis[it.LocationPath] = &model.VFConfig{
			PciKey:  it.LocationPath,
			GpuUUID: it.LocationPath,
			GpuMdev: "DDA",
			GpuHint: it.Name,
		}
	}
	for _, it := range parsed.Usb {
		vid, pid := extractVidPid(it.Hardware)
		if vid == "" || pid == "" {
			continue
		}
		key := strings.ToLower(vid + ":" + pid)
		snap.usbs[key] = &model.USBInfos{
			UsbKey:  key,
			VidUUID: strings.ToLower(vid),
			PidUUID: strings.ToLower(pid),
			UsbHint: it.Name,
		}
	}

	// 缓存写入由 hvDevices 统一负责（需要持 hvDevMu）
	return snap
}

// GetPCIList 枚举可 DDA 直通的 PCI 设备（来自设备快照）
func (d *HyperVDriver) GetPCIList() (map[string]*model.VFConfig, error) {
	return d.hvDevices().pcis, nil
}

// GetGPUList 枚举显卡设备（来自设备快照，可用于 GPU-PV 或 DDA）
func (d *HyperVDriver) GetGPUList() (map[string]*model.VFConfig, error) {
	return d.hvDevices().gpus, nil
}

// hvDeviceJSON hvDeviceScript 的 JSON 输出结构
type hvDeviceJSON struct {
	Gpu []pnpDevice   `json:"gpu"`
	Pci []pnpDevice   `json:"pci"`
	Usb []hvUSBDevice `json:"usb"`
}

// pnpDevice PowerShell 输出的设备条目
type pnpDevice struct {
	InstanceID   string `json:"id"`
	LocationPath string `json:"lp"`
	Name         string `json:"name"`
}

// hvUSBDevice USB 设备条目
type hvUSBDevice struct {
	InstanceID string `json:"id"`
	Name       string `json:"name"`
	Hardware   string `json:"hw"`
}

// GetUSBList 枚举宿主机 USB 设备（来自设备快照）
func (d *HyperVDriver) GetUSBList() (map[string]*model.USBInfos, error) {
	return d.hvDevices().usbs, nil
}

// extractVidPid 从 HardwareIds 字符串中解析 VID/PID（形如 USB\VID_046D&PID_C52B）
func extractVidPid(hw string) (string, string) {
	upper := strings.ToUpper(hw)
	vid, pid := "", ""
	if i := strings.Index(upper, "VID_"); i >= 0 && i+8 <= len(upper) {
		vid = upper[i+4 : i+8]
	}
	if i := strings.Index(upper, "PID_"); i >= 0 && i+8 <= len(upper) {
		pid = upper[i+4 : i+8]
	}
	return vid, pid
}

// MountUSB 挂载 / 卸载 USB 存储（以宿主物理磁盘直通方式附加）
func (d *HyperVDriver) MountUSB(vmUUID string, usbKey string, mount bool) (*model.ZMessage, error) {
	if strings.TrimSpace(usbKey) == "" {
		return &model.ZMessage{Success: false, Action: "USBMount", Message: "USB 设备标识不能为空"}, nil
	}

	if !mount {
		script := fmt.Sprintf(
			"Get-VMHardDiskDrive -VMName '%s' | Where-Object { $_.Path -like '*%s*' } | "+
				"Remove-VMHardDiskDrive -ErrorAction SilentlyContinue",
			psEscape(vmUUID), psEscape(usbKey))
		_, _ = d.execPS(script)
		return &model.ZMessage{Success: true, Action: "USBMount", Message: "USB存储卸载成功"}, nil
	}

	// usbKey 为目标磁盘路径，例如 \\.\PhysicalDrive2
	script := fmt.Sprintf("Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ErrorAction Stop",
		psEscape(vmUUID), psEscape(usbKey))
	if out, err := d.execPS(script); err != nil {
		return &model.ZMessage{Success: false, Action: "USBMount",
			Message: fmt.Sprintf("挂载失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	return &model.ZMessage{Success: true, Action: "USBMount", Message: "USB存储挂载成功"}, nil
}

// GetScreenshot 截取虚拟机屏幕（Hyper-V WMI 缩略图接口）
func (d *HyperVDriver) GetScreenshot(vmUUID string) (string, error) {
	if d.GetPower(vmUUID) != "运行中" {
		return "", fmt.Errorf("虚拟机未运行，无法截图")
	}
	return d.captureHyperVScreenshot(vmUUID)
}
