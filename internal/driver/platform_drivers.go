// Package driver 各平台驱动实现
// 所有驱动使用 security.SafeExec 或 security.SafeShellExec 进行命令执行
package driver

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// ============================================================
// LXC/LXD 驱动（通过 lxc 命令行）
// ============================================================

// 说明：vSphere/ESXi 引擎统一由 esxi_driver.go + esxi_ext.go 的 ESXiDriver 实现
// （基于 govmomi SDK，覆盖创建/删除/电源/快照/直通/截图等完整能力）。
// 历史上此处还有一个基于 vSphere REST 的 VSphereDriver 精简版，因从未被
// createDriver 注册（属死代码），且与 ESXiDriver 构成「同一引擎两套实现」的
// 隐患，已删除以免误用。

// LXDDriver LXC/LXD 驱动
type LXDDriver struct{ *BaseDriver }

func NewLXDDriver(cfg *model.HSConfig) *LXDDriver {
	return &LXDDriver{BaseDriver: NewBaseDriver(cfg)}
}

func (d *LXDDriver) GetPower(vmUUID string) string {
	out, err := d.execHostArgs([]string{"lxc", "info", vmUUID}, 10*time.Second)
	if err != nil {
		return "未知"
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "status: running") {
		return "运行中"
	}
	if strings.Contains(lower, "status: stopped") {
		return "已关机"
	}
	if strings.Contains(lower, "status: frozen") {
		return "已暂停"
	}
	return "未知"
}

// GetStatus 获取 LXD 主机状态。
//
// 与 Python 版 LXContainer.HSStatus 一致：优先解析 `lxc info` 的
// CPU 核数与内存总量；原实现读取输出后直接丢弃，状态恒为空。
func (d *LXDDriver) GetStatus() (*model.HWStatus, error) {
	// 先采集宿主机通用指标（CPU 使用率/内存/磁盘），再用 lxc info 校正总量。
	// 原实现只解析 lxc info 的 cpu_total/memory_total，硬盘与内存使用率恒为 0，
	// 前端主机卡片因此显示不出磁盘占用。
	var hw *model.HWStatus
	if isRemoteTarget(d.Config) {
		hw = d.remoteHWStatus()
	} else {
		hw = localHWStatus(d.Config)
	}
	if hw == nil {
		hw = &model.HWStatus{}
	}
	hw.OnUpdate = time.Now().Unix()

	out, err := d.execHostArgs([]string{"lxc", "info"}, 15*time.Second)
	if err != nil || strings.TrimSpace(out) == "" {
		d.fillRootDisk(hw)
		return hw, nil
	}

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.Contains(trimmed, "cpu_total"):
			var cpus int
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "cpu_total:")), "%d", &cpus)
			if cpus > 0 {
				hw.CpuTotal = cpus
			}
		case strings.HasPrefix(trimmed, "memory_total"):
			var memBytes int64
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "memory_total:")), "%d", &memBytes)
			if memBytes > 0 {
				hw.MemTotal = int(memBytes / 1024 / 1024)
			}
		}
	}
	d.fillRootDisk(hw)
	return hw, nil
}

// rootDiskUsage 采集根分区容量（KB→MB）。
//
// Docker/LXD 的存储路径常常留空或使用 snap 私有目录（如 /var/lib/lxd 不存在），
// hostDiskPaths 取不到任何可用路径时磁盘占用就恒为 0；此时回退到根分区。
func (b *BaseDriver) rootDiskUsage() (totalMB, usedMB int, ok bool) {
	out, err := b.execHostArgs([]string{"df", "-kP", "/"}, 10*time.Second)
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
	if _, e1 := fmt.Sscanf(fields[1], "%d", &totalKB); e1 != nil {
		return 0, 0, false
	}
	if _, e2 := fmt.Sscanf(fields[2], "%d", &usedKB); e2 != nil {
		return 0, 0, false
	}
	return int(totalKB / 1024), int(usedKB / 1024), true
}

// fillRootDisk 在未采集到磁盘总量时用根分区兜底
func (b *BaseDriver) fillRootDisk(hw *model.HWStatus) {
	if hw == nil || hw.HddTotal > 0 {
		return
	}
	total, used, ok := b.rootDiskUsage()
	if !ok {
		return
	}
	hw.HddTotal, hw.HddUsage = total, used
	if hw.DiskUsage == nil {
		hw.DiskUsage = map[string][]int{}
	}
	if _, exists := hw.DiskUsage["system"]; !exists {
		hw.DiskUsage["system"] = []int{total, used}
	}
}

// ============================================================
// Docker/OCI 驱动（通过 docker 命令行）
// ============================================================

// DockerDriver Docker/OCI 驱动
type DockerDriver struct{ *BaseDriver }

func NewDockerDriver(cfg *model.HSConfig) *DockerDriver {
	return &DockerDriver{BaseDriver: NewBaseDriver(cfg)}
}

func (d *DockerDriver) GetPower(vmUUID string) string {
	out, err := d.execHostArgs(
		[]string{"docker", "inspect", "--format={{.State.Status}}", vmUUID},
		10*time.Second)
	if err != nil {
		return "未知"
	}
	switch strings.TrimSpace(out) {
	case "running":
		return "运行中"
	case "exited", "stopped":
		return "已关机"
	case "paused":
		return "已暂停"
	}
	return "未知"
}

// GetStatus 获取 Docker 主机状态。
//
// 与 Python 版 OCInterface.HSStatus 一致：读取 docker info 的
// NCPU / MemTotal / Containers 等真实字段。
// 原实现把「运行中容器数」当成 CPU 核数写入 CpuTotal，属于字段错位。
func (d *DockerDriver) GetStatus() (*model.HWStatus, error) {
	// 先采集宿主机通用指标（CPU 使用率/内存/磁盘），再用 docker info 校正总量。
	// 原实现只取 docker info 的 NCPU/MemTotal，内存使用率、硬盘容量与磁盘明细
	// 全为 0，前端因此看不到 Docker 主机的内存占用和硬盘占用。
	var hw *model.HWStatus
	if isRemoteTarget(d.Config) {
		hw = d.remoteHWStatus()
	} else {
		hw = localHWStatus(d.Config)
	}
	if hw == nil {
		hw = &model.HWStatus{}
	}
	hw.OnUpdate = time.Now().Unix()

	out, err := d.execHostArgs([]string{
		"docker", "info", "--format",
		"{{.NCPU}}|{{.MemTotal}}|{{.Containers}}|{{.ContainersRunning}}|{{.OperatingSystem}}",
	}, 10*time.Second)
	if err != nil || strings.TrimSpace(out) == "" {
		d.fillRootDisk(hw)
		return hw, nil
	}

	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) >= 2 {
		var cpus int
		fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &cpus)
		if cpus > 0 {
			hw.CpuTotal = cpus
		}
		var memBytes int64
		fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &memBytes)
		if memBytes > 0 {
			hw.MemTotal = int(memBytes / 1024 / 1024)
		}
	}
	// 操作系统名不是 CPU 型号：保留 SSH/local 采集到的真实型号，
	// 仅当型号为空时用发行版名称兜底。
	if hw.CpuModel == "" && len(parts) >= 5 {
		hw.CpuModel = strings.TrimSpace(parts[4])
	}
	d.fillRootDisk(hw)
	return hw, nil
}

func (d *DockerDriver) GetVMList() (map[string]*model.VMConfig, error) {
	out, err := d.execHostArgs(
		[]string{"docker", "ps", "-a", "--format={{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Image}}"},
		10*time.Second)
	if err != nil {
		return d.BaseDriver.GetVMList()
	}
	vms := d.GetVMSaving()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		id, name, status := parts[0], parts[1], parts[2]
		image := ""
		if len(parts) >= 4 {
			image = strings.TrimSpace(parts[3])
		}
		// 本项目以「容器名 = vm_uuid」纳管容器，docker ps 输出的 ID 只是
		// 运行时标识；若按 ID 建索引，同一容器会被重复登记一次，
		// 且后续 docker inspect/start 传 ID 与内存中的 vm_uuid 不一致。
		key := name
		if key == "" {
			key = id
		}
		if cfg, ok := vms[key]; ok {
			if strings.HasPrefix(status, "Up") {
				cfg.VmFlag = model.VMPowerStarted
			} else if strings.HasPrefix(status, "Exited") {
				cfg.VmFlag = model.VMPowerStopped
			}
			if cfg.OsName == "" && image != "" {
				cfg.OsName = image
			}
		} else {
			cfg := model.NewVMConfig()
			cfg.VmUUID = key
			cfg.OsName = image
			if strings.HasPrefix(status, "Up") {
				cfg.VmFlag = model.VMPowerStarted
			}
			vms[key] = cfg
		}
	}
	return vms, nil
}

// ============================================================
// Hyper-V 驱动（通过 PowerShell 命令行，仅 Windows）
// ============================================================

// HyperVDriver Hyper-V 驱动
type HyperVDriver struct {
	*BaseDriver
	// hvLimitMu 保护限速驱动的节流时间与开关状态
	hvLimitMu sync.Mutex
	// hvLimitAt 上一次全量同步限速规则的时间
	hvLimitAt time.Time
	// hvDevMu 保护设备枚举快照（GPU / PCI / USB 共用一次批量查询）
	hvDevMu sync.Mutex
	// hvDevAt 快照生成时间
	hvDevAt time.Time
	// hvDevData 最新设备快照
	hvDevData *hvDeviceSnapshot
	// hvDevLoading 标记后台刷新是否在飞，避免并发重复枚举
	hvDevLoading bool
}

func NewHyperVDriver(cfg *model.HSConfig) *HyperVDriver {
	return &HyperVDriver{BaseDriver: NewBaseDriver(cfg)}
}

func (d *HyperVDriver) GetPower(vmUUID string) string {
	out, _, err := security.SafeExecWithTimeout(
		[]string{"powershell", "-Command",
			hvVMExpr(vmUUID) + " | Select-Object -ExpandProperty State"},
		15*time.Second)
	if err != nil {
		return "未知"
	}
	switch strings.TrimSpace(out) {
	case "Running":
		return "运行中"
	case "Off":
		return "已关机"
	case "Paused":
		return "已暂停"
	}
	return "未知"
}

func (d *HyperVDriver) GetVMList() (map[string]*model.VMConfig, error) {
	// State 是枚举，直接序列化会变成数字（Off=3 / Running=2），
	// Go 侧按字符串解析必然失败；这里先转成字符串。
	// -InputObject 包一层 @() 保证只有一台虚拟机时也输出数组。
	out, _, err := security.SafeExecWithTimeout(
		[]string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
			"ConvertTo-Json -InputObject @(Get-VM -ErrorAction Stop | " +
				"Select-Object Id,Name,@{n='State';e={[string]$_.State}}) -Compress"},
		15*time.Second)
	if err != nil {
		return d.BaseDriver.GetVMList()
	}

	var vms []struct {
		Id    string `json:"Id"`
		Name  string `json:"Name"`
		State string `json:"State"`
	}
	body := strings.TrimSpace(out)
	if body == "" || body == "null" {
		return d.GetVMSaving(), nil
	}
	if strings.HasPrefix(body, "[") {
		if err := json.Unmarshal([]byte(body), &vms); err != nil {
			return d.BaseDriver.GetVMList()
		}
	} else {
		// 兜底：少数 PowerShell 版本仍会把单元素数组展开成对象
		var one struct {
			Id    string `json:"Id"`
			Name  string `json:"Name"`
			State string `json:"State"`
		}
		if err := json.Unmarshal([]byte(body), &one); err != nil {
			return d.BaseDriver.GetVMList()
		}
		vms = append(vms, one)
	}
	result := d.GetVMSaving()
	for _, v := range vms {
		// 以虚拟机名作为唯一键：创建路径（New-VM -Name vmUUID）与前端下发的
		// vm_uuid 都是名字，早期版本用 GUID 作键会让同一台虚拟机在内存里
		// 出现两条记录（一条名字、一条 GUID），状态也对不上。
		key := strings.TrimSpace(v.Name)
		if key == "" {
			key = v.Id
		}

		cfg, ok := result[key]
		if !ok {
			// 兼容旧数据：把 GUID 键迁移到名字键
			if old, legacy := result[v.Id]; legacy && old != nil {
				delete(result, v.Id)
				old.VmUUID = key
				result[key] = old
				cfg, ok = old, true
			}
		}

		if ok && cfg != nil {
			switch v.State {
			case "Running":
				cfg.VmFlag = model.VMPowerStarted
			case "Off":
				cfg.VmFlag = model.VMPowerStopped
			}
			continue
		}

		cfg = model.NewVMConfig()
		cfg.VmUUID = key
		cfg.OsName = v.Name
		switch v.State {
		case "Running":
			cfg.VmFlag = model.VMPowerStarted
		case "Off":
			cfg.VmFlag = model.VMPowerStopped
		}
		result[key] = cfg
	}
	return result, nil
}

// ============================================================
// QEMU/KVM 驱动（通过 virsh 命令行）
// ============================================================

// QEMUDriver QEMU/KVM 驱动
type QEMUDriver struct{ *BaseDriver }

func NewQEMUDriver(cfg *model.HSConfig) *QEMUDriver {
	return &QEMUDriver{BaseDriver: NewBaseDriver(cfg)}
}

// GetStatus 获取 QEMU/KVM 主机状态。
//
// 与 Python 版 QEMUService.HSStatus 一致：server_addr 为本地时用本机采集，
// 指向远程主机时通过 SSH 采集对端状态。
// 原实现直接走基类的本机采集，远程 QEMU 主机会把 IDCClient 自身资源当作其状态。
func (d *QEMUDriver) GetStatus() (*model.HWStatus, error) {
	return d.BaseDriver.statusForPlatform()
}

func (d *QEMUDriver) GetPower(vmUUID string) string {
	out, err := d.execHostArgs(
		[]string{"virsh", "domstate", vmUUID}, 10*time.Second)
	if err != nil {
		return "未知"
	}
	switch strings.TrimSpace(out) {
	case "running":
		return "运行中"
	case "shut off":
		return "已关机"
	case "paused":
		return "已暂停"
	}
	return "未知"
}

func (d *QEMUDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	var cmd []string
	switch action {
	case model.VMPowerSStart:
		cmd = []string{"virsh", "start", vmUUID}
	case model.VMPowerSClose:
		cmd = []string{"virsh", "shutdown", vmUUID}
	case model.VMPowerHClose:
		cmd = []string{"virsh", "destroy", vmUUID}
	case model.VMPowerSReset:
		cmd = []string{"virsh", "reboot", vmUUID}
	case model.VMPowerAPause:
		cmd = []string{"virsh", "suspend", vmUUID}
	case model.VMPowerAWaked:
		cmd = []string{"virsh", "resume", vmUUID}
	default:
		return d.BaseDriver.PowerControl(vmUUID, action)
	}
	_, err := d.execHostArgs(cmd, 30*time.Second)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: err.Error()}, nil
	}
	return d.BaseDriver.PowerControl(vmUUID, action)
}

// SupportsGracefulShutdown virsh shutdown 由 libvirt 向 guest 下发 ACPI 软关机。
func (d *QEMUDriver) SupportsGracefulShutdown() bool { return true }

// GetScreenshot 截取虚拟机屏幕。
//
// 与 Python 版 QEMUService.VMScreen 一致：virsh screenshot 输出 PPM，
// 需转换后再返回。原实现写 --file /dev/stdout 会得到二进制 PPM 与命令输出混杂的
// 无效字符串，前端根本无法显示。
func (d *QEMUDriver) GetScreenshot(vmUUID string) (string, error) {
	if d.GetPower(vmUUID) != "运行中" {
		return "", fmt.Errorf("虚拟机未运行，无法截图")
	}
	return d.captureQEMUScreenshot(vmUUID)
}

// ============================================================
// VMware Workstation 驱动（通过 REST API）
// ============================================================

// WorkstationDriver VMware Workstation 驱动
type WorkstationDriver struct{ *BaseDriver }

func NewWorkstationDriver(cfg *model.HSConfig) *WorkstationDriver {
	return &WorkstationDriver{BaseDriver: NewBaseDriver(cfg)}
}

// GetPower 查询电源状态。
// vmrest 的电源端点返回 JSON 对象（{"power_state":"poweredOn"}），
// 且路径中的 id 是 vmrest 内部 id，需先用虚拟机名反查（与 selectVid 一致）。
//
// vmrest 的状态依赖它自己维护的 VM 通道：虚拟机在 Workstation GUI 里启动/重启
// 时，它会长时间停留在 resetting 甚至直接报未知。这类情况用 vmrun list 复核，
// 否则前端会一直显示「未知」。
func (d *WorkstationDriver) GetPower(vmUUID string) string {
	vmID := vmUUID
	if id := d.selectVid(vmUUID); id != "" {
		vmID = id
	}
	return d.powerOf(vmID, vmUUID)
}

// powerOf 解析电源状态：vmrest 返回中间态/未知时用 vmrun list 复核。
// 列表同步（GetVMList）、电源操作回写也必须走这里，否则同一轮调度里
// 「GetPower 判定运行中 → Crontab 用 wsGetPower 判定重启中」会互相覆盖，
// 表现为数据库是 STARTED、驱动内存却停在 ON_WAKE。
func (d *WorkstationDriver) powerOf(vmID, vmName string) string {
	state := d.wsGetPower(vmID)
	if state == "未知" || state == "启动中" || state == "重启中" {
		if d.vmrunIsRunning(vmName) {
			return "运行中"
		}
	}
	return state
}

// ============================================================
// VirtualBox 驱动（通过 VBoxManage 命令行）
// ============================================================

// VirtualBoxDriver VirtualBox 驱动
type VirtualBoxDriver struct{ *BaseDriver }

func NewVirtualBoxDriver(cfg *model.HSConfig) *VirtualBoxDriver {
	return &VirtualBoxDriver{BaseDriver: NewBaseDriver(cfg)}
}

func (d *VirtualBoxDriver) GetPower(vmUUID string) string {
	out, err := d.execVBox("showvminfo", vmUUID, "--machinereadable")
	if err != nil {
		return "未知"
	}
	return parseVBoxVMState(out)
}

// parseVBoxVMState 从 showvminfo --machinereadable 解析电源状态。
//
// Windows 下 VBoxManage 输出为 CRLF，行尾的 \r 会让
// `VMState="running"` 解析成 `running"\r`，状态恒为「未知」，
// 因此必须先 TrimSpace 再比对。
func parseVBoxVMState(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "VMState=") {
			continue
		}
		switch strings.Trim(strings.TrimPrefix(line, "VMState="), "\"") {
		case "running":
			return "运行中"
		case "poweroff":
			return "已关机"
		case "paused", "saved":
			return "已暂停"
		}
	}
	return "未知"
}

// GetVMList 列出 VirtualBox 虚拟机。
//
// 统一以**虚拟机名**作为唯一键：创建路径用 `createvm --name <vm_uuid>` 注册虚拟机，
// 数据库与接口参数里的 vm_uuid 也是这个名字；若这里改用 `list vms` 输出的 UUID 作键，
// 同一台虚拟机会在内存里出现两条记录（一条名字、一条 UUID），
// 前端列表重复、电源状态也互相覆盖。UUID 键的历史记录就地迁移到名字键。
func (d *VirtualBoxDriver) GetVMList() (map[string]*model.VMConfig, error) {
	out, err := d.execVBox("list", "vms")
	if err != nil {
		return d.BaseDriver.GetVMList()
	}
	result := d.GetVMSaving()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// 格式: "vm_name" {vm_uuid}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}
		name := strings.Trim(parts[0], "\"")
		if name == "" {
			continue
		}
		uuid := strings.Trim(parts[1], "{}")

		cfg, ok := result[name]
		if !ok && uuid != "" {
			// 兼容旧数据：把 UUID 键迁移到名字键
			if legacy, exists := result[uuid]; exists && legacy != nil {
				delete(result, uuid)
				legacy.VmUUID = name
				result[name] = legacy
				cfg, ok = legacy, true
			}
		}
		if ok && cfg != nil {
			if cfg.OsName == "" {
				cfg.OsName = name
			}
			continue
		}

		cfg = model.NewVMConfig()
		cfg.VmUUID = name
		cfg.OsName = name
		result[name] = cfg
	}
	// 批量获取状态
	for key, cfg := range result {
		if cfg == nil {
			continue
		}
		switch d.GetPower(key) {
		case "运行中":
			cfg.VmFlag = model.VMPowerStarted
		case "已关机":
			cfg.VmFlag = model.VMPowerStopped
		case "已暂停":
			cfg.VmFlag = model.VMPowerSuspend
		}
	}
	return result, nil
}

// ============================================================
// MEmu Android 驱动（通过 memuc 命令行）
// ============================================================

// MemuDriver MEmu Android 驱动
type MemuDriver struct{ *BaseDriver }

func NewMemuDriver(cfg *model.HSConfig) *MemuDriver {
	return &MemuDriver{BaseDriver: NewBaseDriver(cfg)}
}

// 注：MEmu 的 GetPower 由 memu_lifecycle.go 实现（memuc isvmrunning），
// 与 Python 版 MemuAndroid.GetPower 一致，此处不再重复定义。
