// Package driver VMware Workstation 驱动生命周期实现（vmrest REST API）
// 与 Python 版 HostServer/Workstation.py + WorkstationAPI/VMWRestAPI.py 对齐：
//   - 基础地址 http://{server_addr}:{server_port}(默认8697)，路径前缀 /api
//   - 媒体类型 application/vnd.vmware.vmw.rest-v1+json
//   - 虚拟机标识为 vmrest 内部 id，需通过 GET /api/vms 的 path 反查（对应 select_vid）
//   - 创建流程：生成 VMX 文件 → POST /api/vms/registration 注册
//   - 电源：PUT /api/vms/{id}/power，请求体为纯字符串
package driver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

const vmrestContentType = "application/vnd.vmware.vmw.rest-v1+json"

// wsRequest 发送 vmrest 请求（返回响应体与状态码）
//
// 请求前确保 vmrest 服务可用：未启动时自动拉起本地进程（见 wsEnsureRunning）。
func (d *WorkstationDriver) wsRequest(method, path string, body interface{}) ([]byte, int, error) {
	if err := d.wsEnsureRunning(); err != nil {
		return nil, 0, err
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, d.wsBaseURL()+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", vmrestContentType)
	if d.Config.ServerUser != "" {
		req.SetBasicAuth(d.Config.ServerUser, d.Config.ServerPass)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode, nil
}

// wsVM vmrest 虚拟机条目
type wsVM struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	VMX   string `json:"vmx"`
	Index int    `json:"index"`
}

// selectVid 根据虚拟机名反查 vmrest 内部 id（对应 Python select_vid）
func (d *WorkstationDriver) selectVid(vmName string) string {
	// 优先使用缓存。
	// 注意：缓存读取必须收敛到独立函数里用 defer 解锁 ——
	// 原实现在命中缓存时直接 return，漏掉了 RUnlock，
	// 每调用一次就泄漏一个读锁，最终写者（调度器 SetVMPowerState）
	// 永远拿不到锁，GetVMSaving 等读接口全部卡死。
	if id := d.cachedVid(vmName); id != "" {
		return id
	}

	out, code, err := d.wsRequest(http.MethodGet, "/api/vms", nil)
	if err != nil || code != http.StatusOK {
		return ""
	}
	var list []wsVM
	if err := json.Unmarshal(out, &list); err != nil {
		return ""
	}
	// 精确匹配优先：否则 vmName 为 "vmw-1" 时会先命中 "vmw-110JgYXJ"
	for _, vm := range list {
		base := strings.TrimSuffix(filepath.Base(vm.Path), filepath.Ext(vm.Path))
		if base == vmName {
			return vm.ID
		}
	}
	for _, vm := range list {
		if vm.Path != "" && strings.Contains(vm.Path, vmName) {
			return vm.ID
		}
	}
	return ""
}

// vmrestIDByPath 按 VMX 绝对路径反查 vmrest 内部 id
func (d *WorkstationDriver) vmrestIDByPath(vmxPath string) string {
	if strings.TrimSpace(vmxPath) == "" {
		return ""
	}
	out, code, err := d.wsRequest(http.MethodGet, "/api/vms", nil)
	if err != nil || code != http.StatusOK {
		return ""
	}
	var list []wsVM
	if err := json.Unmarshal(out, &list); err != nil {
		return ""
	}
	target := filepath.Clean(vmxPath)
	for _, vm := range list {
		if vm.Path != "" && strings.EqualFold(filepath.Clean(vm.Path), target) {
			return vm.ID
		}
	}
	return ""
}

// wsVMXPath 通过 vmrest 列表反查虚拟机真实 VMX 路径。
//
// 不能直接用 system_path/<name>.vmx 猜测：vmrest 纳管的虚拟机可能位于任意目录
// （例如在 Workstation GUI 里创建的 E:\VBoxs\<name>\<name>.vmx），
// 猜测路径会导致截图等功能报「配置文件不存在」。
func (d *WorkstationDriver) wsVMXPath(vmName string) string {
	out, code, err := d.wsRequest(http.MethodGet, "/api/vms", nil)
	if err != nil || code != http.StatusOK {
		return ""
	}
	var list []wsVM
	if err := json.Unmarshal(out, &list); err != nil {
		return ""
	}
	for _, vm := range list {
		base := strings.TrimSuffix(filepath.Base(vm.Path), filepath.Ext(vm.Path))
		if base == vmName {
			return vm.Path
		}
	}
	for _, vm := range list {
		if vm.Path != "" && strings.Contains(vm.Path, vmName) {
			return vm.Path
		}
	}
	return ""
}

// cachedVid 读取缓存的 vmrest 内部 id（只读，锁内完成，不覆盖网络调用）。
func (d *WorkstationDriver) cachedVid(vmName string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cfg, ok := d.VmSaving[vmName]
	if !ok || cfg == nil || cfg.ExtData == nil {
		return ""
	}
	v, exists := cfg.ExtData["vmid"]
	if !exists {
		return ""
	}
	return strVal(v)
}

// setVid 缓存 vmrest 内部 id
func (d *WorkstationDriver) setVid(cfg *model.VMConfig, id string) {
	if cfg.ExtData == nil {
		cfg.ExtData = make(map[string]interface{})
	}
	cfg.ExtData["vmid"] = id
}

// GetVMList 获取虚拟机列表
func (d *WorkstationDriver) GetVMList() (map[string]*model.VMConfig, error) {
	result := d.GetVMSaving()

	out, code, err := d.wsRequest(http.MethodGet, "/api/vms", nil)
	if err != nil || code != http.StatusOK {
		return result, nil
	}

	var list []wsVM
	if err := json.Unmarshal(out, &list); err != nil {
		return result, nil
	}

	for _, item := range list {
		if item.ID == "" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path))
		if name == "" {
			name = item.ID
		}

		var flag model.VMPowers = model.VMPowerUnknown
		// 走 powerOf（含 vmrun 复核），与调度器的 GetPower 保持一致
		if s, ok := PowerStatusMap[d.powerOf(item.ID, name)]; ok {
			flag = s
		}

		if cfg, ok := result[name]; ok {
			cfg.VmFlag = flag
			d.setVid(cfg, item.ID)
		} else {
			cfg := model.NewVMConfig()
			cfg.VmUUID = name
			cfg.OsName = name
			cfg.VmFlag = flag
			d.setVid(cfg, item.ID)
			result[name] = cfg
		}
	}
	return result, nil
}

// wsGetPower 通过 vmrest 查询电源状态
func (d *WorkstationDriver) wsGetPower(vmID string) string {
	out, code, err := d.wsRequest(http.MethodGet, fmt.Sprintf("/api/vms/%s/power", vmID), nil)
	if err != nil || code != http.StatusOK {
		return "未知"
	}
	switch strings.ToLower(parseVMRestPower(out)) {
	case "poweredon", "on":
		return "运行中"
	case "poweredoff", "off":
		return "已关机"
	case "suspended", "paused":
		return "已暂停"
	case "poweringon":
		return "启动中"
	case "poweringoff":
		return "关闭中"
	case "suspending":
		return "暂停中"
	case "resetting":
		return "重启中"
	default:
		return "未知"
	}
}

// parseVMRestPower 解析 vmrest 的电源响应。
//
// 实际返回是 JSON 对象 {"power_state":"poweredOn"}（不同版本也可能是裸字符串
// "poweredOn"）。旧实现只去掉外层引号后比对，会把整段 JSON 当成状态值，
// 导致所有虚拟机电源状态恒为「未知」，进而让截图等功能判定为「虚拟机未运行」。
func parseVMRestPower(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return ""
	}
	var obj struct {
		PowerState string `json:"power_state"`
	}
	if err := json.Unmarshal(out, &obj); err == nil && obj.PowerState != "" {
		return obj.PowerState
	}
	var s string
	if err := json.Unmarshal(out, &s); err == nil {
		return s
	}
	return strings.Trim(text, "\"")
}

// ScanVMs 扫描并同步虚拟机列表
//
// 与 Python 版 Workstation.VMDetect 对齐：使用主机配置的 filter_name
// 作为名称前缀过滤，只纳管带该前缀的虚拟机。
func (d *WorkstationDriver) ScanVMs() (*model.ZMessage, error) {
	vms, err := d.GetVMList()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDetect", Message: err.Error()}, nil
	}

	// 前缀过滤（未配置 filter_name 时不过滤）
	prefix := strings.TrimSpace(d.Config.FilterName)
	d.mu.Lock()
	scanned := 0
	added := 0
	for name, cfg := range vms {
		if !MatchFilterPrefix(prefix, cfg) {
			continue
		}
		scanned++
		if _, exists := d.VmSaving[name]; !exists {
			d.VmSaving[name] = cfg
			added++
		} else {
			d.VmSaving[name].VmFlag = cfg.VmFlag
			d.setVid(d.VmSaving[name], strVal(cfg.ExtData["vmid"]))
		}
	}

	// 移除历史遗留的「前缀不匹配」纳管记录（早期版本 Crontab 未过滤导致）。
	// 只删本机记录，不做任何平台操作；返回 uuid 列表供上层同步清理数据库。
	var removed []string
	if prefix != "" {
		for name, cfg := range d.VmSaving {
			if MatchFilterPrefix(prefix, cfg) {
				continue
			}
			delete(d.VmSaving, name)
			removed = append(removed, name)
		}
	}
	d.mu.Unlock()

	msg := fmt.Sprintf("扫描完成：匹配 %d 个，新增 %d 个", scanned, added)
	if len(removed) > 0 {
		msg += fmt.Sprintf("，清理 %d 个前缀不匹配记录", len(removed))
	}
	return &model.ZMessage{Success: true, Action: "VMDetect", Message: msg,
		Results: map[string]interface{}{"added": added, "removed": removed}}, nil
}

// PowerControl 电源控制
// 与 Python powers_set 对齐：PUT /api/vms/{id}/power，body 为纯字符串
func (d *WorkstationDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	var op string
	switch action {
	case model.VMPowerSStart:
		op = "on"
	case model.VMPowerSClose:
		op = "shutdown"
	case model.VMPowerHClose:
		op = "off"
	case model.VMPowerSReset:
		op = "reset"
	case model.VMPowerHReset:
		op = "reset"
	case model.VMPowerAPause:
		op = "pause"
	case model.VMPowerAWaked:
		op = "unpause"
	default:
		return d.BaseDriver.PowerControl(vmUUID, action)
	}

	if err := d.wsEnsureRunning(); err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: err.Error()}, nil
	}

	vmID := d.selectVid(vmUUID)
	if vmID == "" {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("未找到虚拟机: %s", vmUUID)}, nil
	}

	// 开机前补齐 VNC 控制台配置：Workstation GUI 里创建的虚拟机默认没有 VNC，
	// 会导致截图与 noVNC 控制台都不可用。仅追加、不覆盖已有配置，失败不阻断开机。
	if op == "on" {
		if vmx := d.wsVMXPath(vmUUID); vmx != "" {
			d.mu.RLock()
			vcPort := ""
			if cfg, ok := d.VmSaving[vmUUID]; ok && cfg != nil {
				vcPort = cfg.VcPort
			}
			d.mu.RUnlock()
			_ = ensureVMXVNC(vmx, vcPort)
		}
	}

	req, err := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/api/vms/%s/power", d.wsBaseURL(), vmID),
		bytes.NewBufferString(op))
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: err.Error()}, nil
	}
	req.Header.Set("Content-Type", vmrestContentType)
	if d.Config.ServerUser != "" {
		req.SetBasicAuth(d.Config.ServerUser, d.Config.ServerPass)
	}
	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: "电源操作失败: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("电源操作失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))}, nil
	}

	if s, ok := PowerStatusMap[d.powerOf(vmID, vmUUID)]; ok {
		d.mu.Lock()
		if cfg, exists := d.VmSaving[vmUUID]; exists {
			cfg.VmFlag = s
		}
		d.mu.Unlock()
	}
	return &model.ZMessage{Success: true, Action: "VMPowers", Message: "电源操作已执行"}, nil
}

// SupportsGracefulShutdown vmrest 的 shutdown 走 VMware Tools 优雅关机。
func (d *WorkstationDriver) SupportsGracefulShutdown() bool { return true }

// CreateVM 创建虚拟机（生成 VMX → 注册到 vmrest）
func (d *WorkstationDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	dir := d.Config.SystemPath
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "创建目录失败: " + err.Error()}, nil
	}

	// 系统盘必须先就位：VMX 的 scsi0:0.fileName 是硬引用，
	// 缺盘时 VMware 直接拒绝开机（File not found: <uuid>.vmdk）。
	diskName, warn, err := d.prepareSystemDisk(cfg, dir, false)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "准备系统盘失败: " + err.Error()}, nil
	}

	vmxPath := filepath.Join(dir, cfg.VmUUID+".vmx")
	if err := os.WriteFile(vmxPath, []byte(d.buildVMX(cfg, diskName, d.installISO(cfg))), 0644); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "写入VMX失败: " + err.Error()}, nil
	}

	out, code, err := d.wsRequest(http.MethodPost, "/api/vms/registration",
		map[string]interface{}{"name": cfg.VmUUID, "path": vmxPath})
	registeredID := ""
	if err == nil && (code == http.StatusOK || code == http.StatusCreated) {
		var resp struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(out, &resp)
		registeredID = resp.ID
	} else {
		// VMware 的 inventory 里可能残留上一次删除的同路径记录（vmrest DELETE
		// 偶尔只把条目移出列表、不清 inventory），重新注册会报 Code 147
		// "VM already added"。此时虚拟机其实已在纳管列表中，按成功处理。
		if registeredID = d.vmrestIDByPath(vmxPath); registeredID == "" {
			return &model.ZMessage{Success: false, Action: "VMCreate",
				Message: fmt.Sprintf("注册虚拟机失败: %v (HTTP %d, %s)", err, code, strings.TrimSpace(string(out)))}, nil
		}
	}

	cfg.VmFlag = model.VMPowerStopped
	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	if registeredID != "" {
		d.setVid(cfg, registeredID)
	}
	d.mu.Unlock()

	// 注册响应未带 id 时回填 vmrest id
	if registeredID == "" {
		if id := d.selectVid(cfg.VmUUID); id != "" {
			d.mu.Lock()
			d.setVid(cfg, id)
			d.mu.Unlock()
		}
	}

	msg := "虚拟机创建成功"
	if warn != "" {
		msg += "（" + warn + "）"
	}

	// 创建完成后直接开机：VMX 与系统盘均已就绪，用户不必再手动点一次开机
	if res, startErr := d.PowerControl(cfg.VmUUID, model.VMPowerSStart); startErr != nil {
		msg += "；自动开机失败: " + startErr.Error()
	} else if res != nil && !res.Success {
		msg += "；自动开机失败: " + res.Message
	} else {
		msg += "，已自动开机"
	}

	// VMware 在首次开机时才会把 generatedAddress 写进 vmx，因此回写必须放在开机之后。
	d.wsSyncRealMACs(vmxPath, cfg.NicAll)

	return &model.ZMessage{Success: true, Action: "VMCreate", Message: msg}, nil
}

// wsSyncRealMACs 读取 .vmx 里 VMware 实际生成的 MAC 并回写配置。
//
// 与 Hyper-V 同理：VMware 默认使用动态 MAC（00:0C:29 前缀），与 IP 池按 IP
// 推导出来的 MAC 不一致。不回写的话，虚拟机内 CloudInit 上报状态时
// findVMByMAC 永远匹配不到，表现为「上报了但系统里看不到状态」。
//
// 读取失败只影响状态展示，不阻断创建，因此不返回错误。
func (d *WorkstationDriver) wsSyncRealMACs(vmxPath string, nics map[string]model.NCConfig) {
	if len(nics) == 0 || strings.TrimSpace(vmxPath) == "" {
		return
	}
	raw, err := os.ReadFile(vmxPath)
	if err != nil {
		return
	}

	// vmx 里按网卡下标存放：ethernet0.address / ethernet0.generatedAddress
	real := make(map[string]string, len(nics))
	for _, line := range strings.Split(string(raw), "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		dot := strings.Index(key, ".")
		if dot <= 0 {
			continue
		}
		nicName, field := key[:dot], key[dot+1:]
		mac := normalizeVMac(kv[1])
		if mac == "" {
			continue
		}
		switch field {
		case "address":
			real[nicName] = mac // 显式指定优先
		case "generatedaddress":
			if _, exists := real[nicName]; !exists {
				real[nicName] = mac
			}
		}
	}

	for name := range nics {
		mac, ok := real[strings.ToLower(name)]
		if !ok {
			continue
		}
		nic := nics[name]
		nic.MacAddr = mac
		nics[name] = nic
	}
}

// buildVMX 生成最小可用的 VMX 配置。
// diskName 必须是 system_path 下真实存在的系统盘文件名，
// isoPath 非空时挂到光驱（用于 ISO 安装镜像）。
func (d *WorkstationDriver) buildVMX(cfg *model.VMConfig, diskName, isoPath string) string {
	mem := cfg.MemNum
	if mem <= 0 {
		mem = 2048
	}
	cpu := cfg.CpuNum
	if cpu <= 0 {
		cpu = 2
	}
	guestOS := d.guestOS(cfg.OsName)
	vncPort := cfg.VcPort
	if vncPort == "" {
		vncPort = RandomVNCPort()
	}
	if strings.TrimSpace(diskName) == "" {
		diskName = cfg.VmUUID + ".vmdk"
	}

	cdrom := ""
	if isoPath != "" {
		cdrom = fmt.Sprintf("ide1:0.present = \"TRUE\"\n"+
			"ide1:0.deviceType = \"cdrom-image\"\n"+
			"ide1:0.fileName = \"%s\"\n"+
			"ide1:0.startConnected = \"TRUE\"\n", isoPath)
	}

	// 系统盘控制器按镜像来源选择（vhd → SATA，vmdk → SCSI）：
	// Windows 精简镜像只保留制作时使用的存储驱动，选错会 0x7B 蓝屏。
	controller := diskControllerLines(d.diskBus(cfg, diskName), diskName)

	// UEFI 引导：云镜像（win-10-lite / debian cloud）均按 UEFI 构建，
	// 传统 BIOS 引导会停在 "no bootable device"。
	// Secure Boot 关闭：这些自制镜像没有微软签名，开启后会被固件拒绝引导。
	// ulm.disableMitigations：关闭 Hyper-V 侧通道缓解（Spectre/Meltdown 补丁），
	// 否则在已启用 Hyper-V 的宿主上 CPU 性能会明显下降。
	return fmt.Sprintf(`.encoding = "UTF-8"
config.version = "8"
virtualHW.version = "19"
displayName = "%s"
memsize = "%d"
numvcpus = "%d"
guestOS = "%s"
firmware = "efi"
uefi.secureBoot.enabled = "FALSE"
ulm.disableMitigations = "TRUE"
%sethernet0.present = "TRUE"
ethernet0.connectionType = "nat"
ethernet0.virtualDev = "e1000"
ethernet0.startConnected = "TRUE"
floppy0.present = "FALSE"
%sRemoteDisplay.vnc.enabled = "TRUE"
RemoteDisplay.vnc.port = "%s"
`, cfg.VmUUID, mem, cpu, guestOS, controller, cdrom, vncPort)
}

// guestOS 将系统名映射为 VMware guestOS 标识（与 Python create_vmx 的映射思路一致）
func (d *WorkstationDriver) guestOS(osName string) string {
	name := strings.ToLower(strings.TrimSpace(osName))
	switch {
	case strings.Contains(name, "win11"), strings.Contains(name, "windows11"):
		return "windows10-64"
	case strings.Contains(name, "win10"), strings.Contains(name, "windows10"):
		return "windows9-64"
	case strings.Contains(name, "2022"):
		return "windows9Server-64"
	case strings.Contains(name, "2019"):
		return "windows8Server-64"
	case strings.Contains(name, "win"):
		return "windows9-64"
	default:
		return "otherlinux-64"
	}
}

// DeleteVM 删除虚拟机
func (d *WorkstationDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	vmID := d.selectVid(vmUUID)
	if vmID == "" {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: fmt.Sprintf("未找到虚拟机: %s", vmUUID)}, nil
	}

	out, code, err := d.wsRequest(http.MethodDelete, "/api/vms/"+vmID, nil)
	if err != nil || (code != http.StatusOK && code != http.StatusNoContent) {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: fmt.Sprintf("删除失败: %v (HTTP %d, %s)", err, code, strings.TrimSpace(string(out)))}, nil
	}

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDelete", Message: "虚拟机删除成功"}, nil
}

// UpdateVM 更新虚拟机配置（PUT /api/vms/{id}）
func (d *WorkstationDriver) UpdateVM(cfg *model.VMConfig, oldCfg *model.VMConfig) (*model.ZMessage, error) {
	vmID := d.selectVid(cfg.VmUUID)
	if vmID != "" {
		payload := make(map[string]interface{})
		if cfg.CpuNum != oldCfg.CpuNum {
			payload["numvcpus"] = cfg.CpuNum
		}
		if cfg.MemNum != oldCfg.MemNum {
			payload["memsize"] = cfg.MemNum
		}
		if len(payload) > 0 {
			if out, code, err := d.wsRequest(http.MethodPut, "/api/vms/"+vmID, payload); err != nil ||
				(code != http.StatusOK && code != http.StatusNoContent) {
				return &model.ZMessage{Success: false, Action: "VMUpdate",
					Message: fmt.Sprintf("更新失败: %v (HTTP %d, %s)", err, code, strings.TrimSpace(string(out)))}, nil
			}
		}
	}

	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMUpdate", Message: "虚拟机配置更新成功"}, nil
}

// SetPassword 设置虚拟机密码
// vmrest 无改密端点，与 Python 基类 VMPasswd 一致：更新 os_pass 配置
func (d *WorkstationDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, ok := d.VmSaving[vmUUID]
	if !ok {
		return &model.ZMessage{Success: false, Action: "Password", Message: "虚拟机不存在"}, nil
	}
	cfg.OsPass = password
	return &model.ZMessage{Success: true, Action: "Password", Message: "密码已更新"}, nil
}

// GetVNCInfo 获取远程控制台信息（读取 VMX 中的 VNC 端口）
func (d *WorkstationDriver) GetVNCInfo(vmUUID string, ipAddr string) (*model.ZMessage, error) {
	host, _ := d.wsHostPort()
	if wsIsLocalAddress(host) {
		host = "127.0.0.1"
	}

	// 端口以 VMX 实际配置为准：配置中心记录的 vc_port 可能与之不一致
	// （外部导入的虚拟机尤其明显），不一致时控制台会连到错误端口。
	port := 0
	if vmx := d.wsVMXPath(vmUUID); vmx != "" {
		if _, _, p := readVMXVNCConfig(vmx); p > 0 {
			port = p
		}
	}
	if port <= 0 {
		d.mu.RLock()
		if cfg, ok := d.VmSaving[vmUUID]; ok && cfg.VcPort != "" {
			fmt.Sscanf(cfg.VcPort, "%d", &port)
		}
		d.mu.RUnlock()
	}
	if port <= 0 {
		port = 5900
	}

	return &model.ZMessage{Success: true, Action: "VCRemote", Message: "控制台信息获取成功",
		Results: map[string]interface{}{
			"type": "vnc", "host": host, "port": port, "password": "",
		}}, nil
}

// Crontab 定时任务：同步虚拟机状态
func (d *WorkstationDriver) Crontab() bool {
	vms, err := d.GetVMList()
	if err != nil {
		return false
	}
	// 与 ScanVMs 使用同一套前缀规则：调度器每轮都会跑 Crontab，
	// 若不在这里过滤，平台上的其它虚拟机会被逐分钟「偷偷纳管」，
	// 表现为「明明配了 filter_name，扫描/列表里却出现不带前缀的虚拟机」。
	prefix := strings.TrimSpace(d.Config.FilterName)
	d.mu.Lock()
	for name, cfg := range vms {
		if !MatchFilterPrefix(prefix, cfg) {
			continue
		}
		if existing, ok := d.VmSaving[name]; ok {
			existing.VmFlag = cfg.VmFlag
			if cfg.ExtData != nil {
				d.setVid(existing, strVal(cfg.ExtData["vmid"]))
			}
		} else {
			d.VmSaving[name] = cfg
		}
	}
	d.mu.Unlock()
	return true
}
