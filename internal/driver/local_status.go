// Package driver 本机硬件状态采集
//
// 对应 Python 版 BasicServer.local_get_hw_status()（psutil 实现）。
// 用于「驱动运行在 IDCClient 所在机器」的平台（Hyper-V / QEMU / VirtualBox /
// MEmu / VMware Workstation 等）。
//
// 注意：远程平台（Proxmox / ESXi 等）必须使用各自 API 的实现，
// 否则会把 IDCClient 本机的资源误报为远程主机状态。
package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// localStatusCacheTTL 本机状态缓存时间。
//
// 前端每 10 秒轮询一次主机状态，而本机采集需要执行系统命令
// （Windows 下 PowerShell 单次约 3 秒），因此缓存 30 秒以降低开销。
const localStatusCacheTTL = 30 * time.Second

// localStatusEntry 单条本机状态缓存。
// 磁盘统计依赖主机配置里的各个存储路径，同一台机器上不同主机可能配置不同目录，
// 因此缓存按路径组合分桶，避免 A 主机的磁盘读数被 B 主机复用。
type localStatusEntry struct {
	hw     *model.HWStatus
	cached time.Time
}

var (
	localStatusMu    sync.Mutex
	localStatusCache = map[string]*localStatusEntry{}
)

// hostDiskRoles 主机磁盘的用途顺序，同时用于生成稳定的缓存键。
var hostDiskRoles = []string{"system", "data", "images", "iso", "backup"}

// hostDiskPaths 汇总主机配置里各个存储用途对应的路径（未填写的用途不返回）。
//
// 键与前端展示一一对应：
//   - system  系统存储路径
//   - data    数据存储路径
//   - images  模板/镜像存储路径
//   - iso     光盘（ISO）存储路径
//   - backup  备份存储路径
func hostDiskPaths(cfg *model.HSConfig) map[string]string {
	paths := make(map[string]string, len(hostDiskRoles))
	if cfg == nil {
		return paths
	}
	add := func(role, value string) {
		if v := strings.TrimSpace(value); v != "" {
			paths[role] = v
		}
	}
	add("system", cfg.SystemPath)
	add("data", cfg.ExternPath)
	add("images", cfg.ImagesPath)
	add("iso", cfg.DvdromPath)
	add("backup", cfg.BackupPath)
	return paths
}

// diskCacheKey 由用途与路径生成缓存键，路径变更后自然失效。
func diskCacheKey(paths map[string]string) string {
	var b strings.Builder
	for _, role := range hostDiskRoles {
		p, ok := paths[role]
		if !ok {
			continue
		}
		b.WriteString(role)
		b.WriteByte('=')
		b.WriteString(p)
		b.WriteByte('\x00')
	}
	return b.String()
}

// localHWStatus 采集本机硬件状态（带短缓存）。
//
// 磁盘部分只统计主机配置里已填写的存储路径；未填写时不采集，
// 避免把宿主机其它盘（如系统盘 C:）误显示为虚拟机可用空间。
func localHWStatus(cfg *model.HSConfig) *model.HWStatus {
	paths := hostDiskPaths(cfg)
	key := diskCacheKey(paths)

	localStatusMu.Lock()
	defer localStatusMu.Unlock()

	if entry, ok := localStatusCache[key]; ok && entry != nil && time.Since(entry.cached) < localStatusCacheTTL {
		snapshot := *entry.hw
		return &snapshot
	}

	hw := &model.HWStatus{OnUpdate: time.Now().Unix()}
	var windowsDrives map[string][]int
	switch runtime.GOOS {
	case "windows":
		windowsDrives = fillLocalStatusWindows(hw)
	case "linux":
		fillLocalStatusLinux(hw)
	case "darwin":
		fillLocalStatusDarwin(hw)
	}
	hw.DiskUsage = collectLocalDiskUsage(paths, windowsDrives)
	if usage, ok := hw.DiskUsage["system"]; ok {
		hw.HddTotal, hw.HddUsage = usage[0], usage[1]
	}
	if hw.ExtUsage == nil {
		hw.ExtUsage = map[string][]int{}
	}
	if hw.GpuUsage == nil {
		hw.GpuUsage = map[string]float64{}
	}

	localStatusCache[key] = &localStatusEntry{hw: hw, cached: time.Now()}
	snapshot := *hw
	return &snapshot
}

// collectLocalDiskUsage 按用途采集本机磁盘容量（MB）。
// Windows 复用状态采集脚本返回的逻辑盘列表；Linux/macOS 逐路径执行 df。
func collectLocalDiskUsage(paths map[string]string, windowsDrives map[string][]int) map[string][]int {
	usage := make(map[string][]int, len(paths))
	if len(paths) == 0 {
		return usage
	}
	if runtime.GOOS == "windows" {
		for role, path := range paths {
			drive := windowsDriveForPath(path)
			if drive == "" {
				continue
			}
			if u, ok := windowsDrives[drive]; ok {
				usage[role] = u
			}
		}
		return usage
	}
	// 同一路径可能被多个用途引用（例如镜像与系统盘同盘），只查询一次
	byPath := map[string][]int{}
	for role, path := range paths {
		if u, ok := byPath[path]; ok {
			usage[role] = u
			continue
		}
		total, used, ok := diskUsageViaDF(path)
		if !ok {
			continue
		}
		u := []int{total, used}
		byPath[path] = u
		usage[role] = u
	}
	return usage
}

// emptyHWStatus 返回仅带时间戳的空状态。
//
// 供「远程平台」采集失败时使用：远程平台的失败不能用本机资源顶上，
// 否则会把 IDCClient 所在机器的 CPU/内存显示为远程主机状态。
func emptyHWStatus() *model.HWStatus {
	return &model.HWStatus{
		OnUpdate:  time.Now().Unix(),
		ExtUsage:  map[string][]int{},
		DiskUsage: map[string][]int{},
		GpuUsage:  map[string]float64{},
	}
}

// fillLocalStatusLinux 通过 /proc 与 statfs 采集（无外部依赖）
func fillLocalStatusLinux(hw *model.HWStatus) {
	// CPU 使用率
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "cpu ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 8 {
				break
			}
			var total, idle int64
			for i, f := range fields[1:] {
				var v int64
				fmt.Sscanf(f, "%d", &v)
				total += v
				if i == 3 || i == 4 { // idle + iowait
					idle += v
				}
			}
			if total > 0 {
				hw.CpuUsage = int((total - idle) * 100 / total)
			}
			break
		}
	}

	// CPU 核数与型号
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		cores := 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "processor") {
				cores++
			}
			if hw.CpuModel == "" && strings.HasPrefix(line, "model name") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					hw.CpuModel = strings.TrimSpace(line[idx+1:])
				}
			}
		}
		hw.CpuTotal = cores
	}

	// 内存
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			var kb int64
			fmt.Sscanf(fields[1], "%d", &kb)
			switch strings.TrimSuffix(fields[0], ":") {
			case "MemTotal":
				hw.MemTotal = int(kb / 1024)
			case "MemAvailable":
				if hw.MemTotal > 0 {
					hw.MemUsage = hw.MemTotal - int(kb/1024)
				}
			}
		}
	}

}

// diskUsageViaDF 通过 `df -k <path>` 采集指定路径所在分区的总量与已用量（MB）。
// 路径不存在或无权限时返回 false，由调用方跳过该用途。
func diskUsageViaDF(path string) (int, int, bool) {
	p := strings.TrimSpace(path)
	if p == "" {
		return 0, 0, false
	}
	out, _, err := security.SafeExecWithTimeout([]string{"df", "-k", p}, 5*time.Second)
	if err != nil {
		return 0, 0, false
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, false
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 3 {
		return 0, 0, false
	}
	var totalKB, usedKB int64
	if _, err := fmt.Sscanf(fields[1], "%d", &totalKB); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(fields[2], "%d", &usedKB); err != nil {
		return 0, 0, false
	}
	return int(totalKB / 1024), int(usedKB / 1024), true
}

// fillLocalStatusWindows 通过 PowerShell + CIM 采集 CPU/内存，并返回各逻辑盘的用量。
// 磁盘与主机配置路径的映射由 collectLocalDiskUsage 完成。
func fillLocalStatusWindows(hw *model.HWStatus) map[string][]int {
	script := `$ErrorActionPreference='SilentlyContinue'; ` +
		`$os = Get-CimInstance Win32_OperatingSystem; ` +
		`$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1; ` +
		`$drives = @(Get-CimInstance Win32_LogicalDisk | Where-Object { $_.DriveType -eq 3 } | ForEach-Object { ` +
		`[pscustomobject]@{ id=$_.DeviceID; total=[math]::Round($_.Size/1MB); used=[math]::Round(($_.Size-$_.FreeSpace)/1MB) } }); ` +
		`[pscustomobject]@{ ` +
		`cpu_total=$cpu.NumberOfLogicalProcessors; ` +
		`cpu_model=$cpu.Name; ` +
		`cpu_usage=$cpu.LoadPercentage; ` +
		`mem_total=[math]::Round($os.TotalVisibleMemorySize/1024); ` +
		`mem_usage=[math]::Round(($os.TotalVisibleMemorySize-$os.FreePhysicalMemory)/1024); ` +
		`drives=$drives ` +
		`} | ConvertTo-Json -Compress -Depth 4`

	drives := map[string][]int{}
	out, _, err := security.SafeExecWithTimeout(
		[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", script},
		25*time.Second)
	if err != nil {
		return drives
	}

	var parsed struct {
		CpuTotal int    `json:"cpu_total"`
		CpuModel string `json:"cpu_model"`
		CpuUsage int    `json:"cpu_usage"`
		MemTotal int    `json:"mem_total"`
		MemUsage int    `json:"mem_usage"`
		Drives   []struct {
			ID    string `json:"id"`
			Total int    `json:"total"`
			Used  int    `json:"used"`
		} `json:"drives"`
	}
	body := strings.TrimSpace(out)
	if body == "" {
		return drives
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return drives
	}
	hw.CpuTotal = parsed.CpuTotal
	hw.CpuModel = parsed.CpuModel
	hw.CpuUsage = parsed.CpuUsage
	hw.MemTotal = parsed.MemTotal
	hw.MemUsage = parsed.MemUsage
	for _, d := range parsed.Drives {
		id := strings.ToUpper(strings.TrimSpace(d.ID))
		if id == "" {
			continue
		}
		drives[id] = []int{d.Total, d.Used}
	}
	return drives
}

// windowsDriveForPath 返回路径所在逻辑盘（如 "D:"）。
// 空路径、相对路径与 UNC 路径无法定位逻辑盘，返回空字符串表示不采集磁盘。
func windowsDriveForPath(path string) string {
	p := strings.TrimSpace(path)
	if len(p) < 2 || p[1] != ':' {
		return ""
	}
	drive := strings.ToUpper(p[:1])
	if drive < "A" || drive > "Z" {
		return ""
	}
	return drive + ":"
}

// fillLocalStatusDarwin 通过 sysctl 采集 CPU/内存（磁盘由路径统一采集）
func fillLocalStatusDarwin(hw *model.HWStatus) {
	if out, _, err := security.SafeExecWithTimeout([]string{"sysctl", "-n", "hw.ncpu"}, 5*time.Second); err == nil {
		fmt.Sscanf(strings.TrimSpace(out), "%d", &hw.CpuTotal)
	}
	if out, _, err := security.SafeExecWithTimeout([]string{"sysctl", "-n", "machdep.cpu.brand_string"}, 5*time.Second); err == nil {
		hw.CpuModel = strings.TrimSpace(out)
	}
	if out, _, err := security.SafeExecWithTimeout([]string{"sysctl", "-n", "hw.memsize"}, 5*time.Second); err == nil {
		var bytes int64
		fmt.Sscanf(strings.TrimSpace(out), "%d", &bytes)
		if bytes > 0 {
			hw.MemTotal = int(bytes / 1024 / 1024)
		}
	}
}
