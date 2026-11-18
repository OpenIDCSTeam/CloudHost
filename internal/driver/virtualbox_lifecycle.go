// Package driver VirtualBox 驱动生命周期实现（VBoxManage）
// 与 Python 版 HostServer/VirtualBoxs.py 的功能语义对齐。
package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// vboxBin 解析 VBoxManage 可执行文件路径。
//
// VirtualBox 的 Windows 安装包**不会**把安装目录写入 PATH（7.x 仍然如此），
// 直接 exec "VBoxManage" 会得到 executable file not found，
// 表现为主机状态恒为 0、所有虚拟机操作失败。解析顺序：
//
//	1. 主机配置 launch_path（可指向 VBoxManage 本体或其所在目录）
//	2. PATH
//	3. 环境变量 VBOX_MSI_INSTALL_PATH / VBOX_INSTALL_PATH
//	4. 各系统默认安装目录
//
// 全部失败时回退裸命令名，让错误信息里保留原始调用形式便于排查。
func (d *VirtualBoxDriver) vboxBin() string {
	if launch := strings.TrimSpace(d.Config.LaunchPath); launch != "" {
		if p := resolveToolPath(launch, "VBoxManage"); p != "" {
			return p
		}
	}
	if p, err := exec.LookPath("VBoxManage"); err == nil {
		return p
	}
	for _, env := range []string{"VBOX_MSI_INSTALL_PATH", "VBOX_INSTALL_PATH"} {
		if dir := strings.TrimSpace(os.Getenv(env)); dir != "" {
			if p := findExecutable(dir, "VBoxManage"); p != "" {
				return p
			}
		}
	}
	for _, dir := range virtualboxInstallDirs() {
		if p := findExecutable(dir, "VBoxManage"); p != "" {
			return p
		}
	}
	return "VBoxManage"
}

// resolveToolPath 把 launch_path 归一化为具体可执行文件路径。
func resolveToolPath(launch, name string) string {
	info, err := os.Stat(launch)
	if err != nil {
		return ""
	}
	dir := launch
	if !info.IsDir() {
		if strings.EqualFold(strings.TrimSuffix(filepath.Base(launch), ".exe"), name) {
			return launch
		}
		dir = filepath.Dir(launch)
	}
	return findExecutable(dir, name)
}

// virtualboxInstallDirs 各系统 VirtualBox 的默认安装目录
func virtualboxInstallDirs() []string {
	switch runtime.GOOS {
	case "windows":
		var dirs []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if root := strings.TrimSpace(os.Getenv(env)); root != "" {
				dirs = append(dirs, filepath.Join(root, "Oracle", "VirtualBox"))
			}
		}
		return append(dirs, `C:\Program Files\Oracle\VirtualBox`)
	case "darwin":
		return []string{"/Applications/VirtualBox.app/Contents/MacOS", "/usr/local/bin"}
	default:
		return []string{"/usr/bin", "/usr/local/bin", "/opt/VirtualBox"}
	}
}

// execVBox 执行 VBoxManage 命令
func (d *VirtualBoxDriver) execVBox(args ...string) (string, error) {
	full := append([]string{d.vboxBin()}, args...)
	out, _, err := security.SafeExecWithTimeout(full, 60*time.Second)
	return out, err
}

// vboxDiskDir 返回 VirtualBox 磁盘目录
func (d *VirtualBoxDriver) vboxDiskDir() string {
	if d.Config.SystemPath != "" {
		return d.Config.SystemPath
	}
	return "."
}

// GetStatus 获取 VirtualBox 主机状态。
//
// 采集顺序与 LXD / Docker 一致：先用本机采集拿到 CPU 使用率、内存占用与
// 各存储路径的磁盘容量，再用 `VBoxManage list hostinfo` 校正 CPU 核数、
// 内存总量与 CPU 型号。
//
// 原实现只解析 hostinfo，CPU 使用率、内存占用、硬盘容量与磁盘明细全部为 0，
// 前端主机卡片只能显示「0 核 / 0 MB」。
func (d *VirtualBoxDriver) GetStatus() (*model.HWStatus, error) {
	hw := localHWStatus(d.Config)
	if hw == nil {
		hw = emptyHWStatus()
	}
	hw.OnUpdate = time.Now().Unix()

	out, err := d.execVBox("list", "hostinfo")
	if err != nil {
		return hw, nil
	}

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Processor count:"):
			var cpus int
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "Processor count:")), "%d", &cpus)
			if cpus > 0 {
				hw.CpuTotal = cpus
			}
		case strings.HasPrefix(trimmed, "Processor core count:"):
			if hw.CpuTotal == 0 {
				var cores int
				fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "Processor core count:")), "%d", &cores)
				hw.CpuTotal = cores
			}
		case strings.HasPrefix(trimmed, "Processor#0 description:"):
			if hw.CpuModel == "" {
				hw.CpuModel = strings.TrimSpace(strings.TrimPrefix(trimmed, "Processor#0 description:"))
			}
		case strings.HasPrefix(trimmed, "Memory size:"):
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, "Memory size:")))
			if len(fields) > 0 {
				if mb := vboxMemMB(fields); mb > 0 {
					hw.MemTotal = mb
					if hw.MemUsage > mb {
						hw.MemUsage = mb
					}
				}
			}
		case strings.HasPrefix(trimmed, "Memory available:"):
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, "Memory available:")))
			if len(fields) > 0 {
				if avail := vboxMemMB(fields); avail > 0 && hw.MemTotal > avail {
					hw.MemUsage = hw.MemTotal - avail
				}
			}
		}
	}
	return hw, nil
}

// vboxMemMB 解析 hostinfo 的内存值（形如 ["16384", "MByte"]）。
func vboxMemMB(fields []string) int {
	var mb int
	fmt.Sscanf(fields[0], "%d", &mb)
	if mb <= 0 {
		return 0
	}
	if len(fields) > 1 && strings.EqualFold(fields[1], "GByte") {
		mb *= 1024
	}
	return mb
}

// ScanVMs 扫描并同步虚拟机列表
func (d *VirtualBoxDriver) ScanVMs() (*model.ZMessage, error) {
	vms, err := d.GetVMList()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDetect", Message: err.Error()}, nil
	}

	prefix := d.Config.FilterName
	d.mu.Lock()
	added := 0
	for name, cfg := range vms {
		// 备份克隆机是「备份介质」而不是业务虚拟机：
		// 一旦被纳管，前端列表会出现同名机器的影子条目，删除功能也会误删备份。
		if IsVBoxBackupName(name) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if _, exists := d.VmSaving[name]; !exists {
			d.VmSaving[name] = cfg
			added++
		} else {
			d.VmSaving[name].VmFlag = cfg.VmFlag
		}
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDetect",
		Message: fmt.Sprintf("扫描完成：共 %d 个，新增 %d 个", len(vms), added)}, nil
}

// PowerControl 电源控制
//
// VirtualBox 的 controlvm / startvm 对「状态不匹配」的调用会返回
// 「Machine is currently running / not running / locked by a session」
// 这类原始错误，且强制关机后短时间内会话锁仍存在。
// 这里先按当前状态短路，再对 startvm 做短暂重试，最后等待状态落地，
// 让前端刷新后立刻看到新状态而不是等下一轮调度（60 秒）。
func (d *VirtualBoxDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	state := d.GetPower(vmUUID)
	ok := func(msg string) (*model.ZMessage, error) {
		return &model.ZMessage{Success: true, Action: "VMPowers", Message: msg}, nil
	}
	fail := func(msg string) (*model.ZMessage, error) {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: msg}, nil
	}

	var args []string
	switch action {
	case model.VMPowerSStart:
		if state == "运行中" {
			return ok("虚拟机已在运行")
		}
		args = []string{"startvm", vmUUID, "--type", "headless"}
	case model.VMPowerSClose:
		if state == "已关机" {
			return ok("虚拟机已关机")
		}
		args = []string{"controlvm", vmUUID, "acpipowerbutton"}
	case model.VMPowerHClose:
		if state == "已关机" {
			return ok("虚拟机已关机")
		}
		args = []string{"controlvm", vmUUID, "poweroff"}
	case model.VMPowerSReset, model.VMPowerHReset:
		if state != "运行中" {
			return fail("虚拟机未运行，无法重启")
		}
		args = []string{"controlvm", vmUUID, "reset"}
	case model.VMPowerAPause:
		if state == "已暂停" {
			return ok("虚拟机已暂停")
		}
		if state != "运行中" {
			return fail("虚拟机未运行，无法暂停")
		}
		args = []string{"controlvm", vmUUID, "pause"}
	case model.VMPowerAWaked:
		if state == "运行中" {
			return ok("虚拟机已在运行")
		}
		if state != "已暂停" {
			return fail("虚拟机未处于暂停状态，无法恢复")
		}
		args = []string{"controlvm", vmUUID, "resume"}
	default:
		return d.BaseDriver.PowerControl(vmUUID, action)
	}

	out, err := d.execVBox(args...)
	// 开机时若上一轮关机的会话锁尚未释放，VBoxManage 会报
	// "machine is already locked by a session"，稍等重试即可
	if err != nil && action == model.VMPowerSStart &&
		strings.Contains(strings.ToLower(out), "locked by a session") {
		for i := 0; i < 5; i++ {
			time.Sleep(time.Second)
			if out, err = d.execVBox(args...); err == nil {
				break
			}
		}
	}
	if err != nil {
		return fail(fmt.Sprintf("电源操作失败: %v (%s)", err, strings.TrimSpace(out)))
	}

	// 等待状态落地并写回内存，列表页不必等下一轮调度
	if target := vboxPowerTarget(action); target != "" {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if d.GetPower(vmUUID) == target {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	if st, exists := PowerStatusMap[d.GetPower(vmUUID)]; exists {
		d.mu.Lock()
		if cfg, found := d.VmSaving[vmUUID]; found {
			cfg.VmFlag = st
		}
		d.mu.Unlock()
	}

	return ok("电源操作已执行")
}

// vboxPowerTarget 返回电源操作成功后的目标状态（中文），空串表示不校验。
func vboxPowerTarget(action model.VMPowers) string {
	switch action {
	case model.VMPowerSStart, model.VMPowerSReset, model.VMPowerHReset, model.VMPowerAWaked:
		return "运行中"
	case model.VMPowerHClose:
		return "已关机"
	case model.VMPowerAPause:
		return "已暂停"
	default:
		// 软关机依赖 guest 处理 ACPI 事件，无响应时不作为失败
		return ""
	}
}

// SupportsGracefulShutdown acpipowerbutton 向 guest 发送 ACPI 电源键事件。
func (d *VirtualBoxDriver) SupportsGracefulShutdown() bool { return true }

// CreateVM 创建虚拟机
func (d *VirtualBoxDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmName := cfg.VmUUID

	if out, err := d.execVBox("createvm", "--name", vmName,
		"--ostype", vboxOsType(cfg.OsName), "--register"); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("创建虚拟机失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	mem := cfg.MemNum
	if mem <= 0 {
		mem = 1024
	}
	cpu := cfg.CpuNum
	if cpu <= 0 {
		cpu = 1
	}
	// VRDE 端口必须逐台分配：默认 3389 被第一台虚拟机占用后，
	// 其余虚拟机的 VRDE 会绑定失败（控制台连上却是另一台的画面）。
	vrdePort := d.vboxAllocateVRDEPort(cfg, vmName)
	args := []string{"modifyvm", vmName,
		"--memory", fmt.Sprintf("%d", mem),
		"--cpus", fmt.Sprintf("%d", cpu),
		"--vrde", "on",
		"--vrdeport", fmt.Sprintf("%d", vrdePort),
	}
	// 网卡：桥接 + 固定 MAC（原先硬编码 nat，既无桥接也无 MAC）
	args = append(args, d.nicArgs(cfg)...)
	if out, err := d.execVBox(args...); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("配置虚拟机失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	// 声卡：7.x 与 6.x 参数名不同，逐套尝试（失败不影响创建）
	for _, audio := range vboxAudioCommands(vmName) {
		if _, err := d.execVBox(audio...); err == nil {
			break
		}
	}
	// USB 控制器：usbfilter 直通依赖虚拟机先启用 USB；启用 XHCI 以兼容 USB 3.x。
	// 老版本不认参数时忽略，不能因此让整台虚拟机创建失败。
	_, _ = d.execVBox(vboxUSBControllerArgs(vmName)...)

	// 创建虚拟磁盘：优先从系统模板镜像克隆，避免创建出没有系统的裸盘
	diskPath := filepath.Join(d.vboxDiskDir(), vmName+".vdi")
	sizeMB := cfg.HddNum
	if sizeMB <= 0 {
		sizeMB = 8192
	}
	if err := d.createDiskFromTemplate(cfg, diskPath, sizeMB); err != nil {
		d.vboxDiscardVM(vmName)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "创建磁盘失败: " + err.Error()}, nil
	}

	// 添加 SATA 控制器并挂载磁盘
	_, _ = d.execVBox("storagectl", vmName, "--name", "SATA",
		"--add", "sata", "--controller", "IntelAhci")
	// IDE 控制器与空光驱槽位在创建时就备好：运行中的虚拟机不能新增控制器/设备槽，
	// 否则「运行中挂载 ISO」会报 Could not find a controller named 'IDE' /
	// No drive attached to device slot。
	_, _ = d.execVBox("storagectl", vmName, "--name", "IDE", "--add", "ide")
	_, _ = d.execVBox("storageattach", vmName, "--storagectl", "IDE",
		"--port", "0", "--device", "0", "--type", "dvddrive", "--medium", "emptydrive")
	if out, err := d.execVBox("storageattach", vmName,
		"--storagectl", "SATA", "--port", "0", "--device", "0",
		"--type", "hdd", "--medium", diskPath); err != nil {
		d.vboxDiscardVM(vmName)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("挂载磁盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	// 启动：PowerControl 把失败信息放在 ZMessage 里（error 恒为 nil），
	// 只判断 error 会把「启动失败」当成创建成功。
	if msg, _ := d.PowerControl(vmName, model.VMPowerSStart); msg == nil || !msg.Success {
		detail := "启动虚拟机失败"
		if msg != nil {
			detail = msg.Message
		}
		return &model.ZMessage{Success: false, Action: "VMCreate", Message: detail}, nil
	}

	cfg.VcPort = fmt.Sprintf("%d", vrdePort)
	cfg.VmFlag = model.VMPowerStarted
	d.mu.Lock()
	d.VmSaving[vmName] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMCreate", Message: "虚拟机创建成功"}, nil
}

// vboxAudioCommands 返回可逐套尝试的声卡参数。
//
// VirtualBox 7.x 改名为 --audio-driver / --audio-controller / --audio-enabled，
// 6.x 仍是 --audio / --audiocontroller / --audioenabled，
// 只下发一套会让另一版本 modifyvm 直接报未知参数。
func vboxAudioCommands(vmName string) [][]string {
	return [][]string{
		{"modifyvm", vmName, "--audio-driver", "default", "--audio-controller", "hda",
			"--audio-enabled", "on"},
		{"modifyvm", vmName, "--audio", "default", "--audiocontroller", "hda",
			"--audioenabled", "on"},
	}
}

// vboxUSBControllerArgs 启用 USB 控制器（XHCI，向下兼容 USB 2.0 设备）。
//
// VBoxManage usbfilter 的直通规则只有在虚拟机启用 USB 控制器后才生效。
func vboxUSBControllerArgs(vmName string) []string {
	return []string{"modifyvm", vmName, "--usb", "on", "--usb-xhci", "on"}
}

// vboxOsType 依据镜像名推断 VirtualBox 的 ostype。
//
// ostype 只影响虚拟机的默认硬件（芯片组、存储控制器、固件），
// 猜错不会导致创建失败，但会让 Windows 镜像拿到 Linux 的默认配置。
func vboxOsType(osName string) string {
	lower := strings.ToLower(osName)
	switch {
	case strings.Contains(lower, "win11"):
		return "Windows11_64"
	case strings.Contains(lower, "win10"):
		return "Windows10_64"
	case strings.Contains(lower, "win2019"), strings.Contains(lower, "2022"):
		return "Windows2022_64"
	case strings.Contains(lower, "win2016"), strings.Contains(lower, "windows server"):
		return "Windows2016_64"
	case strings.Contains(lower, "win"):
		return "Windows10_64"
	default:
		return "Linux26_64"
	}
}

// vboxUsedVRDEPorts 返回已注册虚拟机占用的 VRDE 端口。
func (d *VirtualBoxDriver) vboxUsedVRDEPorts() map[int]bool {
	used := map[int]bool{}
	out, err := d.execVBox("list", "vms")
	if err != nil {
		return used
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "{") {
			continue
		}
		name := strings.Trim(strings.SplitN(line, " ", 2)[0], "\"")
		if name == "" {
			continue
		}
		if p := d.vboxVRDEPortOf(name); p > 0 {
			used[p] = true
		}
	}
	return used
}

// vboxVRDEPortOf 读取虚拟机当前配置的 VRDE 端口（0 表示未配置）。
func (d *VirtualBoxDriver) vboxVRDEPortOf(vmName string) int {
	out, err := d.execVBox("showvminfo", vmName, "--machinereadable")
	if err != nil {
		return 0
	}
	return parseVBoxVRDEPort(out)
}

// parseVBoxVRDEPort 解析 showvminfo --machinereadable 中的 VRDE 端口。
//
// 运行中的虚拟机会同时给出 vrdeport / vrdeproperty[TCP/Ports]，
// 前者可能是 -1（表示「未显式指定」），此时以 TCP/Ports 为准。
func parseVBoxVRDEPort(out string) int {
	port := 0
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "vrdeproperty[TCP/Ports]="):
			v := strings.Trim(strings.TrimPrefix(line, "vrdeproperty[TCP/Ports]="), "\"")
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p > 0 {
				port = p
			}
		case strings.HasPrefix(line, "vrdeport="):
			v := strings.Trim(strings.TrimPrefix(line, "vrdeport="), "\"")
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p > 0 && port == 0 {
				port = p
			}
		}
	}
	return port
}

// vboxAllocateVRDEPort 为新建虚拟机分配 VRDE 端口。
//
// 优先使用前端填写的 vc_port；未填写或已被其它虚拟机占用时，
// 在主机端口范围内挑选第一个空闲端口（未配置范围时退化为 5900-6999）。
func (d *VirtualBoxDriver) vboxAllocateVRDEPort(cfg *model.VMConfig, vmName string) int {
	used := d.vboxUsedVRDEPorts()

	if cfg != nil {
		if p, err := strconv.Atoi(strings.TrimSpace(cfg.VcPort)); err == nil && p > 0 && p < 65536 && !used[p] {
			return p
		}
	}

	low, high := d.Config.PortsStart, d.Config.PortsClose
	if low <= 0 || high <= low {
		low, high = 5900, 6999
	}
	// 上限保护：端口范围配置异常（如 1-60000）时不做全量扫描
	if high-low > 20000 {
		high = low + 20000
	}
	for p := low; p <= high; p++ {
		if !used[p] {
			return p
		}
	}
	// 全部占用：返回范围起点，让 VBoxManage 报出明确错误
	return low
}

// createDiskFromTemplate 基于系统模板镜像克隆出虚拟机系统盘。
//
// 对应 Python VirtualBoxs.VMCreate：
//
//	VBoxManage clonemedium <模板> <目标.vdi> --format VDI
//
// 模板缺失或克隆失败时必须**报错**，不能回退成 createmedium：
// 原实现静默建空盘，创建任务仍然「成功」，用户在 VirtualBox 里看到的是
// 一块 2MB 的空盘（虚拟容量 80G、实占 2MB），完全不是所选的系统镜像。
// 只有显式未选择模板（os_name 为空）时才建空盘。
func (d *VirtualBoxDriver) createDiskFromTemplate(cfg *model.VMConfig, diskPath string, sizeMB int) error {
	osName := ""
	if cfg != nil {
		osName = strings.TrimSpace(cfg.OsName)
	}
	if osName == "" {
		return d.createEmptyDisk(diskPath, sizeMB)
	}

	imagesPath := strings.TrimSpace(d.Config.ImagesPath)
	if imagesPath == "" {
		return fmt.Errorf("主机未配置系统模板目录（images_path），无法使用模板 %s", osName)
	}
	imFile := filepath.Join(imagesPath, osName)
	if _, statErr := os.Stat(imFile); statErr != nil {
		return fmt.Errorf("系统模板不存在: %s（可能已被删除，请在「镜像管理」中重新绑定或上传）", imFile)
	}

	out, err := d.execVBox("clonemedium", imFile, diskPath, "--format", "VDI")
	if err == nil {
		// 克隆出来的是模板的虚拟容量（如 10G），而套餐里可能申请了更大磁盘。
		// VirtualBox 只支持扩容，这里按需拉大，保证界面上的容量与实际一致；
		// 模板比申请值更大时无需处理（不能缩小）。
		if sizeMB > 0 {
			_, _ = d.execVBox("modifymedium", "disk", diskPath, "--resize", fmt.Sprintf("%d", sizeMB))
		}
		return nil
	}
	// 目标已存在：多半是上一次创建失败留下的残盘，删掉重克隆；
	// 把残盘当成功返回会让虚拟机拿到一块空盘。
	if strings.Contains(strings.ToLower(out), "already exists") {
		if rmErr := d.removeMediumFile(diskPath); rmErr == nil {
			if out2, err2 := d.execVBox("clonemedium", imFile, diskPath, "--format", "VDI"); err2 == nil {
				return nil
			} else {
				return fmt.Errorf("从模板克隆磁盘失败: %v (%s)", err2, strings.TrimSpace(out2))
			}
		}
	}
	return fmt.Errorf("从模板克隆磁盘失败: %v (%s)", err, strings.TrimSpace(out))
}

// createEmptyDisk 创建动态分配的空 VDI（仅在未选择系统模板时使用）。
func (d *VirtualBoxDriver) createEmptyDisk(diskPath string, sizeMB int) error {
	if sizeMB <= 0 {
		sizeMB = 8192
	}
	if out, err := d.execVBox("createmedium", "disk",
		"--filename", diskPath,
		"--size", fmt.Sprintf("%d", sizeMB),
		"--format", "VDI"); err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// removeMediumFile 注销并删除一个磁盘介质文件（失败时直接删文件）。
func (d *VirtualBoxDriver) removeMediumFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("路径为空")
	}
	_, _ = d.execVBox("closemedium", "disk", path, "--delete")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// vboxDiscardVM 回滚创建到一半的虚拟机（含其磁盘）。
//
// 单次 unregistervm 可能因会话锁/介质句柄未释放而失败，
// 失败后虚拟机会以「幽灵」形式留在 VirtualBox 里被扫描纳管，
// 因此重试几次，并带上 --delete 一并回收磁盘。
func (d *VirtualBoxDriver) vboxDiscardVM(vmName string) {
	if strings.TrimSpace(vmName) == "" {
		return
	}
	for i := 0; i < 3; i++ {
		if out, err := d.execVBox("unregistervm", vmName, "--delete"); err == nil {
			_ = out
			return
		}
		// 已经不存在（被别处删除）也算完成
		if d.GetPower(vmName) == "未知" {
			return
		}
		time.Sleep(time.Second)
	}
}

// nicArgs 生成 VirtualBox 网卡参数（桥接 + 固定 MAC）。
//
// 对应 Python VirtualBoxs.VMCreate：
//
//	--nicN bridged --bridgeadapterN <网桥> --macaddressN <MAC>
//
// MAC 必须固定：路由器上的 DHCP 静态绑定与 ARP 绑定都以 MAC 为键，
// 若由 VirtualBox 随机生成，绑定记录与实际网卡对不上，虚拟机拿不到固定 IP。
func (d *VirtualBoxDriver) nicArgs(cfg *model.VMConfig) []string {
	if cfg == nil || len(cfg.NicAll) == 0 {
		// 未配置网卡时回退 NAT，保证虚拟机仍可联网
		return []string{"--nic1", "nat"}
	}

	bridge := strings.TrimSpace(d.Config.NetworkNat)
	if bridge == "" {
		bridge = strings.TrimSpace(d.Config.NetworkPub)
	}
	if bridge == "" {
		bridge = "eth0"
	}
	pubBridge := strings.TrimSpace(d.Config.NetworkPub)
	if pubBridge == "" {
		pubBridge = bridge
	}

	names := make([]string, 0, len(cfg.NicAll))
	for name := range cfg.NicAll {
		names = append(names, name)
	}
	sort.Strings(names)

	args := make([]string, 0, len(names)*4)
	idx := 1
	for _, name := range names {
		if idx > 4 { // VirtualBox 仅支持 4 张网卡
			break
		}
		nic := cfg.NicAll[name]
		adapter := bridge
		if nic.EffectiveNicType() == "pub" {
			adapter = pubBridge
		}
		args = append(args,
			fmt.Sprintf("--nic%d", idx), "bridged",
			fmt.Sprintf("--bridgeadapter%d", idx), adapter)

		if mac := strings.ReplaceAll(strings.TrimSpace(nic.MacAddr), ":", ""); mac != "" {
			args = append(args, fmt.Sprintf("--macaddress%d", idx), strings.ToUpper(mac))
		}
		idx++
	}
	// 移除的网卡必须显式关闭：只下发存在的网卡时，
	// 之前挂在 nic2/nic3 上的配置会继续留在虚拟机里。
	for ; idx <= 4; idx++ {
		args = append(args, fmt.Sprintf("--nic%d", idx), "none")
	}
	return args
}

// nicSignature 生成网卡配置指纹，用于判断网卡是否真的发生变化。
func nicSignature(cfg *model.VMConfig) string {
	if cfg == nil || len(cfg.NicAll) == 0 {
		return ""
	}
	names := make([]string, 0, len(cfg.NicAll))
	for name := range cfg.NicAll {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		nic := cfg.NicAll[name]
		b.WriteString(name)
		b.WriteByte('|')
		b.WriteString(nic.EffectiveNicType())
		b.WriteByte('|')
		b.WriteString(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(nic.MacAddr), ":", "")))
		b.WriteByte(';')
	}
	return b.String()
}

// vboxSystemDisk 返回虚拟机 SATA 0:0 上的系统盘路径（未挂载时返回空串）。
func (d *VirtualBoxDriver) vboxSystemDisk(vmName string) string {
	out, err := d.execVBox("showvminfo", vmName, "--machinereadable")
	if err != nil {
		return ""
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, `"SATA-0-0"=`) {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, `"SATA-0-0"=`), `"`)
	}
	return ""
}

// vboxWaitPowerOff 等待虚拟机关机完成（强制断电后 unregister 会失败）。
func (d *VirtualBoxDriver) vboxWaitPowerOff(vmName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		state := d.GetPower(vmName)
		if state == "已关机" || state == "未知" {
			// 「未知」通常意味着虚拟机已不存在，交由后续步骤判断
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// DeleteVM 删除虚拟机
func (d *VirtualBoxDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	// 先强制关机：poweroff 是异步的，不等它落地就 unregister 会报
	// 「Machine is currently running」，删除流程会一直卡在这一步。
	if d.GetPower(vmUUID) != "已关机" {
		_, _ = d.PowerControl(vmUUID, model.VMPowerHClose)
		if !d.vboxWaitPowerOff(vmUUID, 30*time.Second) {
			return &model.ZMessage{Success: false, Action: "VMDelete",
				Message: "虚拟机仍在运行，无法删除，请稍后重试"}, nil
		}
	}

	args := []string{"unregistervm", vmUUID}
	if removeBackup {
		args = append(args, "--delete")
	}
	if out, err := d.execVBox(args...); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: fmt.Sprintf("删除失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDelete", Message: "虚拟机删除成功"}, nil
}

// UpdateVM 更新虚拟机配置（CPU / 内存 / 网卡 / 磁盘扩容）。
//
// VirtualBox 的 CPU、内存、网卡与磁盘调整都要求虚拟机处于关机状态；
// 运行中直接下发会得到 "Machine is currently running" 的模糊错误，
// 这里先判断电源状态并给出可执行的提示。
func (d *VirtualBoxDriver) UpdateVM(cfg *model.VMConfig, oldCfg *model.VMConfig) (*model.ZMessage, error) {
	if cfg == nil {
		return &model.ZMessage{Success: false, Action: "VMUpdate", Message: "配置不能为空"}, nil
	}
	if oldCfg == nil {
		oldCfg = &model.VMConfig{}
	}

	cpuChanged := cfg.CpuNum != oldCfg.CpuNum && cfg.CpuNum > 0
	memChanged := cfg.MemNum != oldCfg.MemNum && cfg.MemNum > 0
	nicChanged := nicSignature(cfg) != nicSignature(oldCfg)
	diskChanged := cfg.HddNum > 0 && oldCfg.HddNum > 0 && cfg.HddNum != oldCfg.HddNum

	if !cpuChanged && !memChanged && !nicChanged && !diskChanged {
		d.mu.Lock()
		d.VmSaving[cfg.VmUUID] = cfg
		d.mu.Unlock()
		return &model.ZMessage{Success: true, Action: "VMUpdate", Message: "配置无变化"}, nil
	}

	if d.GetPower(cfg.VmUUID) == "运行中" {
		return &model.ZMessage{Success: false, Action: "VMUpdate",
			Message: "VirtualBox 修改 CPU / 内存 / 网卡 / 磁盘前需先关机，请关机后重试"}, nil
	}

	args := []string{"modifyvm", cfg.VmUUID}
	if cpuChanged {
		args = append(args, "--cpus", fmt.Sprintf("%d", cfg.CpuNum))
	}
	if memChanged {
		args = append(args, "--memory", fmt.Sprintf("%d", cfg.MemNum))
	}
	if nicChanged {
		args = append(args, d.nicArgs(cfg)...)
	}
	if len(args) > 2 {
		if out, err := d.execVBox(args...); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("更新失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	// 磁盘扩容：VirtualBox 只能扩大不能缩小
	if diskChanged {
		if cfg.HddNum < oldCfg.HddNum {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("VirtualBox 不支持缩小磁盘（当前 %dMB，目标 %dMB）", oldCfg.HddNum, cfg.HddNum)}, nil
		}
		disk := d.vboxSystemDisk(cfg.VmUUID)
		if disk == "" {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: "未找到系统盘，无法扩容"}, nil
		}
		if out, err := d.execVBox("modifymedium", "disk", disk,
			"--resize", fmt.Sprintf("%d", cfg.HddNum)); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("磁盘扩容失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMUpdate", Message: "虚拟机配置更新成功"}, nil
}

// ReinstallVM 重装系统（移除旧盘、按模板克隆新盘后启动）
func (d *VirtualBoxDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmName := cfg.VmUUID

	// 1) 强制关机并等待落地（挂载/删除介质都要求虚拟机已停止）
	if d.GetPower(vmName) != "已关机" {
		_, _ = d.PowerControl(vmName, model.VMPowerHClose)
		if !d.vboxWaitPowerOff(vmName, 30*time.Second) {
			return &model.ZMessage{Success: false, Action: "VMReinstall",
				Message: "虚拟机仍在运行，无法重装，请稍后重试"}, nil
		}
	}

	// 2) 卸载并删除旧系统盘
	diskPath := filepath.Join(d.vboxDiskDir(), vmName+".vdi")
	if current := d.vboxSystemDisk(vmName); current != "" {
		diskPath = current
	}
	_, _ = d.execVBox("storageattach", vmName, "--storagectl", "SATA",
		"--port", "0", "--device", "0", "--type", "hdd", "--medium", "none")
	if out, err := d.execVBox("closemedium", "disk", diskPath, "--delete"); err != nil {
		// 介质可能已不存在：只要文件也没了就可以继续
		if _, statErr := os.Stat(diskPath); statErr == nil {
			return &model.ZMessage{Success: false, Action: "VMReinstall",
				Message: fmt.Sprintf("删除旧系统盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	// 3) 重新生成系统盘：与创建一致，优先从模板克隆，模板缺失才建空盘
	sizeMB := cfg.HddNum
	if sizeMB <= 0 {
		sizeMB = 8192
	}
	if err := d.createDiskFromTemplate(cfg, diskPath, sizeMB); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: "创建磁盘失败: " + err.Error()}, nil
	}
	if out, err := d.execVBox("storageattach", vmName, "--storagectl", "SATA",
		"--port", "0", "--device", "0", "--type", "hdd", "--medium", diskPath); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: fmt.Sprintf("挂载新系统盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	// 4) 启动（失败信息在 ZMessage 里，error 恒为 nil）
	if msg, _ := d.PowerControl(vmName, model.VMPowerSStart); msg == nil || !msg.Success {
		detail := "启动失败"
		if msg != nil {
			detail = msg.Message
		}
		return &model.ZMessage{Success: false, Action: "VMReinstall", Message: detail}, nil
	}

	d.mu.Lock()
	if existing, ok := d.VmSaving[vmName]; ok {
		existing.VmFlag = model.VMPowerStarted
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMReinstall", Message: "系统重装成功"}, nil
}

// vboxHasStorageController 判断虚拟机是否已挂载指定名称的存储控制器。
func (d *VirtualBoxDriver) vboxHasStorageController(vmName, name string) bool {
	out, err := d.execVBox("showvminfo", vmName, "--machinereadable")
	if err != nil {
		return false
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "storagecontrollername") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		if strings.EqualFold(strings.Trim(line[idx+1:], "\""), name) {
			return true
		}
	}
	return false
}

// vboxHasDVDSlot 判断 IDE 0:0 是否已存在光驱设备槽位（空盘或已挂 ISO 都算）。
func (d *VirtualBoxDriver) vboxHasDVDSlot(vmName string) bool {
	out, err := d.execVBox("showvminfo", vmName, "--machinereadable")
	if err != nil {
		return false
	}
	for _, raw := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(raw), `"IDE-0-0"=`) {
			return true
		}
	}
	return false
}

// vboxISOPath 解析 ISO 的本地绝对路径。
//
// 前端传来的 iso_path 可能只是文件名（model.IMConfig 会在缺少 iso_path 时
// 用 iso_file 兜底），直接交给 VBoxManage 会被当成相对路径，
// 报「Could not find file for the medium 'xxx.iso'」。
// 这里按「绝对路径 → ISO 目录 → 镜像目录」依次解析。
func (d *VirtualBoxDriver) vboxISOPath(iso *model.IMConfig) (string, error) {
	if iso == nil {
		return "", fmt.Errorf("未指定 ISO 文件")
	}
	raw := strings.TrimSpace(iso.IsoPath)
	if raw == "" {
		raw = strings.TrimSpace(iso.IsoFile)
	}
	if raw == "" {
		return "", fmt.Errorf("未指定 ISO 文件")
	}
	if filepath.IsAbs(raw) {
		if _, err := os.Stat(raw); err == nil {
			return raw, nil
		}
		return "", fmt.Errorf("ISO 文件不存在: %s", raw)
	}

	dirs := make([]string, 0, 2)
	for _, dir := range []string{d.Config.DvdromPath, d.Config.ImagesPath} {
		if v := strings.TrimSpace(dir); v != "" {
			dirs = append(dirs, v)
		}
	}
	for _, dir := range dirs {
		candidate := filepath.Join(dir, raw)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("主机未配置 ISO 目录，无法定位 %s", raw)
	}
	return "", fmt.Errorf("ISO 文件不存在: %s（已查找 %s）", raw, strings.Join(dirs, "、"))
}

// MountISO 挂载 / 卸载 ISO
func (d *VirtualBoxDriver) MountISO(vmUUID string, iso *model.IMConfig, mount bool) (*model.ZMessage, error) {
	// 确保 IDE 控制器存在（旧版本创建的虚拟机没有 IDE 控制器）
	if !d.vboxHasStorageController(vmUUID, "IDE") {
		if mount && d.GetPower(vmUUID) == "运行中" {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: "虚拟机缺少 IDE 控制器且正在运行，请关机后再挂载 ISO"}, nil
		}
		if out, err := d.execVBox("storagectl", vmUUID, "--name", "IDE", "--add", "ide"); err != nil {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: fmt.Sprintf("创建 IDE 控制器失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}
	// IDE 0:0 必须已有光驱槽位才能热插拔（运行中的虚拟机不能新增槽位）
	if mount && !d.vboxHasDVDSlot(vmUUID) {
		if d.GetPower(vmUUID) == "运行中" {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: "虚拟机没有预置光驱槽位且正在运行，请关机后再挂载 ISO"}, nil
		}
		_, _ = d.execVBox("storageattach", vmUUID, "--storagectl", "IDE",
			"--port", "0", "--device", "0", "--type", "dvddrive", "--medium", "emptydrive")
	}

	if mount {
		path, pathErr := d.vboxISOPath(iso)
		if pathErr != nil {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: pathErr.Error()}, nil
		}
		if out, err := d.execVBox("storageattach", vmUUID, "--storagectl", "IDE",
			"--port", "0", "--device", "0", "--type", "dvddrive", "--medium", path); err != nil {
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

	_, _ = d.execVBox("storageattach", vmUUID, "--storagectl", "IDE",
		"--port", "0", "--device", "0", "--type", "dvddrive", "--medium", "emptydrive")
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		delete(cfg.IsoAll, iso.IsoName)
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "ISOMount", Message: "ISO卸载成功"}, nil
}

// vboxHasVRDE 判断本机 VirtualBox 是否具备 VRDE（RDP）远程桌面能力。
//
// 从 6.1 起 Oracle VRDE 随 Extension Pack 分发，未安装扩展包时
// 即使 --vrde on 也不会监听端口，控制台永远连不上。
func (d *VirtualBoxDriver) vboxHasVRDE() bool {
	out, err := d.execVBox("list", "extpacks")
	if err != nil {
		return false
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "Extension Packs:") {
			n := strings.TrimSpace(strings.TrimPrefix(line, "Extension Packs:"))
			count, convErr := strconv.Atoi(n)
			return convErr == nil && count > 0
		}
	}
	return false
}

// GetVNCInfo 获取远程控制台信息。
//
// VirtualBox 没有 VNC 服务端，唯一的图形控制台是 VRDE（RDP 协议），
// 且依赖 Extension Pack。因此这里如实返回 type=rdp 与真实监听端口，
// 由接口层提示「请用 RDP 客户端连接」而不是拼一个连不上的 noVNC 地址
// （原实现恒返回 3389 + type=rdp，前端却按裸 VNC 桥接，必然黑屏）。
func (d *VirtualBoxDriver) GetVNCInfo(vmUUID string, ipAddr string) (*model.ZMessage, error) {
	if d.GetPower(vmUUID) == "未知" {
		return &model.ZMessage{Success: false, Action: "VCRemote",
			Message: "虚拟机不存在或状态未知"}, nil
	}
	if !d.vboxHasVRDE() {
		return &model.ZMessage{Success: true, Action: "VCRemote",
			Message: "VirtualBox 未安装 Extension Pack，VRDE 远程桌面不可用",
			Results: map[string]interface{}{
				"type":   "none",
				"reason": "VirtualBox 未安装 Extension Pack，无法启用 VRDE 远程桌面；可在虚拟机详情页使用「截图」查看画面",
			}}, nil
	}

	host := strings.TrimSpace(d.Config.VncIP)
	if host == "" {
		host = strings.TrimSpace(d.Config.ServerAddr)
	}
	if host == "" || host == "0.0.0.0" {
		host = strings.TrimSpace(ipAddr)
	}
	if host == "" {
		host = "127.0.0.1"
	}

	// VRDE 端口：优先使用已配置的端口，未配置时按主机端口范围分配
	port := d.vboxVRDEPortOf(vmUUID)
	running := d.GetPower(vmUUID) == "运行中"
	if port <= 0 {
		port = d.vboxAllocateVRDEPort(d.GetVMSaving()[vmUUID], vmUUID)
		if running {
			_, _ = d.execVBox("controlvm", vmUUID, "vrdeproperty",
				fmt.Sprintf("TCP/Ports=%d", port))
			_, _ = d.execVBox("controlvm", vmUUID, "vrde", "on")
		} else {
			_, _ = d.execVBox("modifyvm", vmUUID, "--vrde", "on",
				"--vrdeport", fmt.Sprintf("%d", port))
		}
		if actual := d.vboxVRDEPortOf(vmUUID); actual > 0 {
			port = actual
		}
	}

	return &model.ZMessage{Success: true, Action: "VCRemote", Message: "远程桌面信息获取成功",
		Results: map[string]interface{}{
			"type":     "rdp",
			"protocol": "rdp",
			"host":     host,
			"port":     port,
			"password": "",
		}}, nil
}

// MountHDD 挂载 / 卸载数据盘
func (d *VirtualBoxDriver) MountHDD(vmUUID string, hdd *model.SDConfig, mount bool) (*model.ZMessage, error) {
	if !mount {
		_, _ = d.execVBox("storageattach", vmUUID, "--storagectl", "SATA",
			"--port", "1", "--device", "0", "--type", "hdd", "--medium", "none")
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

	path := filepath.Join(d.vboxDiskDir(), hdd.HddName+".vdi")
	sizeMB := hdd.HddSize
	if sizeMB <= 0 {
		sizeMB = 10240
	}
	if out, err := d.execVBox("createmedium", "disk",
		"--filename", path, "--size", fmt.Sprintf("%d", sizeMB), "--format", "VDI"); err != nil {
		return &model.ZMessage{Success: false, Action: "HDDMount",
			Message: fmt.Sprintf("创建数据盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	if out, err := d.execVBox("storageattach", vmUUID, "--storagectl", "SATA",
		"--port", "1", "--device", "0", "--type", "hdd", "--medium", path); err != nil {
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

// SetupUSB USB直通（VBoxManage usbfilter）
// 与 Python 版 VirtualBoxs.USBSetup 一致
func (d *VirtualBoxDriver) SetupUSB(vmUUID string, info *model.USBInfos, usbKey string, add bool) (*model.ZMessage, error) {
	var out string
	var err error
	if add {
		name := info.UsbHint
		if name == "" {
			name = usbKey
		}
		out, err = d.execVBox("usbfilter", "add", "0",
			"--target", vmUUID,
			"--name", name,
			"--vendorid", info.VidUUID,
			"--productid", info.PidUUID)
	} else {
		out, err = d.execVBox("usbfilter", "remove", "0", "--target", vmUUID)
	}
	if err != nil {
		return &model.ZMessage{Success: false, Action: "USBSetup",
			Message: fmt.Sprintf("USB操作失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		if cfg.UsbAll == nil {
			cfg.UsbAll = make(map[string]model.USBInfos)
		}
		if add {
			cfg.UsbAll[usbKey] = *info
		} else {
			delete(cfg.UsbAll, usbKey)
		}
	}
	d.mu.Unlock()

	text := "添加"
	if !add {
		text = "移除"
	}
	return &model.ZMessage{Success: true, Action: "USBSetup", Message: "USB 设备" + text + "成功"}, nil
}

// SetPassword 通过 guestcontrol 修改虚拟机密码。
//
// 依赖 Guest Additions：未安装或未运行时 guestcontrol 会报
// 「The guest execution service is not ready」，这里补上可执行的提示，
// 避免前端只看到一段 VBoxManage 原始错误。
func (d *VirtualBoxDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	user := "root"
	oldPass := ""
	d.mu.RLock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		if cfg.OsUser != "" {
			user = cfg.OsUser
		}
		oldPass = cfg.OsPass
	}
	d.mu.RUnlock()

	if d.GetPower(vmUUID) != "运行中" {
		return &model.ZMessage{Success: false, Action: "VMPasswd",
			Message: "虚拟机未运行，无法修改密码"}, nil
	}

	out, err := d.execVBox("guestcontrol", vmUUID, "run",
		"--exe", "/bin/bash",
		"--username", user,
		"--password", oldPass,
		"--wait-stdout", "--wait-stderr",
		"--", "bash", "-c",
		fmt.Sprintf("echo '%s:%s' | chpasswd", user, password))
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPasswd",
			Message: fmt.Sprintf("密码修改失败（需已安装 Guest Additions 且旧密码正确）: %v (%s)",
				err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.OsPass = password
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "VMPasswd", Message: "密码修改成功"}, nil
}

// Crontab 定时任务：同步电源状态
func (d *VirtualBoxDriver) Crontab() bool {
	d.mu.RLock()
	names := make([]string, 0, len(d.VmSaving))
	for name := range d.VmSaving {
		names = append(names, name)
	}
	d.mu.RUnlock()

	for _, name := range names {
		state := d.GetPower(name)
		d.mu.Lock()
		if cfg, ok := d.VmSaving[name]; ok {
			if s, ok2 := PowerStatusMap[state]; ok2 {
				cfg.VmFlag = s
			}
		}
		d.mu.Unlock()
	}
	return true
}
