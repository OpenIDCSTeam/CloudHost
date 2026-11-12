// Package driver VMware Workstation 驱动扩展实现。
//
// 补齐 Python 版 HostServer/Workstation.py 中存在、而 Go 版此前缺失的能力：
//   - 系统重装（VMSetups）：复制系统模板镜像并扩展磁盘容量
//   - 屏幕截图（VMScreen）：Workstation 无截图 API，改走 RemoteDisplay VNC 帧缓冲
//   - USB 设备枚举（USBShows）：Windows 走 PowerShell，其它平台走 lsusb
//
// 说明：HDDMount / ISOMount / RMMounts 在 Python 版中同样是调用基类
// 的「配置记录」实现，因此 Go 版保持继承基类行为即可，不做伪实现。
package driver

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// vmrunBin 解析 vmrun 可执行文件路径（优先取 launch_path 配置）
func (d *WorkstationDriver) vmrunBin() string {
	launch := strings.TrimSpace(d.Config.LaunchPath)
	if launch != "" {
		if info, err := os.Stat(launch); err == nil {
			if info.IsDir() {
				for _, name := range []string{"vmrun.exe", "vmrun"} {
					candidate := filepath.Join(launch, name)
					if _, err := os.Stat(candidate); err == nil {
						return candidate
					}
				}
			} else {
				return launch
			}
		}
	}
	return "vmrun"
}

// vmxPath 返回虚拟机的 VMX 配置文件路径
func (d *WorkstationDriver) vmxPath(vmUUID string) string {
	dir := strings.TrimSpace(d.Config.SystemPath)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, vmUUID+".vmx")
}

// ReinstallVM 重装系统：用系统模板镜像覆盖系统盘，并把磁盘扩展到目标容量。
//
// 与 Python 版 Workstation.VMSetups 一致，并补上 VMX 引用同步：
// 原实现复制出 <uuid>.vhd 后 VMX 仍指向 <uuid>.vmdk，重装完成开机依旧报
// File not found，等于把系统重装成了「不可启动」。
func (d *WorkstationDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmUUID := cfg.VmUUID

	// 停止虚拟机
	if _, err := d.PowerControl(vmUUID, model.VMPowerHClose); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: "关闭虚拟机失败: " + err.Error()}, nil
	}
	time.Sleep(2 * time.Second)

	dir := strings.TrimSpace(d.Config.SystemPath)
	if dir == "" {
		dir = "."
	}
	vmxPath := d.vmxPath(vmUUID)
	oldDisk := readVMXDiskFile(vmxPath)

	diskName, warn, err := d.prepareSystemDisk(cfg, dir, true)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: "准备系统盘失败: " + err.Error()}, nil
	}

	// 模板扩展名可能变化（.vmdk → .vhd），旧盘残留会被误认为数据盘
	if oldDisk != "" && !strings.EqualFold(oldDisk, diskName) {
		_ = os.Remove(filepath.Join(dir, oldDisk))
	}
	if _, statErr := os.Stat(vmxPath); statErr == nil {
		if updErr := updateVMXDiskFile(vmxPath, diskName); updErr != nil {
			return &model.ZMessage{Success: false, Action: "VMReinstall",
				Message: "更新 VMX 磁盘引用失败: " + updErr.Error()}, nil
		}
	}

	d.mu.Lock()
	if c, ok := d.VmSaving[vmUUID]; ok {
		c.OsName = cfg.OsName
		c.HddNum = cfg.HddNum
	}
	d.mu.Unlock()

	// 重新启动
	_, _ = d.PowerControl(vmUUID, model.VMPowerSStart)

	msg := "系统重装成功"
	if warn != "" {
		msg += "（" + warn + "）"
	}
	return &model.ZMessage{Success: true, Action: "VMReinstall", Message: msg}, nil
}

// GetScreenshot 采集虚拟机当前屏幕。
//
// Workstation 没有可用的截图 API：vmrest 无截图端点；vmrun captureScreen 在
// Workstation 上被当作 guest 操作执行，直接报
// 「Anonymous guest operations are not allowed ... VixVM_LoginInGuest」。
// 这里改为复用驱动的 RemoteDisplay VNC 通道（与前端 noVNC 控制台同源）
// 读取一帧帧缓冲并转成 PNG。
func (d *WorkstationDriver) GetScreenshot(vmUUID string) (string, error) {
	if d.GetPower(vmUUID) != "运行中" {
		return "", fmt.Errorf("虚拟机未运行，无法截图")
	}
	host, port, err := d.vncCaptureEndpoint(vmUUID)
	if err != nil {
		return "", err
	}
	pngData, err := captureVNCScreenshot(host, port, 12*time.Second)
	if err != nil {
		return "", fmt.Errorf("VNC 抓屏失败: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData), nil
}

// vncCaptureEndpoint 解析截图使用的 VNC 地址与端口。
//
// 端口以 VMX 中的 RemoteDisplay.vnc.port 为准（外部创建的虚拟机可能不在
// system_path 下、端口也与配置中心记录的不一致），VMX 不可用时回退到
// 配置中心记录的 vc_port。
func (d *WorkstationDriver) vncCaptureEndpoint(vmUUID string) (string, int, error) {
	vmx := d.wsVMXPath(vmUUID)
	if vmx == "" {
		vmx = d.vmxPath(vmUUID)
	}

	fallback := ""
	d.mu.RLock()
	if cfg, ok := d.VmSaving[vmUUID]; ok && cfg != nil {
		fallback = cfg.VcPort
	}
	d.mu.RUnlock()

	port, err := resolveVNCConfigPort(vmx, fallback)
	if err != nil {
		return "", 0, err
	}

	host, _ := d.wsHostPort()
	if wsIsLocalAddress(host) {
		host = "127.0.0.1"
	}
	return host, port, nil
}

// resolveVNCConfigPort 决定截图使用的 VNC 端口：
// VMX 中的 RemoteDisplay.vnc.port 优先，其次配置中心记录的 vc_port。
// VMX 明确关闭 VNC 时直接返回可操作的提示，避免无意义的连接尝试。
func resolveVNCConfigPort(vmxPath, fallbackPort string) (int, error) {
	if strings.TrimSpace(vmxPath) == "" {
		return 0, fmt.Errorf("无法定位虚拟机配置文件，无法截图")
	}
	// 配置文件读不到时只能凭配置端口尝试（虚拟机可能不在 system_path 下）
	if _, err := os.Stat(vmxPath); err != nil {
		if n, e := strconv.Atoi(strings.TrimSpace(fallbackPort)); e == nil && n > 0 {
			return n, nil
		}
		return 0, fmt.Errorf("未找到虚拟机配置文件: %s", vmxPath)
	}

	found, enabled, vncPort := readVMXVNCConfig(vmxPath)
	if !found || !enabled {
		return 0, fmt.Errorf("虚拟机未启用 VNC 控制台，无法截图；开机会自动补配置，请关机重启后重试")
	}
	if vncPort > 0 {
		return vncPort, nil
	}
	if n, err := strconv.Atoi(strings.TrimSpace(fallbackPort)); err == nil && n > 0 {
		return n, nil
	}
	return 0, fmt.Errorf("虚拟机未配置 VNC 控制台端口，无法截图")
}

// ensureVMXVNC 在开机前补齐 VNC 控制台配置。
//
// 只在 VMX 完全没有 RemoteDisplay.vnc.* 时追加，不覆盖已有配置：
// 用户显式关闭 VNC 时保持关闭。端口优先取主机配置里的 vc_port，
// 否则随机分配（与 CreateVM 一致）。
func ensureVMXVNC(vmxPath, fallbackPort string) error {
	if strings.TrimSpace(vmxPath) == "" {
		return nil
	}
	if found, _, _ := readVMXVNCConfig(vmxPath); found {
		return nil
	}
	port := 0
	if n, err := strconv.Atoi(strings.TrimSpace(fallbackPort)); err == nil && n > 0 {
		port = n
	}
	if port <= 0 {
		if n, err := strconv.Atoi(RandomVNCPort()); err == nil && n > 0 {
			port = n
		}
	}
	if port <= 0 {
		port = 5900
	}

	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return err
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += fmt.Sprintf("RemoteDisplay.vnc.enabled = \"TRUE\"\nRemoteDisplay.vnc.port = \"%d\"\n", port)
	return os.WriteFile(vmxPath, []byte(text), 0o644)
}

// readVMXVNCConfig 读取 VMX 中的 VNC 控制台配置。
// found 表示 VMX 中是否存在 RemoteDisplay.vnc.* 键，用于区分「明确关闭」与「文件不可读」。
func readVMXVNCConfig(vmxPath string) (found bool, enabled bool, port int) {
	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return false, false, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		if !strings.HasPrefix(key, "remotedisplay.vnc.") {
			continue
		}
		found = true
		val := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		switch key {
		case "remotedisplay.vnc.enabled":
			enabled = strings.EqualFold(val, "TRUE")
		case "remotedisplay.vnc.port":
			if n, err := strconv.Atoi(val); err == nil {
				port = n
			}
		}
	}
	return found, enabled, port
}

// GetPCIList Workstation 不支持 PCI 直通（与 Python 版 PCIShows 返回空一致）
func (d *WorkstationDriver) GetPCIList() (map[string]*model.VFConfig, error) {
	return make(map[string]*model.VFConfig), nil
}

// GetGPUList Workstation 不支持 GPU 直通
func (d *WorkstationDriver) GetGPUList() (map[string]*model.VFConfig, error) {
	return make(map[string]*model.VFConfig), nil
}

// SetupPCI Workstation 无 PCI 直通能力，明确拒绝
func (d *WorkstationDriver) SetupPCI(vmUUID string, cfg *model.VFConfig, pciKey string, add bool) (*model.ZMessage, error) {
	return &model.ZMessage{Success: false, Action: "PCISetup",
		Message: "VMware Workstation 不支持 PCI 设备直通"}, nil
}

// GetUSBList 枚举宿主机 USB 设备（Windows 走 PowerShell，其它平台走 lsusb）
func (d *WorkstationDriver) GetUSBList() (map[string]*model.USBInfos, error) {
	result := make(map[string]*model.USBInfos)

	// Windows：Get-PnpDevice（与 Python 版一致）
	psScript := `$ErrorActionPreference='SilentlyContinue'; ` +
		`Get-PnpDevice -Class USB -PresentOnly | Where-Object { $_.InstanceId -like 'USB\VID_*' } | ` +
		`ForEach-Object { $id=$_.InstanceId; $name=$_.FriendlyName; ` +
		`if ($id -match 'VID_([0-9A-Fa-f]{4})&PID_([0-9A-Fa-f]{4})') { "$($Matches[1])|$($Matches[2])|$name" } }`

	if out, _, err := security.SafeExecWithTimeout(
		[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", psScript},
		30*time.Second); err == nil {
		for _, line := range splitLines(out) {
			parts := strings.SplitN(line, "|", 3)
			if len(parts) < 2 {
				continue
			}
			vid := strings.ToLower(strings.TrimSpace(parts[0]))
			pid := strings.ToLower(strings.TrimSpace(parts[1]))
			if vid == "" || pid == "" {
				continue
			}
			hint := vid + ":" + pid
			if len(parts) > 2 {
				hint = strings.TrimSpace(parts[2])
			}
			key := vid + ":" + pid
			result[key] = &model.USBInfos{
				UsbKey: key, VidUUID: vid, PidUUID: pid, UsbHint: hint,
			}
		}
	}

	// 非 Windows（或 PowerShell 不可用）：回退 lsusb
	if len(result) == 0 {
		if res, _, err := security.SafeExecWithTimeout([]string{"lsusb"}, 15*time.Second); err == nil {
			for _, line := range strings.Split(res, "\n") {
				if !strings.Contains(line, "ID ") {
					continue
				}
				idPart := strings.SplitN(line, "ID ", 2)[1]
				fields := strings.SplitN(strings.TrimSpace(idPart), " ", 2)
				if len(fields) == 0 || !strings.Contains(fields[0], ":") {
					continue
				}
				pair := strings.SplitN(fields[0], ":", 2)
				hint := fields[0]
				if len(fields) > 1 {
					hint = strings.TrimSpace(fields[1])
				}
				result[fields[0]] = &model.USBInfos{
					UsbKey: fields[0], VidUUID: pair[0], PidUUID: pair[1], UsbHint: hint,
				}
			}
		}
	}

	return result, nil
}

// SetupUSB USB 直通：写入 VMX 配置并记录（Workstation 需重启虚拟机生效）
//
// 与 Python 版一致：更新虚拟机配置中的 USB 列表；VMX 侧的 usb_x 条目
// 在下次冷启动时由 Workstation 读取，因此这里如实提示「需重启生效」。
func (d *WorkstationDriver) SetupUSB(vmUUID string, info *model.USBInfos, usbKey string, add bool) (*model.ZMessage, error) {
	d.mu.Lock()
	cfg, ok := d.VmSaving[vmUUID]
	if !ok {
		d.mu.Unlock()
		return &model.ZMessage{Success: false, Action: "USBSetup", Message: "虚拟机不存在"}, nil
	}
	if cfg.UsbAll == nil {
		cfg.UsbAll = make(map[string]model.USBInfos)
	}
	if add {
		cfg.UsbAll[usbKey] = *info
	} else {
		delete(cfg.UsbAll, usbKey)
	}
	d.mu.Unlock()

	action := "添加"
	if !add {
		action = "移除"
	}
	return &model.ZMessage{Success: true, Action: "USBSetup",
		Message: fmt.Sprintf("USB 设备%s成功（需重启虚拟机生效）", action)}, nil
}

// MountUSB Workstation 不支持宿主 USB 存储直挂（与 Python 版一致）
func (d *WorkstationDriver) MountUSB(vmUUID string, usbKey string, mount bool) (*model.ZMessage, error) {
	return &model.ZMessage{Success: false, Action: "USBMount",
		Message: "VMware Workstation 不支持 USB 存储直挂，请使用 USB 直通"}, nil
}
