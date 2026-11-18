// Package driver 容器类平台的虚拟机实时用量采集。
//
// Docker / LXD 的 guest 内没有 Agent 上报，列表页的 CPU/内存/硬盘/流量
// 一直显示 0。这里直接从运行时读取：
//   - Docker：docker stats（一次取全部容器）+ docker ps -s（可写层大小）
//   - LXD   ：GET /1.0/instances/<name>/state + 容器内 du（LXD 5.0 的 state.disk 为空）
//
// 采集结果按主机做短缓存，避免列表页逐台查询把 docker stats 的 1 秒采样
// 放大成 N 秒。SmolVM 没有 guest 内采集通道，保持不提供统计数据。
package driver

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// VMStats 单个虚拟机的实时用量（列表页展示用）
type VMStats struct {
	CpuUsage int   // CPU 使用率（%，相对分配的 vCPU）
	MemTotal int   // 内存上限（MB）
	MemUsage int   // 已用内存（MB）
	HddTotal int   // 磁盘上限（MB）
	HddUsage int   // 已用磁盘（MB）
	NetUp    int64 // 累计上行字节
	NetDown  int64 // 累计下行字节
}

// VMStatsProvider 平台能直接提供 guest 用量时实现该接口
type VMStatsProvider interface {
	GetVMStats(vmUUID string) (*VMStats, bool)
}

// VMStatsCachedProvider 允许读取「任意年龄」的缓存值。
//
// 列表页先用旧值立即渲染，再异步刷新，避免 docker stats 的 1 秒采样
// 把每次页面加载都拖住。
type VMStatsCachedProvider interface {
	PeekVMStats(vmUUID string) (*VMStats, bool)
}

func peekVMStats(key string) (*VMStats, bool) {
	vmStatsCacheMu.Lock()
	defer vmStatsCacheMu.Unlock()
	e, ok := vmStatsCache[key]
	if !ok {
		return nil, false
	}
	return e.stats, true
}

// PeekVMStats 返回缓存中的用量（不论是否过期）
func (d *DockerDriver) PeekVMStats(vmUUID string) (*VMStats, bool) {
	return peekVMStats(vmStatsKey("docker", d.Config, vmUUID))
}

// PeekVMStats 返回缓存中的用量（不论是否过期）
func (d *LXDDriver) PeekVMStats(vmUUID string) (*VMStats, bool) {
	return peekVMStats(vmStatsKey("lxd", d.Config, vmUUID))
}

// vmStatsCache 主机维度的短缓存（key = 平台|主机|虚拟机）
var (
	vmStatsCacheMu sync.Mutex
	vmStatsCache   = map[string]vmStatsCacheEntry{}
)

type vmStatsCacheEntry struct {
	stats *VMStats
	at    time.Time
}

func getVMStatsCached(key string, ttl time.Duration) (*VMStats, bool) {
	vmStatsCacheMu.Lock()
	defer vmStatsCacheMu.Unlock()
	e, ok := vmStatsCache[key]
	if !ok || time.Since(e.at) > ttl {
		return nil, false
	}
	return e.stats, true
}

func putVMStatsCached(key string, stats *VMStats) {
	vmStatsCacheMu.Lock()
	vmStatsCache[key] = vmStatsCacheEntry{stats: stats, at: time.Now()}
	vmStatsCacheMu.Unlock()
}

func vmStatsKey(platform string, cfg *model.HSConfig, vmUUID string) string {
	host := ""
	if cfg != nil {
		host = cfg.ServerAddr + "|" + cfg.ServerName
	}
	return platform + "|" + host + "|" + vmUUID
}

// vmStatsBase 读取配置中的资源上限
func (b *BaseDriver) vmStatsBase(vmUUID string) *VMStats {
	cfg := b.GetVMSaving()[vmUUID]
	if cfg == nil {
		return &VMStats{}
	}
	return &VMStats{
		MemTotal: cfg.MemNum,
		HddTotal: cfg.HddNum,
	}
}

// bytesToMBUp 向上取整到 MB：容器用量常不足 1MB，直接整除会显示成 0。
func bytesToMBUp(n int64) int {
	if n <= 0 {
		return 0
	}
	const mb = int64(1024 * 1024)
	return int((n + mb - 1) / mb)
}

// parseDockerSize 解析 docker 的容量字符串（1.5MiB / 0B / 1.2kB）
func parseDockerSize(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	mult := float64(1)
	switch unit {
	case "b", "":
		mult = 1
	case "kb":
		mult = 1000
	case "kib":
		mult = 1024
	case "mb":
		mult = 1000 * 1000
	case "mib":
		mult = 1024 * 1024
	case "gb":
		mult = 1000 * 1000 * 1000
	case "gib":
		mult = 1024 * 1024 * 1024
	case "tb":
		mult = 1000 * 1000 * 1000 * 1000
	case "tib":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		mult = 1
	}
	return int64(num * mult)
}

// parseDockerPair 解析 "a / b" 双值（内存、BlockIO、NetIO）
func parseDockerPair(raw string) (int64, int64) {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	return parseDockerSize(parts[0]), parseDockerSize(parts[1])
}

// GetVMStats Docker 容器实时用量
//
// docker stats --no-stream 单次采样约 1 秒，因此一次拉取主机上全部容器并缓存，
// 列表页逐台读取时不会重复等待。
func (d *DockerDriver) GetVMStats(vmUUID string) (*VMStats, bool) {
	key := vmStatsKey("docker", d.Config, vmUUID)
	if stats, ok := getVMStatsCached(key, 30*time.Second); ok {
		return stats, true
	}

	// 一次 SSH 里同时取 stats 与可写层大小：分开执行要多付一次 SSH 握手（约 1 秒）
	const split = "__DOCKERSPLIT__"
	script := fmt.Sprintf(
		"docker stats --no-stream --format %s; echo %s; docker ps -a -s --format %s",
		shellQuote("{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.NetIO}}"), split,
		shellQuote("{{.Names}}|{{.Size}}"))
	out, err := d.execHostArgs([]string{"sh", "-c", script}, 30*time.Second)
	if err != nil {
		return nil, false
	}
	statsOut, sizeOut := out, ""
	if idx := strings.Index(out, split); idx >= 0 {
		statsOut = out[:idx]
		sizeOut = out[idx+len(split):]
	}

	sizeMap := map[string]string{}
	for _, line := range strings.Split(sizeOut, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) == 2 {
			sizeMap[parts[0]] = parts[1]
		}
	}

	for _, line := range strings.Split(strings.TrimSpace(statsOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 4 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		stats := d.vmStatsBase(name)
		if p := strings.TrimSuffix(strings.TrimSpace(parts[1]), "%"); p != "" {
			if v, convErr := strconv.ParseFloat(p, 64); convErr == nil {
				stats.CpuUsage = int(v + 0.5)
				if stats.CpuUsage > 100 {
					stats.CpuUsage = 100
				}
			}
		}
		memUsed, memLimit := parseDockerPair(parts[2])
		if memLimit > 0 {
			stats.MemTotal = int(memLimit / 1024 / 1024)
		}
		stats.MemUsage = bytesToMBUp(memUsed)
		netIn, netOut := parseDockerPair(parts[3])
		stats.NetDown, stats.NetUp = netIn, netOut

		if size, ok := sizeMap[name]; ok {
			if idx := strings.Index(size, "("); idx > 0 {
				size = strings.TrimSpace(size[:idx])
			}
			stats.HddUsage = bytesToMBUp(parseDockerSize(size))
		}
		putVMStatsCached(vmStatsKey("docker", d.Config, name), stats)
	}

	if stats, ok := getVMStatsCached(key, 30*time.Second); ok {
		return stats, true
	}
	return nil, false
}

// ============================================================
// QEMU / KVM（virsh domstats）
// ============================================================

var (
	qemuCPUMu     sync.Mutex
	qemuCPUSample = map[string]struct {
		at time.Time
		ns int64
	}{}
)

// GetVMStats QEMU/KVM 虚拟机实时用量
//
// virsh domstats 提供累计 CPU 时间、balloon 内存、块设备分配量与网卡计数；
// CPU 百分比用两次采样差值计算（与 LXD 一致）。
func (d *QEMUDriver) GetVMStats(vmUUID string) (*VMStats, bool) {
	key := vmStatsKey("qemu", d.Config, vmUUID)
	if stats, ok := getVMStatsCached(key, 15*time.Second); ok {
		return stats, true
	}
	out, err := d.execHostArgs([]string{
		"virsh", "domstats", vmUUID,
		"--state", "--cpu-total", "--balloon", "--block", "--interface",
	}, 20*time.Second)
	if err != nil {
		return nil, false
	}
	stats := d.vmStatsBase(vmUUID)
	var cpuNS int64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Domain:") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		val := strings.TrimSpace(kv[1])
		switch {
		case kv[0] == "cpu.time":
			cpuNS, _ = strconv.ParseInt(val, 10, 64)
		case kv[0] == "balloon.rss":
			// rss 才是 guest 内实际驻留内存；balloon.current 是分配上限
			if kb, e := strconv.ParseInt(val, 10, 64); e == nil && kb > 0 {
				stats.MemUsage = bytesToMBUp(kb * 1024)
			}
		case kv[0] == "balloon.maximum":
			if kb, e := strconv.ParseInt(val, 10, 64); e == nil && kb > 0 {
				stats.MemTotal = int(kb / 1024)
			}
		case strings.HasSuffix(kv[0], ".allocation") && strings.HasPrefix(kv[0], "block."):
			if b, e := strconv.ParseInt(val, 10, 64); e == nil {
				stats.HddUsage += bytesToMBUp(b)
			}
		case strings.HasSuffix(kv[0], ".rx.bytes"):
			if b, e := strconv.ParseInt(val, 10, 64); e == nil {
				stats.NetDown += b
			}
		case strings.HasSuffix(kv[0], ".tx.bytes"):
			if b, e := strconv.ParseInt(val, 10, 64); e == nil {
				stats.NetUp += b
			}
		}
	}

	cores := 1
	if cfg := d.GetVMSaving()[vmUUID]; cfg != nil && cfg.CpuNum > 0 {
		cores = cfg.CpuNum
	}
	now := time.Now()
	qemuCPUMu.Lock()
	prev, seen := qemuCPUSample[vmUUID]
	pct := 0
	if seen && now.Sub(prev.at) >= 500*time.Millisecond && cpuNS >= prev.ns {
		pct = int(float64(cpuNS-prev.ns) / 1e9 / now.Sub(prev.at).Seconds() * 100 / float64(cores))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		qemuCPUSample[vmUUID] = struct {
			at time.Time
			ns int64
		}{now, cpuNS}
	} else if !seen {
		qemuCPUSample[vmUUID] = struct {
			at time.Time
			ns int64
		}{now, cpuNS}
	}
	qemuCPUMu.Unlock()
	stats.CpuUsage = pct

	putVMStatsCached(key, stats)
	return stats, true
}

// PeekVMStats 返回缓存中的用量（不论是否过期）
func (d *QEMUDriver) PeekVMStats(vmUUID string) (*VMStats, bool) {
	return peekVMStats(vmStatsKey("qemu", d.Config, vmUUID))
}

// lxdStateResponse LXD GET /1.0/instances/<name>/state 的字段
type lxdStateResponse struct {
	CPU struct {
		Usage int64 `json:"usage"`
	} `json:"cpu"`
	Memory struct {
		Usage int64 `json:"usage"`
	} `json:"memory"`
	Disk map[string]struct {
		Usage int64 `json:"usage"`
	} `json:"disk"`
	Network map[string]struct {
		Counters struct {
			BytesReceived int64 `json:"bytes_received"`
			BytesSent     int64 `json:"bytes_sent"`
		} `json:"counters"`
	} `json:"network"`
}

var (
	lxdCPUMu     sync.Mutex
	lxdCPUSample = map[string]struct {
		at  time.Time
		ns  int64
		pct int
	}{}
)

// GetVMStats LXD 容器实时用量
func (d *LXDDriver) GetVMStats(vmUUID string) (*VMStats, bool) {
	key := vmStatsKey("lxd", d.Config, vmUUID)
	if stats, ok := getVMStatsCached(key, 30*time.Second); ok {
		return stats, true
	}

	// 一次 SSH 内完成 state 查询与磁盘统计：
	//   - 不走 lxc exec（会等待容器内进程，偶发长时间卡住）
	//   - 直接在宿主机对容器 rootfs 目录 du（dir 存储池，毫秒级）
	const split = "__LXDSPLIT__"
	instURL := shellQuote("/1.0/instances/" + vmUUID)
	poolScript := fmt.Sprintf(
		`P=$(lxc query %s | sed -n 's/.*"pool": "\([^"]*\)".*/\1/p' | head -1); [ -z "$P" ] && P=default; `+
			`S=$(lxc query /1.0/storage-pools/$P | sed -n 's/.*"source": "\([^"]*\)".*/\1/p' | head -1); `+
			`du -skx "$S/containers/"%s 2>/dev/null | cut -f1`,
		instURL, shellQuote(vmUUID))
	script := fmt.Sprintf("lxc query %s/state; echo %s; %s", instURL, split, poolScript)
	out, err := d.execHostArgs([]string{"sh", "-c", script}, 30*time.Second)
	if err != nil {
		return nil, false
	}
	stateOut, diskOut := out, ""
	if idx := strings.Index(out, split); idx >= 0 {
		stateOut = out[:idx]
		diskOut = out[idx+len(split):]
	}
	var st lxdStateResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stateOut)), &st); err != nil {
		return nil, false
	}

	stats := d.vmStatsBase(vmUUID)
	stats.MemUsage = bytesToMBUp(st.Memory.Usage)
	if root, ok := st.Disk["root"]; ok && root.Usage > 0 {
		stats.HddUsage = bytesToMBUp(root.Usage)
	} else {
		fields := strings.Fields(strings.TrimSpace(diskOut))
		if len(fields) > 0 {
			if kb, convErr := strconv.ParseInt(fields[0], 10, 64); convErr == nil {
				stats.HddUsage = bytesToMBUp(kb * 1024)
			}
		}
	}
	for _, nic := range st.Network {
		stats.NetDown += nic.Counters.BytesReceived
		stats.NetUp += nic.Counters.BytesSent
	}

	// CPU：LXD 只给累计纳秒，用两次采样差值算百分比
	cores := 1
	if cfg := d.GetVMSaving()[vmUUID]; cfg != nil && cfg.CpuNum > 0 {
		cores = cfg.CpuNum
	}
	now := time.Now()
	lxdCPUMu.Lock()
	prev, seen := lxdCPUSample[vmUUID]
	pct := 0
	if seen && now.Sub(prev.at) >= 500*time.Millisecond && st.CPU.Usage >= prev.ns {
		deltaNS := float64(st.CPU.Usage - prev.ns)
		elapsed := now.Sub(prev.at).Seconds()
		pct = int(deltaNS / 1e9 / elapsed * 100 / float64(cores))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		lxdCPUSample[vmUUID] = struct {
			at  time.Time
			ns  int64
			pct int
		}{now, st.CPU.Usage, pct}
	} else if !seen {
		lxdCPUSample[vmUUID] = struct {
			at  time.Time
			ns  int64
			pct int
		}{now, st.CPU.Usage, 0}
	}
	lxdCPUMu.Unlock()
	stats.CpuUsage = pct

	putVMStatsCached(key, stats)
	return stats, true
}
