// Package driver QEMU/KVM 驱动生命周期实现（virsh / qemu-img）
// 与 Python 版 HostServer/QEMUService.py 的功能语义对齐。
package driver

import (
	"fmt"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// execVirsh 执行 virsh 命令
func (d *QEMUDriver) execVirsh(args ...string) (string, error) {
	full := append([]string{"virsh"}, args...)
	out, err := d.execHostArgs(full, 30*time.Second)
	return out, err
}

// diskDir 返回磁盘存储目录
func (d *QEMUDriver) diskDir() string {
	if d.Config.SystemPath != "" {
		return d.Config.SystemPath
	}
	return "/var/lib/libvirt/images"
}

// diskPath 返回虚拟机的系统盘路径
func (d *QEMUDriver) diskPath(vmUUID string) string {
	return pathpkg.Join(d.diskDir(), vmUUID+".qcow2")
}

// GetVMList 获取虚拟机列表（以 virsh 为准，同步电源状态）
func (d *QEMUDriver) GetVMList() (map[string]*model.VMConfig, error) {
	// 优先使用内存中已加载的配置，避免扫描时丢失所有者等业务字段
	result := d.GetVMSaving()

	out, err := d.execHostArgs(
		[]string{"virsh", "list", "--all", "--name"}, 15*time.Second)
	if err != nil {
		return result, nil
	}
	for _, name := range splitLines(out) {
		if _, exists := result[name]; !exists {
			cfg := model.NewVMConfig()
			cfg.VmUUID = name
			result[name] = cfg
		}
	}
	// 同步电源状态
	for name, cfg := range result {
		switch d.GetPower(name) {
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

// ScanVMs 扫描并同步虚拟机列表
func (d *QEMUDriver) ScanVMs() (*model.ZMessage, error) {
	vms, err := d.GetVMList()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDetect", Message: err.Error()}, nil
	}

	prefix := d.Config.FilterName
	d.mu.Lock()
	added := 0
	for name, cfg := range vms {
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

	return &model.ZMessage{
		Success: true, Action: "VMDetect",
		Message: fmt.Sprintf("扫描完成：共 %d 个，新增 %d 个", len(vms), added),
	}, nil
}

// CreateVM 创建虚拟机（qemu-img 建盘 + virsh define + 启动）
func (d *QEMUDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmName := cfg.VmUUID
	disk := d.diskPath(vmName)

	sizeGB := cfg.HddNum / 1024
	if sizeGB <= 0 {
		sizeGB = 10
	}

	if !d.hostFileExists(disk) {
		if err := d.createDiskFromTemplate(cfg, disk, sizeGB); err != nil {
			return &model.ZMessage{Success: false, Action: "VMCreate",
				Message: "创建磁盘失败: " + err.Error()}, nil
		}
	}

	// 生成 cloud-init NoCloud seed（设置 root 密码/主机名/网络），
	// 否则 Debian/Ubuntu 云镜像不会配置网卡，也不会启用密码登录。
	seedISO := d.writeCloudInitSeed(cfg)

	// 生成并写入 domain XML
	xml := d.buildDomainXML(cfg, disk, seedISO)
	xmlPath := "/tmp/hostagent-" + vmName + ".xml"
	if err := d.writeHostFile(xmlPath, xml, 0644); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "写入XML失败: " + err.Error()}, nil
	}
	defer func() { _, _ = d.execHostArgs([]string{"rm", "-f", xmlPath}, 10*time.Second) }()

	if out, err := d.execVirsh("define", xmlPath); err != nil {
		// 声卡依赖宿主机音频后端：没有可用后端时 libvirt/QEMU 会拒绝带 <sound> 的域。
		// 去掉声卡重试一次，保证虚拟机仍能建出来（宁可无声卡，也不能创建失败）。
		detail := strings.ToLower(out + " " + err.Error())
		if strings.Contains(detail, "audio") || strings.Contains(detail, "sound") {
			if fallback := stripSoundCardXML(xml); fallback != xml {
				if werr := d.writeHostFile(xmlPath, fallback, 0644); werr == nil {
					out, err = d.execVirsh("define", xmlPath)
				}
			}
		}
		if err != nil {
			return &model.ZMessage{Success: false, Action: "VMCreate",
				Message: fmt.Sprintf("定义虚拟机失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	if out, err := d.execVirsh("start", vmName); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("启动虚拟机失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	cfg.VmFlag = model.VMPowerStarted
	d.mu.Lock()
	d.VmSaving[vmName] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMCreate", Message: "虚拟机创建成功"}, nil
}

// createDiskFromTemplate 基于系统模板镜像创建差量磁盘。
//
// 对应 Python QEMUService.VMCreate：
//
//	qemu-img create -f qcow2 -b <模板> -F qcow2 <目标盘>
//
// 直接用 qemu-img create 建空盘会得到一个没有系统的裸盘，虚拟机启动即失败。
// 仅在模板缺失时回退为空盘（保证流程仍可继续）。
func (d *QEMUDriver) createDiskFromTemplate(cfg *model.VMConfig, disk string, sizeGB int) error {
	imagesPath := strings.TrimSpace(d.Config.ImagesPath)
	if cfg != nil && strings.TrimSpace(cfg.OsName) != "" && imagesPath != "" {
		imFile := pathpkg.Join(imagesPath, cfg.OsName)
		if d.hostFileExists(imFile) {
			// 首选：以模板为后端创建差量盘
			if _, err := d.execHostArgs(
				[]string{"qemu-img", "create", "-f", "qcow2", "-b", imFile, "-F", "qcow2", disk},
				5*time.Minute); err == nil {
				return nil
			}
			// 回退：完整转换一份独立副本
			if _, err := d.execHostArgs(
				[]string{"qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", imFile, disk},
				10*time.Minute); err == nil {
				return nil
			}
		}
	}

	// 无可用模板：创建空盘
	_, err := d.execHostArgs(
		[]string{"qemu-img", "create", "-f", "qcow2", disk, fmt.Sprintf("%dG", sizeGB)},
		2*time.Minute)
	return err
}

// buildDomainXML 生成 libvirt domain XML
func (d *QEMUDriver) buildDomainXML(cfg *model.VMConfig, disk string, seedISO string) string {
	mem := cfg.MemNum
	if mem <= 0 {
		mem = 1024
	}
	cpu := cfg.CpuNum
	if cpu <= 0 {
		cpu = 1
	}

	// 逐张网卡生成 interface 片段
	//
	// 必须写入 mac address：路由器上的 DHCP 静态绑定与 ARP 绑定都以 MAC 为键，
	// 若不定制 MAC，libvirt 会随机生成，与绑定记录不一致，虚拟机拿不到固定 IP。
	bridge := d.Config.NetworkNat
	if bridge == "" {
		bridge = d.Config.NetworkPub
	}
	if bridge == "" {
		bridge = "virbr0"
	}

	var netXML strings.Builder
	nicNames := make([]string, 0, len(cfg.NicAll))
	for name := range cfg.NicAll {
		nicNames = append(nicNames, name)
	}
	sort.Strings(nicNames)

	if len(nicNames) == 0 {
		netXML.WriteString(fmt.Sprintf(
			"    <interface type='bridge'>\n      <source bridge='%s'/>\n      <model type='virtio'/>\n    </interface>\n", bridge))
	}
	for _, name := range nicNames {
		nic := cfg.NicAll[name]
		macXML := ""
		if strings.TrimSpace(nic.MacAddr) != "" {
			macXML = fmt.Sprintf("      <mac address='%s'/>\n", nic.MacAddr)
		}
		netXML.WriteString(fmt.Sprintf(
			"    <interface type='bridge'>\n%s      <source bridge='%s'/>\n      <model type='virtio'/>\n    </interface>\n",
			macXML, bridge))
	}

	return fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='MiB'>%d</memory>
  <vcpu>%d</vcpu>
  <os>
    <type arch='x86_64'>hvm</type>
  </os>
  <features>
    <acpi/>
  </features>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>%s
    <controller type='ide' index='0'/>
    <controller type='usb' index='0' model='qemu-xhci'/>
    <sound model='ich9'/>
    <disk type='file' device='cdrom'>
      <target dev='hdc' bus='ide'/>
      <readonly/>
    </disk>
%s    <serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    <channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'/>
  </devices>
</domain>`, cfg.VmUUID, mem, cpu, disk, seedDiskXML(seedISO), netXML.String())
}

// stripSoundCardXML 去掉 <sound .../> 设备行。
//
// 宿主机没有可用音频后端时，libvirt 会拒绝定义带声卡的域；
// 调用方据此在失败后去掉声卡重试，避免整台虚拟机创建失败。
func stripSoundCardXML(xml string) string {
	lines := strings.Split(xml, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "<sound ") {
			continue
		}
		out = append(out, line)
	}
	if len(out) == len(lines) {
		return xml
	}
	return strings.Join(out, "\n")
}

// DeleteVM 删除虚拟机（强制关机 + undefine + 删除磁盘）
func (d *QEMUDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	_, _ = d.execVirsh("destroy", vmUUID)

	// seed iso 不属于 libvirt 存储卷，--remove-all-storage 不会清理
	_, _ = d.execHostArgs([]string{"rm", "-f", d.seedPath(vmUUID)}, 10*time.Second)
	// 有内部快照的域必须先删快照元数据，否则 undefine 会直接失败
	args := []string{"undefine", vmUUID, "--snapshots-metadata", "--nvram"}
	if removeBackup {
		args = append(args, "--remove-all-storage")
	}
	if out, err := d.execVirsh(args...); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: fmt.Sprintf("删除失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	// undefine --remove-all-storage 对「差量盘 + 外部 seed」并不总是生效，
	// 这里显式清理系统盘，避免残留占用磁盘空间。
	_, _ = d.execHostArgs([]string{"rm", "-f", d.diskPath(vmUUID)}, 15*time.Second)

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDelete", Message: "虚拟机删除成功"}, nil
}

// UpdateVM 更新虚拟机配置（需关机；仅调整 CPU / 内存）
func (d *QEMUDriver) UpdateVM(cfg *model.VMConfig, oldCfg *model.VMConfig) (*model.ZMessage, error) {
	if d.GetPower(cfg.VmUUID) == "运行中" {
		return &model.ZMessage{Success: false, Action: "VMUpdate",
			Message: "虚拟机必须关机后才能修改配置"}, nil
	}

	if cfg.CpuNum != oldCfg.CpuNum && cfg.CpuNum > 0 {
		if out, err := d.execVirsh("setvcpus", cfg.VmUUID,
			strconv.Itoa(cfg.CpuNum), "--config"); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("修改CPU失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	if cfg.MemNum != oldCfg.MemNum && cfg.MemNum > 0 {
		kib := strconv.Itoa(cfg.MemNum * 1024)
		if out, err := d.execVirsh("setmaxmem", cfg.VmUUID, kib, "--config"); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("修改内存上限失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
		if out, err := d.execVirsh("setmem", cfg.VmUUID, kib, "--config"); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("修改内存失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMUpdate", Message: "虚拟机配置更新成功"}, nil
}

// ReinstallVM 重装系统（删除旧盘并重建，随后启动）
func (d *QEMUDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmUUID := cfg.VmUUID
	_, _ = d.execVirsh("destroy", vmUUID)

	disk := d.diskPath(vmUUID)
	_, _ = d.execHostArgs([]string{"rm", "-f", disk}, 15*time.Second)

	sizeGB := cfg.HddNum / 1024
	if sizeGB <= 0 {
		sizeGB = 10
	}
	// 重装必须重新套用系统模板，不能建空盘，否则 guest 无系统无法启动
	if err := d.createDiskFromTemplate(cfg, disk, sizeGB); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: "重建磁盘失败: " + err.Error()}, nil
	}

	if out, err := d.execVirsh("start", vmUUID); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: fmt.Sprintf("启动失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	return &model.ZMessage{Success: true, Action: "VMReinstall", Message: "系统重装成功"}, nil
}

// GetVNCInfo 获取 VNC 连接信息
func (d *QEMUDriver) GetVNCInfo(vmUUID string, ipAddr string) (*model.ZMessage, error) {
	out, err := d.execVirsh("domdisplay", vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VCRemote",
			Message: "获取VNC信息失败: " + err.Error()}, nil
	}

	// virsh domdisplay 返回 vnc://localhost:0，冒号后是「display 编号」而不是端口；
	// 端口 = 5900 + display。原实现把它当端口，得到 port=0，前端直接判为不可用。
	display := strings.TrimSpace(out)
	host := d.Config.ServerAddr
	port := 5900
	if idx := strings.LastIndex(display, ":"); idx >= 0 {
		if n, err := strconv.Atoi(display[idx+1:]); err == nil {
			if n >= 5900 {
				port = n
			} else {
				port = 5900 + n
			}
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}

	return &model.ZMessage{
		Success: true,
		Action:  "VCRemote",
		Message: "VNC信息获取成功",
		Results: map[string]interface{}{
			"type": "vnc", "host": host, "port": port, "password": "",
		},
	}, nil
}

// MountHDD 挂载 / 卸载数据盘
func (d *QEMUDriver) MountHDD(vmUUID string, hdd *model.SDConfig, mount bool) (*model.ZMessage, error) {
	dev := "vdb"

	if !mount {
		_, _ = d.execVirsh("detach-disk", vmUUID, dev, "--persistent")
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

	path := hdd.HddPath
	if path == "" {
		dir := d.Config.ExternPath
		if dir == "" {
			dir = d.diskDir()
		}
		path = pathpkg.Join(dir, hdd.HddName+".qcow2")
	}

	if !d.hostFileExists(path) {
		sizeGB := hdd.HddSize / 1024
		if sizeGB <= 0 {
			sizeGB = 10
		}
		if _, err := d.execHostArgs(
			[]string{"qemu-img", "create", "-f", "qcow2", path, fmt.Sprintf("%dG", sizeGB)},
			2*time.Minute); err != nil {
			return &model.ZMessage{Success: false, Action: "HDDMount",
				Message: "创建数据盘失败: " + err.Error()}, nil
		}
	}

	if out, err := d.execVirsh("attach-disk", vmUUID, path, dev,
		"--type", "disk", "--persistent"); err != nil {
		return &model.ZMessage{Success: false, Action: "HDDMount",
			Message: fmt.Sprintf("挂载磁盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
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

// attachISO 把 ISO 放进 hdc 光驱。
//
// 创建虚拟机时已预置 IDE 光驱，优先换介质（运行中也能生效）；
// 早期创建的虚拟机没有光驱，退回 attach-disk 现场添加。
func (d *QEMUDriver) attachISO(vmUUID, path string) (string, error) {
	if out, err := d.execVirsh("change-media", vmUUID, "hdc", path, "--insert"); err == nil {
		return out, nil
	}
	if out, err := d.execVirsh("change-media", vmUUID, "hdc", path, "--update", "--force"); err == nil {
		return out, nil
	}
	return d.execVirsh("attach-disk", vmUUID, path, "hdc",
		"--type", "cdrom", "--mode", "readonly", "--persistent")
}

// MountISO 挂载 / 卸载 ISO（cdrom）
func (d *QEMUDriver) MountISO(vmUUID string, iso *model.IMConfig, mount bool) (*model.ZMessage, error) {
	if mount {
		path := iso.IsoPath
		if path == "" {
			dir := d.Config.DvdromPath
			if dir == "" {
				dir = d.Config.ImagesPath
			}
			path = pathpkg.Join(dir, iso.IsoFile)
		}
		if out, err := d.attachISO(vmUUID, path); err != nil {
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

	_, _ = d.execVirsh("change-media", vmUUID, "hdc", "--eject", "--live")
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		delete(cfg.IsoAll, iso.IsoName)
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "ISOMount", Message: "ISO卸载成功"}, nil
}

// CreateBackup 创建备份（基于 libvirt 快照）
func (d *QEMUDriver) CreateBackup(vmUUID string, tips string) (*model.ZMessage, error) {
	name := fmt.Sprintf("backup-%s", time.Now().Format("20060102-150405"))
	// 使用内部快照（含内存/磁盘状态）。--disk-only 会产生外部快照，
	// libvirt 明确不支持 snapshot-revert 外部快照，备份将无法还原。
	if out, err := d.execVirsh("snapshot-create-as", vmUUID, name, tips,
		"--atomic"); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: fmt.Sprintf("创建快照失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.Backups = append(cfg.Backups, model.VMBackup{
			BackupName: name, BackupTime: time.Now(), BackupTips: tips,
		})
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMBackup",
		Message: "备份创建成功", Results: map[string]interface{}{"backup_name": name}}, nil
}

// RestoreBackup 还原备份
func (d *QEMUDriver) RestoreBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	if out, err := d.execVirsh("snapshot-revert", vmUUID, backupName); err != nil {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("还原失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	return &model.ZMessage{Success: true, Action: "Restores", Message: "备份还原成功"}, nil
}

// DeleteBackup 删除备份
func (d *QEMUDriver) DeleteBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	if out, err := d.execVirsh("snapshot-delete", vmUUID, backupName); err != nil {
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

// Crontab 定时任务：同步电源状态
func (d *QEMUDriver) Crontab() bool {
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

// SetupUSB USB直通（virsh attach-device / detach-device）
// 与 Python 版 QEMUService.USBSetup 一致：生成 hostdev XML 后 attach/detach
func (d *QEMUDriver) SetupUSB(vmUUID string, info *model.USBInfos, usbKey string, add bool) (*model.ZMessage, error) {
	usbXML := fmt.Sprintf(`<hostdev mode='subsystem' type='usb' managed='yes'>
  <source>
    <vendor id='0x%s'/>
    <product id='0x%s'/>
  </source>
</hostdev>`, info.VidUUID, info.PidUUID)

	xmlPath := "/tmp/hostagent-usb-" + usbKey + ".xml"
	if err := d.writeHostFile(xmlPath, usbXML, 0644); err != nil {
		return &model.ZMessage{Success: false, Action: "USBSetup",
			Message: "写入XML失败: " + err.Error()}, nil
	}
	defer func() { _, _ = d.execHostArgs([]string{"rm", "-f", xmlPath}, 10*time.Second) }()

	action := "attach-device"
	if !add {
		action = "detach-device"
	}
	if out, err := d.execVirsh(action, vmUUID, xmlPath, "--persistent"); err != nil {
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

// SetPassword 设置虚拟机密码（通过 qemu-guest-agent）
func (d *QEMUDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	user := "root"
	d.mu.RLock()
	if cfg, ok := d.VmSaving[vmUUID]; ok && cfg.OsUser != "" {
		user = cfg.OsUser
	}
	d.mu.RUnlock()

	out, err := d.execVirsh("set-user-password", vmUUID, user, password)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPasswd",
			Message: fmt.Sprintf("密码修改失败（需要qemu-guest-agent）: %v (%s)",
				err, strings.TrimSpace(out))}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.OsPass = password
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "VMPasswd", Message: "密码修改成功"}, nil
}

// seedPath cloud-init NoCloud seed ISO 路径
func (d *QEMUDriver) seedPath(vmUUID string) string {
	return pathpkg.Join(d.diskDir(), vmUUID+"-seed.iso")
}

// seedDiskXML 生成 seed ISO 的 cdrom 片段
func seedDiskXML(seedISO string) string {
	if seedISO == "" {
		return ""
	}
	// Debian cloud 内核是精简内核，可能没有 AHCI/SATA 驱动，cdrom 在 guest 内不可见；
	// 用 virtio 磁盘挂载 seed ISO（只读），cloud-init 同样能按 cidata 卷标识别。
	return fmt.Sprintf(`
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='vdb' bus='virtio'/>
      <readonly/>
    </disk>`, seedISO)
}

// writeCloudInitSeed 生成 user-data/meta-data/network-config 并打包为 seed ISO。
// 失败时返回空串，虚拟机仍可启动（只是没有自动配置密码/网络）。
func (d *QEMUDriver) writeCloudInitSeed(cfg *model.VMConfig) string {
	if cfg == nil {
		return ""
	}
	tmp := "/tmp/hostagent-ci-" + cfg.VmUUID
	_, _ = d.execHostArgs([]string{"rm", "-rf", tmp}, 10*time.Second)
	if _, err := d.execHostArgs([]string{"mkdir", "-p", tmp}, 10*time.Second); err != nil {
		return ""
	}
	defer func() { _, _ = d.execHostArgs([]string{"rm", "-rf", tmp}, 10*time.Second) }()

	password := strings.TrimSpace(cfg.OsPass)
	user := strings.TrimSpace(cfg.OsUser)
	if user == "" {
		user = "root"
	}
	// cloud-init 的 netplan 配置在 Debian 云镜像里可能没有 netplan 渲染器，
	// 直接写 systemd-networkd 配置最稳；同时保留 network-config 供支持的镜像使用。
	nicName := "en*"
	staticLine := ""
	for _, nic := range cfg.NicAll {
		if strings.TrimSpace(nic.IP4Addr) != "" {
			prefix := "/24"
			if strings.TrimSpace(nic.NicMask) != "" {
				prefix = "/" + strings.TrimPrefix(strings.TrimSpace(nic.NicMask), "/")
			}
			gw := strings.TrimSpace(nic.NicGate)
			staticLine = fmt.Sprintf("Address=%s%s\nGateway=%s\nDNS=%s\n", nic.IP4Addr, prefix, gw, gw)
			break
		}
	}
	netBody := "[Match]\nName=" + nicName + "\n[Network]\n"
	if staticLine == "" {
		netBody += "DHCP=yes\n"
	} else {
		netBody += staticLine
	}

	userData := fmt.Sprintf(`#cloud-config
hostname: %s
preserve_hostname: false
ssh_pwauth: true
users:
  - name: %s
    lock_passwd: false
chpasswd:
  expire: false
  users:
    - {name: %s, password: '%s', type: text}
write_files:
  - path: /etc/systemd/network/20-primary.network
    permissions: '0644'
    content: |
%s
runcmd:
  - systemctl enable --now systemd-networkd
  - systemctl restart systemd-networkd
  - systemctl enable --now qemu-guest-agent
`, cfg.VmUUID, user, user, password, indentLines(netBody, "      "))

	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", cfg.VmUUID, cfg.VmUUID)

	networkConfig := `version: 2
ethernets:
  primary:
    match:
      name: "en*"
`
	if staticLine == "" {
		networkConfig += "    dhcp4: true\n"
	} else {
		networkConfig += "    dhcp4: false\n"
	}
	if err := d.writeHostFile(tmp+"/user-data", userData, 0600); err != nil {
		return ""
	}
	if err := d.writeHostFile(tmp+"/meta-data", metaData, 0644); err != nil {
		return ""
	}
	if err := d.writeHostFile(tmp+"/network-config", networkConfig, 0644); err != nil {
		return ""
	}

	seed := d.seedPath(cfg.VmUUID)
	if _, err := d.execHostArgs([]string{
		"cloud-localds", "--network-config", tmp + "/network-config",
		seed, tmp + "/user-data", tmp + "/meta-data",
	}, 60*time.Second); err != nil {
		return ""
	}
	return seed
}

// indentLines 给多行文本统一加缩进（用于 YAML 块）
func indentLines(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// splitLines 按行拆分并去除空白行
func splitLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
