// Package driver Proxmox VE 驱动
// 使用 github.com/luthermonson/go-proxmox 库
package driver

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TLSVerifyEnabled 返回是否校验下游平台证书。
//
// 默认保持与 Python 版一致的「不校验」，因为 PVE / ESXi 普遍使用
// 自签名证书，强制校验会导致既有部署无法连接。
// 可通过环境变量 OPENIDCS_TLS_VERIFY=1 启用校验以获得完整的主机身份保证。
func TLSVerifyEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("OPENIDCS_TLS_VERIFY")))
	return v == "1" || v == "true" || v == "yes"
}

// platformHTTPClient 返回访问虚拟化平台 API 的 HTTP 客户端。
//
// 必须设置整体超时：原实现没有 Timeout，下游不响应时会让调用方
// 永久阻塞（持锁时还会拖住整台主机的所有操作）。
func platformHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: !TLSVerifyEnabled()},
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}

// ProxmoxDriver Proxmox VE 驱动
type ProxmoxDriver struct {
	*BaseDriver
	client   *proxmox.Client
	clientMu sync.Mutex

	// VNC 走 vncwebsocket 时除了 vncticket 还要求请求本身已登录，
	// 这里缓存 access/ticket 拿到的 PVEAuthCookie 与 CSRF token
	// （有效期约 2 小时），代理每次连接都据此现场创建 vnc 会话。
	ticketMu     sync.Mutex
	apiTicket    string
	apiCSRF      string
	apiTicketExp time.Time
}

// pveAPITicket 返回 PVE 登录票据与 CSRF token（用于 PVEAuthCookie / 写操作）。
func (d *ProxmoxDriver) pveAPITicket(ctx context.Context) (string, string, error) {
	d.ticketMu.Lock()
	if d.apiTicket != "" && time.Now().Before(d.apiTicketExp) {
		ticket, csrf := d.apiTicket, d.apiCSRF
		d.ticketMu.Unlock()
		return ticket, csrf, nil
	}
	d.ticketMu.Unlock()

	user := d.Config.ServerUser
	if user == "" {
		user = "root"
	}
	if !strings.Contains(user, "@") {
		user += "@pam"
	}

	form := url.Values{}
	form.Set("username", user)
	form.Set("password", d.Config.ServerPass)

	base := strings.TrimSuffix(d.proxmoxBaseURL(), proxmoxAPIPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+proxmoxAPIPath+"/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := platformHTTPClient().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("PVE 登录失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("PVE 登录失败（HTTP %d）", resp.StatusCode)
	}

	var out struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data.Ticket == "" {
		return "", "", fmt.Errorf("PVE 登录响应异常")
	}

	d.ticketMu.Lock()
	d.apiTicket = out.Data.Ticket
	d.apiCSRF = out.Data.CSRF
	d.apiTicketExp = time.Now().Add(100 * time.Minute)
	d.ticketMu.Unlock()
	return out.Data.Ticket, out.Data.CSRF, nil
}

// pveVNCSession 创建一个 web 端使用的 VNC 会话，返回端口、ticket 与 VNC 密码。
//
// 关键：必须带 websocket=1。PVE 只在该参数下生成 VNC 协议密码，
// 并把它拼在 ticket 最前面（PVE::API2::Qemu：$ticket = "${password}:${ticket}"）。
// 不带该参数拿到的是纯 ticket，QEMU 会以 Authentication failed 拒绝连接。
func (d *ProxmoxDriver) pveVNCSession(vmid int64) (int, string, string, error) {
	ctx, cancel := context.WithTimeout(d.Ctx(""), 20*time.Second)
	defer cancel()

	cookie, csrf, err := d.pveAPITicket(ctx)
	if err != nil {
		return 0, "", "", err
	}

	base := strings.TrimSuffix(d.proxmoxBaseURL(), proxmoxAPIPath)
	u := fmt.Sprintf("%s%s/nodes/%s/qemu/%d/vncproxy",
		base, proxmoxAPIPath, d.nodeName(), vmid)

	form := url.Values{}
	form.Set("websocket", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "PVEAuthCookie="+cookie)
	req.Header.Set("CSRFPreventionToken", csrf)

	resp, err := platformHTTPClient().Do(req)
	if err != nil {
		return 0, "", "", fmt.Errorf("创建VNC会话失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return 0, "", "", fmt.Errorf("创建VNC会话失败（HTTP %d）: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Data struct {
			Port   interface{} `json:"port"`
			Ticket string      `json:"ticket"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, "", "", fmt.Errorf("解析VNC会话响应失败: %w", err)
	}

	port := 0
	switch v := out.Data.Port.(type) {
	case float64:
		port = int(v)
	case string:
		port, _ = strconv.Atoi(v)
	}
	if port <= 0 || out.Data.Ticket == "" {
		return 0, "", "", fmt.Errorf("VNC会话响应缺少 port/ticket")
	}

	// PVE 生成的密码是 8 个可打印 ASCII，拼在 ticket 最前面
	password := out.Data.Ticket
	if len(password) > 8 {
		password = password[:8]
	}
	return port, out.Data.Ticket, password, nil
}

// proxmoxAPIPath go-proxmox 客户端要求的 API 路径前缀。
//
// go-proxmox 的 NewClient(baseURL) 不会自动补 /api2/json：其 Req 对以
// "/" 开头的路径直接做 baseURL+path 拼接，因此 baseURL 必须自带
// /api2/json。若只传 "https://host:8006"，会去请求 https://host:8006/version，
// 被 Proxmox 的 pveproxy 以 `500 no such file '/version'` 拒绝
// （表现为「连接Proxmox失败: 500 no such file '/version'」）。
const proxmoxAPIPath = "/api2/json"

// pveAudioDevice PVE 声卡配置。
//
// 会议、媒体播放等桌面应用在没有任何音频设备时会直接不工作，
// 因此默认分配一块 HDA 声卡。
//
// 注意 driver 必须是 none：spice 音频要求虚拟机同时启用 SPICE 显示
// （QEMU 需要 -spice 参数），普通 VNC 显示下会直接启动失败：
//
//	kvm: Cannot use spice audio without -spice
//
// driver=none 仍然会创建音频设备（应用能检测到声卡），只是没有音频后端。
// 个别老版本 PVE 不认识该型号时，创建会自动去掉该项重试。
const pveAudioDevice = "device=ich9-intel-hda,driver=none"

// proxmoxBaseURL 归一化出 go-proxmox 的 API 基地址 https://host:port/api2/json。
//
// 兼容以下常见填写方式：
//   - 192.168.1.50                        → https://192.168.1.50:8006/api2/json
//   - 192.168.1.50:8006
//   - https://192.168.1.50:8006/
//   - https://pve.example.com/pve/api2/json（反向代理前缀会被保留）
func (d *ProxmoxDriver) proxmoxBaseURL() string {
	raw := strings.TrimSpace(d.Config.ServerAddr)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimRight(raw, "/")

	host, path := raw, ""
	if i := strings.Index(raw, "/"); i >= 0 {
		host, path = raw[:i], strings.TrimRight(raw[i:], "/")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	// 未显式指定端口时：直连场景补默认 8006；
	// 若填写了路径（反向代理前缀），则沿用 https 默认端口
	if !hostHasPort(host) && path == "" {
		host += ":8006"
	}
	switch {
	case path == "" || path == "/":
		path = proxmoxAPIPath
	case !strings.HasSuffix(path, proxmoxAPIPath):
		path += proxmoxAPIPath
	}
	return "https://" + host + path
}

// proxmoxHostPort 返回 host:port（不含协议与 API 路径），供 VNC 等直连场景使用。
func (d *ProxmoxDriver) proxmoxHostPort() string {
	host := strings.TrimPrefix(d.proxmoxBaseURL(), "https://")
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	return host
}

// filterVMOptions 过滤掉指定名称的配置项（用于按能力降级重试）。
func filterVMOptions(opts []proxmox.VirtualMachineOption, names ...string) []proxmox.VirtualMachineOption {
	skip := make(map[string]bool, len(names))
	for _, n := range names {
		skip[n] = true
	}
	out := make([]proxmox.VirtualMachineOption, 0, len(opts))
	for _, o := range opts {
		if skip[o.Name] {
			continue
		}
		out = append(out, o)
	}
	return out
}

// proxmoxHost 返回不含端口的节点地址（仅用于展示与兼容字段）。
func (d *ProxmoxDriver) proxmoxHost() string {
	host := d.proxmoxHostPort()
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// hostHasPort 判断 host[:port] 是否已带端口（兼容 IPv6 字面量 [::1]:8006）
func hostHasPort(host string) bool {
	if strings.HasPrefix(host, "[") {
		_, _, err := net.SplitHostPort(host)
		return err == nil
	}
	return strings.Contains(host, ":")
}

// NewProxmoxDriver 创建Proxmox驱动
func NewProxmoxDriver(cfg *model.HSConfig) *ProxmoxDriver {
	return &ProxmoxDriver{BaseDriver: NewBaseDriver(cfg)}
}

// ============================================================
// 连接管理
// ============================================================

// apiConn 获取Proxmox API连接（懒加载+复用）
//
// 原实现每次调用都在持锁状态下做一次网络探测，且使用永不取消的
// context.Background()：PVE 不响应时会占住连接锁并无限阻塞，
// 进而拖住该主机后续所有操作、整轮状态采集以及进程退出。
// 现在改为：锁内只读取指针，慢路径在锁外建立连接并带超时。
func (d *ProxmoxDriver) apiConn() (*proxmox.Client, error) {
	d.clientMu.Lock()
	existing := d.client
	d.clientMu.Unlock()
	if existing != nil {
		return existing, nil
	}

	user := d.Config.ServerUser
	if user == "" {
		user = "root"
	}
	if !strings.Contains(user, "@") {
		user = user + "@pam"
	}

	client := proxmox.NewClient(
		d.proxmoxBaseURL(),
		proxmox.WithCredentials(&proxmox.Credentials{
			Username: user,
			Password: d.Config.ServerPass,
		}),
		proxmox.WithHTTPClient(platformHTTPClient()),
	)

	// 建立连接带超时，避免依赖不可用时阻塞调用方
	ctx, cancel := context.WithTimeout(d.Ctx(""), 30*time.Second)
	defer cancel()
	if _, err := client.Version(ctx); err != nil {
		return nil, fmt.Errorf("连接Proxmox失败: %w", err)
	}

	d.clientMu.Lock()
	// 并发首次访问时以先发布者为准，避免重复覆盖
	if d.client == nil {
		d.client = client
	}
	result := d.client
	d.clientMu.Unlock()
	return result, nil
}

// resetConn 在调用失败时丢弃连接，令下次访问重新建立。
func (d *ProxmoxDriver) resetConn() {
	d.clientMu.Lock()
	d.client = nil
	d.clientMu.Unlock()
}

// nodeName 获取PVE节点名
func (d *ProxmoxDriver) nodeName() string {
	if d.Config.LaunchPath != "" {
		return d.Config.LaunchPath
	}
	return "pve"
}

// getNode 获取节点对象
func (d *ProxmoxDriver) getNode() (*proxmox.Node, error) {
	client, err := d.apiConn()
	if err != nil {
		return nil, err
	}
	node, err := client.Node(d.Ctx(""), d.nodeName())
	if err != nil {
		return nil, fmt.Errorf("获取节点失败: %w", err)
	}
	return node, nil
}

// ProxmoxSameVMName 判断两个虚拟机名是否为「同一台」。
//
// Proxmox 平台上的名称使用连字符，而早期版本的 Go 驱动在扫描时把 "-"
// 替换成 "_" 作为 vm_uuid，导致同一台虚拟机在库里出现 pve-xxx 与
// pve_xxx 两种键。这里把 "_" 与 "-" 视为等价来判重。
func ProxmoxSameVMName(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.ReplaceAll(a, "_", "-") == strings.ReplaceAll(b, "_", "-")
}

// findExistingVMKeyLocked 在锁内按命名变体查找已存在的 vm_uuid（未找到返回空串）。
func (d *ProxmoxDriver) findExistingVMKeyLocked(name string) string {
	if name == "" {
		return ""
	}
	if _, ok := d.VmSaving[name]; ok {
		return name
	}
	for key := range d.VmSaving {
		if ProxmoxSameVMName(key, name) {
			return key
		}
	}
	return ""
}

// getVMID 根据vm_uuid查找VMID（先查缓存，再查API）
func (d *ProxmoxDriver) getVMID(vmUUID string) (int64, error) {
	d.mu.RLock()
	cfg, ok := d.VmSaving[vmUUID]
	if !ok {
		// 兼容历史数据的命名变体（"_" 与 "-" 差异）
		if variant := d.findExistingVMKeyLocked(vmUUID); variant != "" {
			cfg, ok = d.VmSaving[variant]
		}
	}
	d.mu.RUnlock()

	if ok {
		if vmData, ok2 := cfg.ExtData["vmid"]; ok2 {
			if id, ok3 := vmData.(float64); ok3 && id > 0 {
				return int64(id), nil
			}
		}
	}

	// 从API查找
	node, err := d.getNode()
	if err != nil {
		return 0, err
	}

	vms, err := node.VirtualMachines(d.Ctx(""))
	if err != nil {
		return 0, fmt.Errorf("获取VM列表失败: %w", err)
	}

	// 平台上的名称优先与 vm_uuid 一致，其次兼容 "_"/"-" 互换的历史形式
	names := []string{vmUUID}
	for _, variant := range []string{
		strings.ReplaceAll(vmUUID, "_", "-"),
		strings.ReplaceAll(vmUUID, "-", "_"),
	} {
		if variant != vmUUID {
			names = append(names, variant)
		}
	}
	for _, vm := range vms {
		matched := false
		for _, n := range names {
			if vm.Name == n {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		// 缓存VMID
		d.mu.Lock()
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			if cfg.ExtData == nil {
				cfg.ExtData = make(map[string]interface{})
			}
			cfg.ExtData["vmid"] = float64(vm.VMID)
		}
		d.mu.Unlock()
		return int64(vm.VMID), nil
	}
	return 0, fmt.Errorf("未找到虚拟机 %s 的VMID", vmUUID)
}

// getVM 获取VM对象
func (d *ProxmoxDriver) getVM(vmUUID string) (*proxmox.VirtualMachine, error) {
	node, err := d.getNode()
	if err != nil {
		return nil, err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return nil, err
	}
	vm, err := node.VirtualMachine(d.Ctx(""), int(vmid))
	if err != nil {
		return nil, fmt.Errorf("获取VM对象失败: %w", err)
	}
	return vm, nil
}

// newVMID 分配新的VMID
func (d *ProxmoxDriver) newVMID() (int, error) {
	node, err := d.getNode()
	if err != nil {
		return 100, nil
	}
	vms, err := node.VirtualMachines(d.Ctx(""))
	if err != nil {
		return 100, nil
	}
	used := make(map[int]bool)
	for _, vm := range vms {
		used[int(vm.VMID)] = true
	}
	for id := 100; id < 9999; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 100, nil
}

// ============================================================
// 电源状态
// ============================================================

// GetPower 获取虚拟机电源状态
func (d *ProxmoxDriver) GetPower(vmUUID string) string {
	vm, err := d.getVM(vmUUID)
	if err != nil {
		return "未知"
	}
	switch vm.Status {
	case "running":
		return "运行中"
	case "stopped":
		return "已关机"
	case "paused":
		return "已暂停"
	default:
		return "未知"
	}
}

// GetStatus 获取主机状态
func (d *ProxmoxDriver) GetStatus() (*model.HWStatus, error) {
	node, err := d.getNode()
	if err != nil {
		// 远程平台采集失败返回空状态，不能用本机资源替代
		return emptyHWStatus(), nil
	}

	if err := node.Status(d.Ctx("")); err != nil {
		return emptyHWStatus(), nil
	}

	hw := &model.HWStatus{
		OnUpdate: time.Now().Unix(),
	}

	if node.CPU != 0 {
		hw.CpuUsage = int(node.CPU * 100)
	}
	if node.Memory.Total > 0 {
		hw.MemTotal = int(node.Memory.Total / 1024 / 1024)
		hw.MemUsage = int(node.Memory.Used / 1024 / 1024)
	}
	// 系统盘所在存储的容量：只统计配置的 system_path（PVE 存储名）；
	// 未配置时不填磁盘数据，避免把 PVE 根分区误当成虚拟机可用空间。
	if storage := strings.TrimSpace(d.Config.SystemPath); storage != "" {
		if total, used, ok := d.storageUsage(storage); ok {
			hw.HddTotal = int(total / 1024 / 1024)
			hw.HddUsage = int(used / 1024 / 1024)
		}
	}
	hw.CpuTotal = int(node.CPUInfo.CPUs)
	hw.CpuModel = node.CPUInfo.Model

	return hw, nil
}

// storageUsage 读取 PVE 存储（system_path）的总量与已用量（字节）。
func (d *ProxmoxDriver) storageUsage(storage string) (int64, int64, bool) {
	client, err := d.apiConn()
	if err != nil {
		return 0, 0, false
	}
	var status struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
		Avail int64 `json:"avail"`
	}
	path := fmt.Sprintf("/nodes/%s/storage/%s/status", d.nodeName(), url.PathEscape(storage))
	if err := client.Get(d.Ctx(""), path, &status); err != nil {
		return 0, 0, false
	}
	if status.Total <= 0 {
		return 0, 0, false
	}
	used := status.Used
	if used <= 0 && status.Avail > 0 {
		used = status.Total - status.Avail
	}
	if used < 0 {
		used = 0
	}
	return status.Total, used, true
}

// ============================================================
// 虚拟机生命周期
// ============================================================

// ScanVMs 扫描并同步虚拟机列表
//
// 与 Python 版 ProxmoxQemu.VMDetect 对齐：使用主机配置的 filter_name
// 作为名称前缀过滤，只纳管带该前缀的虚拟机，
// 避免把平台上他人或模板虚拟机一并纳入本主机管理。
func (d *ProxmoxDriver) ScanVMs() (*model.ZMessage, error) {
	node, err := d.getNode()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDetect", Message: err.Error()}, nil
	}

	vms, err := node.VirtualMachines(d.Ctx(""))
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDetect", Message: err.Error()}, nil
	}

	prefix := d.Config.FilterName
	scanned := 0
	added := 0

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, vm := range vms {
		// 前缀过滤（未配置 filter_name 时不过滤）
		if prefix != "" && !strings.HasPrefix(vm.Name, prefix) {
			continue
		}
		scanned++

		// vm_uuid 统一使用 Proxmox 平台上的真实名称（与 Python 版一致），
		// 并对历史遗留的「下划线变体」键做复用，避免同一台虚拟机
		// 因命名形式不同产生第二条记录。
		vmUUID := d.findExistingVMKeyLocked(vm.Name)
		if vmUUID == "" {
			vmUUID = vm.Name
			cfg := model.NewVMConfig()
			cfg.VmUUID = vmUUID
			if cfg.ExtData == nil {
				cfg.ExtData = make(map[string]interface{})
			}
			cfg.ExtData["vmid"] = float64(vm.VMID)
			d.VmSaving[vmUUID] = cfg
			added++
		} else {
			if d.VmSaving[vmUUID].ExtData == nil {
				d.VmSaving[vmUUID].ExtData = make(map[string]interface{})
			}
			d.VmSaving[vmUUID].ExtData["vmid"] = float64(vm.VMID)
		}
	}

	return &model.ZMessage{
		Success: true, Action: "VMDetect",
		Message: fmt.Sprintf("扫描完成：匹配 %d 台，新增 %d 台", scanned, added),
	}, nil
}

// CreateVM 创建虚拟机
func (d *ProxmoxDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	if cfg == nil {
		return &model.ZMessage{Success: false, Action: "VMCreate", Message: "虚拟机配置为空"}, nil
	}

	node, err := d.getNode()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate", Message: err.Error()}, nil
	}

	vmid, err := d.newVMID()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate", Message: err.Error()}, nil
	}

	ctx := d.Ctx(cfg.VmUUID)

	// 平台上的虚拟机名与内部 vm_uuid 保持一致（不再做 "_"→"-" 转换），
	// 否则创建后扫描会因键形式不同而多出一条重复记录。
	vmName := cfg.VmUUID

	memMB := cfg.MemNum
	if memMB <= 0 {
		memMB = 512
	}
	cpuNum := cfg.CpuNum
	if cpuNum <= 0 {
		cpuNum = 1
	}

	// 系统盘所在的存储（PVE 存储名，如 local / local-lvm）
	storage := "local-lvm"
	if strings.TrimSpace(d.Config.SystemPath) != "" {
		storage = d.Config.SystemPath
	}

	// 缓存 VMID，后续安装系统、开机都需要
	if cfg.ExtData == nil {
		cfg.ExtData = make(map[string]interface{})
	}
	cfg.ExtData["vmid"] = float64(vmid)

	// 填充默认启动项顺序（系统盘 -> 数据盘 -> 光盘）
	if len(cfg.EfiAll) == 0 {
		cfg.EfiAll = buildEfiList(cfg)
	}

	// 构建创建参数
	//
	// 关键项说明（对应 Python ProxmoxQemu.VMCreate）：
	//   bios=ovmf + efidisk0 ：UEFI 启动，Windows/现代 Linux 镜像必需
	//   cpu=host             ：透传宿主 CPU 特性，兼容性与性能最佳
	//   scsihw=virtio-scsi-single
	//   netN                 ：必须带 macaddr，否则路由器 DHCP 静态绑定对不上
	opts := []proxmox.VirtualMachineOption{
		{Name: "name", Value: vmName},
		{Name: "memory", Value: strconv.Itoa(memMB)},
		{Name: "cores", Value: strconv.Itoa(cpuNum)},
		{Name: "sockets", Value: "1"},
		{Name: "cpu", Value: "host"},
		{Name: "ostype", Value: pveOsType(cfg.OsName)},
		{Name: "bios", Value: "ovmf"},
		{Name: "scsihw", Value: "virtio-scsi-single"},
		{Name: "efidisk0", Value: fmt.Sprintf("%s:1,efitype=4m", storage)},
		// 预置一个空光驱：后续挂载 ISO 只是“换盘”，运行中的虚拟机可热生效；
		// 若创建时没有光驱，热添加 IDE 设备必须先关机。
		{Name: "ide2", Value: "none,media=cdrom"},
		// 声卡：没有音频设备时部分桌面应用会直接报错或用不了
		{Name: "audio0", Value: pveAudioDevice},
	}

	// 网卡配置（含 MAC）
	nets := d.netConf(cfg)
	for _, key := range sortedMapKeys(nets) {
		opts = append(opts, proxmox.VirtualMachineOption{Name: key, Value: nets[key]})
	}

	// 启动顺序：系统盘 → 光驱 → 网卡（PXE）。
	// PVE 的配置键是 boot，值为 order=...（写成 order 会被 400 拒绝）。
	bootParts := []string{"ide0", "ide2"}
	bootParts = append(bootParts, sortedMapKeys(nets)...)
	opts = append(opts, proxmox.VirtualMachineOption{
		Name:  "boot",
		Value: "order=" + strings.Join(bootParts, ";"),
	})

	task, err := node.NewVirtualMachine(ctx, vmid, opts...)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "audio") {
		// 老版本 PVE 不支持该声卡型号：去掉 audio0 再试一次，
		// 不能因为一块声卡让整台虚拟机创建失败。
		log.Printf("[proxmox] 当前节点不接受 audio0（%v），去掉声卡后重试", err)
		task, err = node.NewVirtualMachine(ctx, vmid, filterVMOptions(opts, "audio0")...)
	}
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate", Message: "创建虚拟机失败: " + err.Error()}, nil
	}
	if task != nil {
		if err := task.WaitFor(ctx, 120); err != nil {
			return &model.ZMessage{Success: false, Action: "VMCreate", Message: "等待创建任务超时: " + err.Error()}, nil
		}
	}

	// 重新获取 VM 对象（创建任务完成后才有完整配置）
	vm, err := node.VirtualMachine(ctx, vmid)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate", Message: "获取虚拟机对象失败: " + err.Error()}, nil
	}

	// PCI/GPU 直通（hostpciN）
	if len(cfg.PciAll) > 0 {
		gpuOpts := make([]proxmox.VirtualMachineOption, 0, len(cfg.PciAll))
		idx := 0
		for _, key := range sortedMapKeys(cfg.PciAll) {
			pci := cfg.PciAll[key]
			if strings.TrimSpace(pci.GpuUUID) == "" {
				continue
			}
			gpuOpts = append(gpuOpts, proxmox.VirtualMachineOption{
				Name:  fmt.Sprintf("hostpci%d", idx),
				Value: fmt.Sprintf("%s,pcie=1", pci.GpuUUID),
			})
			idx++
		}
		if len(gpuOpts) > 0 {
			if _, err := vm.Config(ctx, gpuOpts...); err != nil {
				// 直通失败不阻断创建，但必须暴露出来
				return &model.ZMessage{
					Success: false, Action: "VMCreate",
					Message: "虚拟机已创建，但 PCI 直通配置失败: " + err.Error(),
				}, nil
			}
		}
	}

	// 安装系统：把模板镜像导入/复制为系统盘并挂载
	// 缺少这一步虚拟机没有可启动的系统盘
	installResult, err := d.installSystem(cfg)
	if err != nil {
		return &model.ZMessage{
			Success: false, Action: "VMCreate",
			Message: "系统镜像安装失败: " + err.Error(),
		}, nil
	}
	if installResult != nil && !installResult.Success {
		return &model.ZMessage{
			Success: false, Action: "VMCreate",
			Message: "虚拟机已创建，但系统安装失败: " + installResult.Message,
		}, nil
	}

	// 配置 VNC 直通端口（创建时立即写入，无需重启）
	d.applyVNCPort(cfg, vm, vmid)

	// 启动虚拟机
	// 注意：此处 vm_saving 尚未写入，不能走 PowerControl（其内部会查表失败）
	if _, err := vm.Start(ctx); err != nil {
		return &model.ZMessage{
			Success: false, Action: "VMCreate",
			Message: "虚拟机创建成功但启动失败: " + err.Error(),
		}, nil
	}

	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMCreate", Message: "虚拟机创建成功"}, nil
}

// applyVNCPort 为虚拟机写入 VNC 直通参数（args=-vnc 0.0.0.0:<id>）
//
// PVE 默认的 VNC 由 pveproxy 代理且需要租约，直接指定 -vnc 参数
// 可以让 VNC 端口固定可预测，便于前端直连。
func (d *ProxmoxDriver) applyVNCPort(cfg *model.VMConfig, vm *proxmox.VirtualMachine, vmid int) {
	if cfg == nil || vm == nil {
		return
	}
	ctx := d.Ctx(cfg.VmUUID)

	vncPort := 5900 + vmid
	if p, err := strconv.Atoi(strings.TrimSpace(cfg.VcPort)); err == nil && p > 0 {
		vncPort = p
	}
	vncID := vncPort - 5900
	vncArg := fmt.Sprintf("-vnc 0.0.0.0:%d", vncID)

	currentArgs := ""
	if vm.VirtualMachineConfig != nil {
		currentArgs = strings.TrimSpace(vm.VirtualMachineConfig.Args)
	}

	newArgs := strings.TrimSpace(vncRe.ReplaceAllString(currentArgs, ""))
	if newArgs == "" {
		newArgs = vncArg
	} else {
		newArgs = newArgs + " " + vncArg
	}

	if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{Name: "args", Value: newArgs}); err != nil {
		// VNC 写入失败不阻断创建流程
		return
	}
	cfg.VcPort = strconv.Itoa(vncPort)
}

var vncRe = regexp.MustCompile(`-vnc\s+\S+`)

// pveOsType 根据镜像名推断 PVE 的 ostype（对应 Python 的判断逻辑）
func pveOsType(osName string) string {
	lower := strings.ToLower(osName)
	if strings.Contains(lower, "win") {
		if strings.Contains(lower, "win11") || strings.Contains(lower, "win-11") {
			return "win11"
		}
		return "win10"
	}
	return "l26"
}

// sortedMapKeys 返回 map 的有序键（保证 netX / hostpciX 编号稳定）
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReinstallVM 重装虚拟机系统
//
// 对应 Python ProxmoxQemu.VMSetups 的重装语义：
// 停止虚拟机 → 删除旧系统盘 → 重新导入/复制模板镜像 → 启动。
func (d *ProxmoxDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	if cfg == nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall", Message: "虚拟机配置为空"}, nil
	}
	if strings.TrimSpace(cfg.OsName) == "" {
		return &model.ZMessage{Success: false, Action: "VMReinstall", Message: "未指定系统镜像"}, nil
	}

	vm, err := d.getVM(cfg.VmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall", Message: err.Error()}, nil
	}
	ctx := d.Ctx(cfg.VmUUID)

	// 1. 停止虚拟机：优先 PVE 软关机（ACPI），超时才强制断电
	if vm.IsRunning() {
		if err := d.StopGracefully(cfg.VmUUID, gracefulStopWait); err != nil {
			return &model.ZMessage{
				Success: false, Action: "VMReinstall",
				Message: "停止虚拟机失败: " + err.Error(),
			}, nil
		}
		// StopGracefully 内部会写中间态 ON_STOP，这里已确认断电，落为稳定态
		d.SetVMPowerState(cfg.VmUUID, model.VMPowerStopped)
	}

	// 2. 卸载并删除旧系统盘（ide0）；旧盘不存在时忽略错误
	if _, err := vm.Config(ctx,
		proxmox.VirtualMachineOption{Name: "delete", Value: "ide0"}); err != nil {
		// 删除失败通常意味着本来就没有 ide0，继续重装即可
		_, _ = vm.Config(ctx, proxmox.VirtualMachineOption{Name: "ide0", Value: "none"})
	}

	// 3. 重新安装系统（导入/复制镜像并挂载）
	if len(cfg.EfiAll) == 0 {
		cfg.EfiAll = buildEfiList(cfg)
	}
	installResult, err := d.installSystem(cfg)
	if err != nil {
		return &model.ZMessage{
			Success: false, Action: "VMReinstall",
			Message: "系统重装失败: " + err.Error(),
		}, nil
	}
	if installResult != nil && !installResult.Success {
		return &model.ZMessage{
			Success: false, Action: "VMReinstall",
			Message: "系统重装失败: " + installResult.Message,
		}, nil
	}

	// 4. 重新启动
	if _, err := vm.Start(ctx); err != nil {
		return &model.ZMessage{
			Success: false, Action: "VMReinstall",
			Message: "系统已重装但启动失败: " + err.Error(),
		}, nil
	}
	d.SetVMPowerState(cfg.VmUUID, model.VMPowerStarted)

	return &model.ZMessage{Success: true, Action: "VMReinstall", Message: "系统重装成功"}, nil
}

// UpdateVM 更新虚拟机配置
func (d *ProxmoxDriver) UpdateVM(cfg *model.VMConfig, oldCfg *model.VMConfig) (*model.ZMessage, error) {
	ctx := d.Ctx(cfg.VmUUID)

	vm, err := d.getVM(cfg.VmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMUpdate", Message: err.Error()}, nil
	}

	var opts []proxmox.VirtualMachineOption

	if cfg.CpuNum > 0 && (oldCfg == nil || cfg.CpuNum != oldCfg.CpuNum) {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "cores", Value: strconv.Itoa(cfg.CpuNum)})
	}
	if cfg.MemNum > 0 && (oldCfg == nil || cfg.MemNum != oldCfg.MemNum) {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "memory", Value: strconv.Itoa(cfg.MemNum)})
	}

	// 网卡同步：MAC/IP 变更后必须重新下发，否则平台侧网卡与
	// 路由器上的 DHCP 静态绑定会不一致，虚拟机拿不到预期地址。
	newNets := d.netConf(cfg)
	oldNets := map[string]string{}
	if oldCfg != nil {
		oldNets = d.netConf(oldCfg)
	}
	if !stringMapEqual(newNets, oldNets) {
		for _, key := range sortedMapKeys(newNets) {
			opts = append(opts, proxmox.VirtualMachineOption{Name: key, Value: newNets[key]})
		}
		// 网卡数量减少时删除多余的旧网卡
		for _, key := range sortedMapKeys(oldNets) {
			if _, ok := newNets[key]; !ok {
				opts = append(opts, proxmox.VirtualMachineOption{Name: "delete", Value: key})
			}
		}
	}

	if len(opts) > 0 {
		if _, err := vm.Config(ctx, opts...); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate", Message: err.Error()}, nil
		}
	}

	// 磁盘扩容（仅在容量变大时执行，PVE 不支持缩容）
	if oldCfg != nil && cfg.HddNum > oldCfg.HddNum && cfg.HddNum > 0 {
		sizeGB := cfg.HddNum / 1024
		if cfg.HddNum%1024 != 0 {
			sizeGB++
		}
		if sizeGB < 1 {
			sizeGB = 1
		}
		if _, err := vm.ResizeDisk(ctx, "ide0", fmt.Sprintf("%dG", sizeGB)); err != nil {
			return &model.ZMessage{
				Success: false, Action: "VMUpdate",
				Message: "系统盘扩容失败: " + err.Error(),
			}, nil
		}
	}

	// 旧虚拟机没有声卡：补一块，否则部分桌面应用（会议/媒体）无法工作。
	// 已在配置里存在且驱动正确时不重复下发，避免每次更新都多一次 API 调用。
	if vmid, vmidErr := d.getVMID(cfg.VmUUID); vmidErr == nil {
		if cfgMap, cfgErr := d.pveVMConfigMap(vmid); cfgErr == nil {
			audio := cfgMap["audio0"]
			needFix := audio == ""
			// 历史遗留：spice 音频在没有 -spice 显示时会让 QEMU 直接退出
			// （kvm: Cannot use spice audio without -spice）
			if !needFix && strings.Contains(audio, "driver=spice") &&
				!strings.Contains(cfgMap["vga"], "spice") {
				log.Printf("[proxmox] 修正声卡配置：spice 音频需要 SPICE 显示，改为 none（原 %s）", audio)
				needFix = true
			}
			if needFix {
				if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
					Name:  "audio0",
					Value: pveAudioDevice,
				}); err != nil {
					log.Printf("[proxmox] 补充声卡失败: %v", err)
				}
			}
		}
	}

	// 启动顺序与磁盘/光盘/网卡保持一致。
	// 「启动顺序管理」保存只改了本地 efi_all，此前不会落到 PVE 的 boot 参数，
	// 表现为「界面上有光盘，平台启动顺序里没有」。
	if boot := d.proxmoxBootOrder(cfg); boot != "" {
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "boot",
			Value: boot,
		}); err != nil {
			log.Printf("[proxmox] 更新启动顺序失败（%s）: %v", boot, err)
		}
	}

	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMUpdate", Message: "虚拟机配置已更新"}, nil
}

// DeleteVM 删除虚拟机
//
// 删除语义是「强制断电 + 直接删除」：不做 ACPI 软关机协商，避免 guest
// 不响应关机请求时把删除一起拖死。PVE 的 destroy 只接受停机实例
// （运行中的会直接报错），所以先发 status/stop 强杀 qemu，确认平台状态
// 变为 stopped 后立刻删除。
func (d *ProxmoxDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete", Message: err.Error()}, nil
	}

	if vm.Status != proxmox.StatusVirtualMachineStopped {
		if !d.forceStopVM(ctx, vmUUID, vm) {
			return &model.ZMessage{Success: false, Action: "VMDelete",
				Message: "删除前强制关机失败: 虚拟机未在 " + forceStopWait.String() + " 内关机"}, nil
		}
	}

	task, err := vm.Delete(ctx)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete", Message: err.Error()}, nil
	}

	if err := task.WaitFor(ctx, 60); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete", Message: "等待删除任务超时: " + err.Error()}, nil
	}

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDelete", Message: "虚拟机已删除"}, nil
}

// forceStopVM 直接强杀 PVE 实例（status/stop，等价 qm stop）并等待状态收敛。
//
// 与 StopGracefully 的区别：不走 ACPI 协商，qemu 进程被直接终止，不依赖
// guest 配合。返回 false 表示请求失败或超时后平台仍报告运行中。
func (d *ProxmoxDriver) forceStopVM(ctx context.Context, vmUUID string, vm *proxmox.VirtualMachine) bool {
	task, err := vm.Stop(ctx)
	if err != nil {
		// 状态缓存滞后时实例可能已经停机，PVE 会回 "VM xxx is not running"，
		// 这种情况按已关机处理，删除可直接继续。
		if strings.Contains(strings.ToLower(err.Error()), "not running") {
			return true
		}
		log.Printf("[proxmox] %d 强制关机请求失败: %v", vm.VMID, err)
		return false
	}
	if task != nil {
		// 等待超时不等于关机失败，最终以平台实际状态为准。
		if err := task.WaitFor(ctx, int(forceStopWait/time.Second)); err != nil {
			log.Printf("[proxmox] %d 强制关机任务未在 %s 内结束: %v", vm.VMID, forceStopWait, err)
		}
	}
	return d.waitPowerStopped(d, vmUUID, forceStopWait)
}

// SupportsGracefulShutdown PVE 自带软关机：/status/shutdown 由平台向 guest
// 下发 ACPI 关机请求，不依赖 guest 内的 CloudInit 代理。
func (d *ProxmoxDriver) SupportsGracefulShutdown() bool { return true }

// PowerControl 电源控制
func (d *ProxmoxDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: err.Error()}, nil
	}

	var task *proxmox.Task
	var taskErr error

	switch action {
	case model.VMPowerSStart:
		task, taskErr = vm.Start(ctx)
	case model.VMPowerSClose:
		// 走 PVE 的 POST /status/shutdown（qm shutdown）：由平台向 guest
		// 下发 ACPI 关机，这是 PVE 的软关机通道。CloudInit 代理只是兜底，
		// 不再是发起关机的前提。
		task, taskErr = vm.Shutdown(ctx)
	case model.VMPowerHClose:
		task, taskErr = vm.Stop(ctx)
	case model.VMPowerSReset, model.VMPowerHReset:
		task, taskErr = vm.Reset(ctx)
	case model.VMPowerAPause:
		task, taskErr = vm.Pause(ctx)
	case model.VMPowerAWaked:
		task, taskErr = vm.Resume(ctx)
	default:
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: "不支持的电源操作"}, nil
	}

	if taskErr != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: taskErr.Error()}, nil
	}

	if task != nil {
		// 软关机是「请求」而不是「结果」：PVE 的 shutdown 任务要等 guest
		// 真正断电才结束（qm shutdown 默认 60s），同步等待会把正常关机
		// 误报成失败。这里只做 5 秒的即时错误探测，任务超时视为平台已受理，
		// 最终状态交给 SoftPowerMonitor 收敛。
		waitSeconds := 30
		if action == model.VMPowerSClose {
			waitSeconds = 5
		}
		if err := task.WaitFor(ctx, waitSeconds); err != nil {
			if action != model.VMPowerSClose || !errors.Is(err, proxmox.ErrTimeout) {
				return &model.ZMessage{Success: false, Action: "VMPowers", Message: "等待电源操作失败: " + err.Error()}, nil
			}
			log.Printf("[proxmox] %s 软关机已受理，等待 guest 完成关机", vmUUID)
		}
	}

	// 更新内存状态
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		if newState, ok2 := PowerActionMap[action]; ok2 {
			cfg.VmFlag = newState
		}
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMPowers", Message: "电源操作已执行"}, nil
}

// ============================================================
// VNC / 远程控制
// ============================================================

// GetVNCInfo 获取VNC连接信息
func (d *ProxmoxDriver) GetVNCInfo(vmUUID string, ipAddr string) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VCRemote", Message: err.Error()}, nil
	}

	if vm.Status != "running" {
		return &model.ZMessage{Success: false, Action: "VCRemote", Message: "虚拟机未运行，无法打开控制台"}, nil
	}

	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VCRemote", Message: err.Error()}, nil
	}

	// vncwebsocket 除了 vncticket 还要求请求本身已登录，
	// 否则会返回 401 No ticket。
	apiTicket, apiCSRF, ticketErr := d.pveAPITicket(ctx)
	if ticketErr != nil {
		return &model.ZMessage{Success: false, Action: "VCRemote", Message: ticketErr.Error()}, nil
	}

	// 预先创建 web 用 vnc 会话（等价于 qm vncproxy --websocket）。
	// 只有带 websocket=1 时 PVE 才会生成 VNC 密码并拼在 ticket 最前面
	// （PVE::API2::Qemu：$ticket = "${password}:${ticket}"），
	// 否则拿到的是纯 ticket，noVNC 在 VNC 认证阶段会被拒。
	port, vncTicket, password, sessErr := d.pveVNCSession(vmid)
	if sessErr != nil {
		return &model.ZMessage{Success: false, Action: "VCRemote", Message: sessErr.Error()}, nil
	}

	// PVE 的 vncproxy 只在节点本地开一条 VNC 通道，必须经
	//   wss://<node>:8006/api2/json/nodes/<node>/qemu/<vmid>/vncwebsocket?port=&vncticket=
	// 访问，直接 TCP 连过去既连不上也不需要密码。
	return &model.ZMessage{
		Success: true,
		Action:  "VCRemote",
		Message: "VNC信息获取成功",
		Results: map[string]interface{}{
			"type":     "proxmox",
			"host":     d.proxmoxHost(),
			"api_host": d.proxmoxHostPort(),
			"node":     d.nodeName(),
			"vmid":     int(vmid),
			"port":     port,
			"ticket":   vncTicket,
			"password": password,
			"cookie":   apiTicket,
			"csrf":     apiCSRF,
		},
	}, nil
}

// GetScreenshot 获取虚拟机截图（纯 base64，不含 data: 前缀）。
//
// PVE 没有截图 REST API，官方手段是让 QEMU 把当前画面 dump 成 PPM：
//
//	echo 'screendump /tmp/x.ppm' | qm monitor <vmid>
//
// 因此需要 SSH 到 PVE 节点（主机配置的 server_addr / server_user / server_pass）。
// 原实现走 qemu-guest-agent 在虚拟机内执行 ImageMagick，要求来宾系统装有
// X11 与 imagemagick，实际几乎不可用，表现为「截图功能不可用」。
func (d *ProxmoxDriver) GetScreenshot(vmUUID string) (string, error) {
	vm, err := d.getVM(vmUUID)
	if err != nil {
		return "", fmt.Errorf("获取虚拟机失败: %w", err)
	}
	if vm.Status != "running" {
		return "", fmt.Errorf("虚拟机未运行，无法截图")
	}

	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return "", fmt.Errorf("获取 VM ID 失败: %w", err)
	}

	sshCfg := sshConfigFromHS(d.Config)
	if strings.TrimSpace(sshCfg.Host) == "" {
		return "", fmt.Errorf("主机未配置 SSH 地址，无法截取 Proxmox 画面")
	}
	sshCfg.Timeout = 30 * time.Second

	// screendump 由 QEMU 异步写文件，一次抓取可能只读到头部和少量像素
	// （实测拿到 4080 字节而不是完整的 3MB），因此失败时重试一次。
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		data, err := d.captureProxmoxScreen(sshCfg, vmid)
		if err == nil {
			return data, nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	return "", lastErr
}

// captureProxmoxScreen 通过 SSH 让 QEMU dump 一帧 PPM，读回后转成 PNG base64。
func (d *ProxmoxDriver) captureProxmoxScreen(sshCfg SSHExecConfig, vmid int64) (string, error) {
	// 优先写 tmpfs（/dev/shm）：screendump 在普通磁盘上可能留下未完成的
	// 部分文件，而 tmpfs 写入更快、无回写延迟。
	remote := fmt.Sprintf("/dev/shm/openidcs-screen-%d-%d.ppm", vmid, time.Now().UnixNano())

	// 必须等文件大小稳定，而不是「非空」：screendump 一开始就会创建文件，
	// 此时内容只有头部，读到的是残缺 PPM。
	// dump 与读回合并成一次 SSH 会话，省掉一次握手。
	// monitor 会话要保持到 QEMU 写完文件：命令发完立即关闭管道会让
	// screendump 只留下头部和少量像素；之后再等文件大小稳定。
	dumpCmd := fmt.Sprintf(`rm -f %[1]s
{ echo 'screendump %[1]s'; sleep 1; } | qm monitor %[2]d >/dev/null 2>&1
prev=-1; same=0
for i in $(seq 1 40); do
  cur=$(stat -c %%s %[1]s 2>/dev/null || echo 0)
  if [ "$cur" -gt 0 ] && [ "$cur" = "$prev" ]; then same=$((same+1)); else same=0; fi
  if [ "$same" -ge 2 ]; then break; fi
  prev=$cur
  sleep 0.2
done
if [ -s %[1]s ]; then
  echo __OK__ >&2
  base64 -w0 %[1]s 2>/dev/null || base64 %[1]s
  rm -f %[1]s
else
  echo __FAIL__ >&2
fi`, remote, vmid)

	res, err := RunSSHCommand(sshCfg, dumpCmd)
	if err != nil {
		return "", fmt.Errorf("SSH 执行截图命令失败: %w", err)
	}
	if !strings.Contains(res.Stderr, "__OK__") {
		msg := strings.TrimSpace(res.Stderr)
		return "", fmt.Errorf("Proxmox 未生成截图文件（请确认节点可 SSH 且虚拟机有画面）: %s", msg)
	}

	b64 := strings.Join(strings.Fields(res.Stdout), "")
	if b64 == "" {
		return "", fmt.Errorf("截图文件为空")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("截图数据解码失败: %w", err)
	}
	out, err := ppmBytesToBase64PNG(raw)
	if err != nil {
		// 附带实际字节数，便于区分「QEMU 没写完」与「读取被截断」
		return "", fmt.Errorf("%w（b64=%d, raw=%d）", err, len(b64), len(raw))
	}
	return out, nil
}

// ============================================================
// 备份管理
// ============================================================

// pveBackupContent PVE 存储内容接口返回的备份条目
type pveBackupContent struct {
	VolID string `json:"volid"`
	CTime int64  `json:"ctime"`
	Size  int64  `json:"size"`
	Notes string `json:"notes"`
}

// SyncBackups 从 PVE 拉取该虚拟机的备份文件列表并覆盖内存记录。
//
// 不能靠时间戳拼文件名：vzdump 的实际命名、压缩后缀、以及管理员手工
// 创建的备份都无法预测。以存储内容为准，列表才与 PVE 一致。
func (d *ProxmoxDriver) SyncBackups(vmUUID string) {
	ctx := d.Ctx(vmUUID)

	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		// 拿不到 vmid 就无法判断哪些备份属于这台虚拟机：
		// 清空本地列表，避免残留其它虚拟机的备份记录被当成自己的。
		d.mu.Lock()
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			cfg.Backups = nil
		}
		d.mu.Unlock()
		return
	}
	client, err := d.apiConn()
	if err != nil {
		return
	}
	storage := strings.TrimSpace(d.Config.BackupPath)
	if storage == "" {
		storage = "local"
	}

	var items []pveBackupContent
	path := fmt.Sprintf("/nodes/%s/storage/%s/content?content=backup",
		d.nodeName(), url.PathEscape(storage))
	if err := client.Get(ctx, path, &items); err != nil {
		log.Printf("[proxmox] 拉取备份列表失败（storage=%s）: %v", storage, err)
		return
	}

	prefix := fmt.Sprintf("vzdump-qemu-%d-", vmid)
	list := make([]model.VMBackup, 0, len(items))
	for _, it := range items {
		name := it.VolID
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		backup := model.VMBackup{
			BackupName: name,
			BackupTime: time.Unix(it.CTime, 0),
			BackupTips: it.Notes,
		}
		list = append(list, backup)
	}
	// 新的在前
	sort.Slice(list, func(i, j int) bool { return list[i].BackupTime.After(list[j].BackupTime) })

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.Backups = list
	}
	d.mu.Unlock()
}

// CreateBackup 创建备份
func (d *ProxmoxDriver) CreateBackup(vmUUID string, tips string) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	node, err := d.getNode()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup", Message: err.Error()}, nil
	}

	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup", Message: err.Error()}, nil
	}

	storage := "local"
	if d.Config.BackupPath != "" {
		storage = d.Config.BackupPath
	}

	task, err := node.Vzdump(ctx, &proxmox.VirtualMachineBackupOptions{
		VMID:     uint64(vmid),
		Storage:  storage,
		Compress: proxmox.VirtualMachineBackupCompressZstd,
	})
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup", Message: err.Error()}, nil
	}

	// 备份是分钟级任务，大磁盘可能超过 10 分钟：
	// 这里由后台协程等待，放宽到 24 小时，避免把「还在跑」误判成「超时失败」。
	if err := task.WaitFor(ctx, 86400); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup", Message: "备份失败: " + err.Error()}, nil
	}

	// 以 PVE 上的实际备份文件为准刷新列表（不再靠时间戳拼文件名）
	d.SyncBackups(vmUUID)

	return &model.ZMessage{Success: true, Action: "VMBackup", Message: "备份创建成功"}, nil
}

// RestoreBackup 已迁移至 proxmox_ext.go（真实实现：POST /nodes/{node}/qemu 恢复任务）。

// DeleteBackup 删除备份
func (d *ProxmoxDriver) DeleteBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	storage := "local"
	if d.Config.BackupPath != "" {
		storage = d.Config.BackupPath
	}

	node, err := d.getNode()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "RMBackup", Message: err.Error()}, nil
	}
	bkpStorage, err := node.Storage(ctx, storage)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "RMBackup", Message: err.Error()}, nil
	}
	volID := fmt.Sprintf("%s:backup/%s", storage, backupName)
	delTask, err := bkpStorage.DeleteContent(ctx, volID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "RMBackup", Message: err.Error()}, nil
	}
	if delTask != nil {
		_ = delTask.WaitFor(ctx, 60)
	}

	// 从备份列表中移除
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		newBackups := cfg.Backups[:0]
		for _, b := range cfg.Backups {
			if b.BackupName != backupName {
				newBackups = append(newBackups, b)
			}
		}
		cfg.Backups = newBackups
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "RMBackup", Message: "备份删除成功"}, nil
}

// ScanBackups 扫描备份文件
func (d *ProxmoxDriver) ScanBackups() (*model.ZMessage, error) {
	storage := "local"
	if d.Config.BackupPath != "" {
		storage = d.Config.BackupPath
	}

	node, err := d.getNode()
	if err != nil {
		return &model.ZMessage{Success: false, Action: "LDBackup", Message: err.Error()}, nil
	}
	bkpStorage, err := node.Storage(d.Ctx(""), storage)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "LDBackup", Message: err.Error()}, nil
	}
	contents, err := bkpStorage.GetContent(d.Ctx(""))
	if err != nil {
		return &model.ZMessage{Success: false, Action: "LDBackup", Message: err.Error()}, nil
	}

	// 按vmid分组备份（通过volid中包含backup判断）
	backupsByVM := make(map[int64][]*proxmox.StorageContent)
	for _, c := range contents {
		if strings.Contains(c.Volid, "backup") {
			backupsByVM[int64(c.VMID)] = append(backupsByVM[int64(c.VMID)], c)
		}
	}

	// 更新各VM的备份列表
	d.mu.Lock()
	for vmUUID, cfg := range d.VmSaving {
		vmid, _ := d.getVMIDNoLock(vmUUID)
		if vmid == 0 {
			continue
		}
		if bkps, ok := backupsByVM[vmid]; ok {
			cfg.Backups = make([]model.VMBackup, 0, len(bkps))
			for _, b := range bkps {
				cfg.Backups = append(cfg.Backups, model.VMBackup{
					BackupName: b.Volid,
					BackupTime: time.Unix(int64(b.Ctime), 0),
					BackupTips: b.Notes,
				})
			}
		}
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "LDBackup", Message: "备份扫描完成"}, nil
}

// getVMIDNoLock 不加锁获取VMID（内部使用，调用方已持有锁）
func (d *ProxmoxDriver) getVMIDNoLock(vmUUID string) (int64, error) {
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		if vmData, ok2 := cfg.ExtData["vmid"]; ok2 {
			if id, ok3 := vmData.(float64); ok3 && id > 0 {
				return int64(id), nil
			}
		}
	}
	return 0, fmt.Errorf("未找到VMID")
}

// ============================================================
// PCI / GPU 直通
// ============================================================

// GetGPUList / GetPCIList 已迁移至 proxmox_ext.go（SSH lspci -nn 真实枚举）。

// SetupPCI PCI 设备直通操作
func (d *ProxmoxDriver) SetupPCI(vmUUID string, cfg *model.VFConfig, pciKey string, add bool) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "PCISetup", Message: err.Error()}, nil
	}

	if add {
		// 添加PCI 设备
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "hostpci0",
			Value: pciKey + ",pcie=1",
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "PCISetup", Message: err.Error()}, nil
		}
		// 更新内存配置
		d.mu.Lock()
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			vmCfg.PciAll[pciKey] = *cfg
		}
		d.mu.Unlock()
	} else {
		// 移除PCI 设备
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "delete",
			Value: "hostpci0",
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "PCISetup", Message: err.Error()}, nil
		}
		d.mu.Lock()
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			delete(vmCfg.PciAll, pciKey)
		}
		d.mu.Unlock()
	}

	action := "添加"
	if !add {
		action = "移除"
	}
	return &model.ZMessage{Success: true, Action: "PCISetup", Message: "PCI 设备" + action + "成功"}, nil
}

// ============================================================
// USB 直通
// ============================================================

// GetUSBList 已迁移至 proxmox_ext.go（SSH lsusb 真实枚举）。

// SetupUSB USB 设备直通操作
func (d *ProxmoxDriver) SetupUSB(vmUUID string, info *model.USBInfos, usbKey string, add bool) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "USBSetup", Message: err.Error()}, nil
	}

	if add {
		usbVal := fmt.Sprintf("host=%s:%s", info.VidUUID, info.PidUUID)
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "usb0",
			Value: usbVal,
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "USBSetup", Message: err.Error()}, nil
		}
		d.mu.Lock()
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			vmCfg.UsbAll[usbKey] = *info
		}
		d.mu.Unlock()
	} else {
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "delete",
			Value: "usb0",
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "USBSetup", Message: err.Error()}, nil
		}
		d.mu.Lock()
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			delete(vmCfg.UsbAll, usbKey)
		}
		d.mu.Unlock()
	}

	action := "添加"
	if !add {
		action = "移除"
	}
	return &model.ZMessage{Success: true, Action: "USBSetup", Message: "USB 设备" + action + "成功"}, nil
}

// ============================================================
// 磁盘管理
// ============================================================

// MountHDD 挂载/卸载数据盘
func (d *ProxmoxDriver) MountHDD(vmUUID string, hdd *model.SDConfig, mount bool) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "HDDMount", Message: err.Error()}, nil
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "HDDMount", Message: err.Error()}, nil
	}

	storage := "local-lvm"
	if d.Config.ExternPath != "" {
		storage = d.Config.ExternPath
	}

	if mount {
		// 创建并挂载数据盘：先找一个空闲的 scsi 位（系统盘通常占 scsi0）
		sizeGB := hdd.HddSize / 1024
		if sizeGB <= 0 {
			sizeGB = 10
		}

		slot := hdd.HddScsi
		if slot == "" {
			cfgMap, _ := d.pveVMConfigMap(vmid)
			for i := 1; i <= 30; i++ {
				candidate := fmt.Sprintf("scsi%d", i)
				if _, used := cfgMap[candidate]; !used {
					slot = candidate
					break
				}
			}
			if slot == "" {
				slot = "scsi1"
			}
		}

		diskVal := fmt.Sprintf("%s:%d,format=qcow2", storage, sizeGB)
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  slot,
			Value: diskVal,
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "HDDMount", Message: err.Error()}, nil
		}

		// 回读卷标识，供后续删除/移交精确定位
		hdd.HddFlag = 1
		hdd.HddScsi = slot
		if cfgMap, err := d.pveVMConfigMap(vmid); err == nil {
			if v, ok := cfgMap[slot]; ok {
				hdd.HddPath = pveVolumeOf(v)
			}
		}
		d.mu.Lock()
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			vmCfg.HddAll[hdd.HddName] = *hdd
		}
		d.mu.Unlock()
	} else {
		// 卸载数据盘：优先使用记录的设备位，避免误删其它磁盘
		slot := hdd.HddScsi
		if slot == "" {
			cfgMap, _ := d.pveVMConfigMap(vmid)
			if key, _ := pveFindDiskKey(cfgMap, hdd); key != "" && !strings.HasPrefix(key, "unused") {
				slot = key
			}
		}
		if slot != "" {
			if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
				Name:  "delete",
				Value: slot,
			}); err != nil {
				return &model.ZMessage{Success: false, Action: "HDDMount", Message: err.Error()}, nil
			}
		}
		d.mu.Lock()
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			if h, exists := vmCfg.HddAll[hdd.HddName]; exists {
				h.HddFlag = 0
				h.HddScsi = ""
				vmCfg.HddAll[hdd.HddName] = h
			}
		}
		d.mu.Unlock()
	}

	action := "挂载"
	if !mount {
		action = "卸载"
	}
	return &model.ZMessage{Success: true, Action: "HDDMount", Message: "磁盘" + action + "成功"}, nil
}

// MountISO 挂载/卸载ISO
func (d *ProxmoxDriver) MountISO(vmUUID string, iso *model.IMConfig, mount bool) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "ISOMount", Message: err.Error()}, nil
	}

	if mount {
		volume := proxmoxISOVolume(d.Config.DvdromPath, iso)
		isoVal := volume + ",media=cdrom"
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "ide2",
			Value: isoVal,
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: fmt.Sprintf("挂载ISO失败: %v（卷: %s）", err, isoVal)}, nil
		}

		// 回读校验：PVE 接受请求不等于配置真的落盘（曾出现返回成功、
		// 但 ide2 仍是 none 的情况），把实际值暴露出来，避免「假成功」。
		if vmid, vmidErr := d.getVMID(vmUUID); vmidErr == nil {
			want := filepath.Base(volume)
			var got string
			for attempt := 0; attempt < 3; attempt++ {
				cfgMap, cfgErr := d.pveVMConfigMap(vmid)
				if cfgErr != nil {
					break
				}
				got = cfgMap["ide2"]
				if strings.Contains(got, want) {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			if got != "" && !strings.Contains(got, want) {
				return &model.ZMessage{Success: false, Action: "ISOMount",
					Message: fmt.Sprintf("挂载ISO未生效：期望 %s，实际 ide2=%q（请确认该 ISO 存在于 %s 存储）",
						volume, got, strings.TrimSuffix(d.Config.DvdromPath, ":iso"))}, nil
			}
		}

		d.mu.Lock()
		var bootOrder string
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			// IsoAll 从未挂载过时为 nil，直接赋值会 panic
			if vmCfg.IsoAll == nil {
				vmCfg.IsoAll = make(map[string]model.IMConfig)
			}
			vmCfg.IsoAll[iso.IsoName] = *iso
			bootOrder = d.proxmoxBootOrder(vmCfg)
		}
		d.mu.Unlock()

		// 启动顺序要和“有光盘”这件事保持一致，否则挂上 ISO 也引导不到。
		// PVE 的配置键是 boot，值为 order=...（写成 name=order 会被 400 拒绝）。
		if bootOrder != "" {
			if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
				Name:  "boot",
				Value: bootOrder,
			}); err != nil {
				log.Printf("[proxmox] 更新启动顺序失败（%s）: %v", bootOrder, err)
			} else if vmid, vmidErr := d.getVMID(vmUUID); vmidErr == nil {
				if cfgMap, cfgErr := d.pveVMConfigMap(vmid); cfgErr == nil {
					log.Printf("[proxmox] 光盘已挂载：ide2=%s boot=%s", cfgMap["ide2"], cfgMap["boot"])
				}
			}
		}
	} else {
		if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name:  "ide2",
			Value: "none,media=cdrom",
		}); err != nil {
			return &model.ZMessage{Success: false, Action: "ISOMount", Message: err.Error()}, nil
		}
		d.mu.Lock()
		var bootOrder string
		if vmCfg, ok := d.VmSaving[vmUUID]; ok {
			delete(vmCfg.IsoAll, iso.IsoName)
			bootOrder = d.proxmoxBootOrder(vmCfg)
		}
		d.mu.Unlock()

		if bootOrder != "" {
			if _, err := vm.Config(ctx, proxmox.VirtualMachineOption{
				Name:  "boot",
				Value: bootOrder,
			}); err != nil {
				log.Printf("[proxmox] 更新启动顺序失败（%s）: %v", bootOrder, err)
			}
		}
	}

	action := "挂载"
	if !mount {
		action = "卸载"
	}
	return &model.ZMessage{Success: true, Action: "ISOMount", Message: "ISO" + action + "成功"}, nil
}

// proxmoxISOVolume 构造 PVE 的 ISO 卷 ID（形如 local:iso/foo.iso）。
//
// 主机配置里的 dvdrom_path 常见两种写法：
//   - "local"       → 需要补成 local:iso
//   - "local:iso"   → 已经是 storage:content 形式
//
// 原实现无条件拼 "%s:iso/%s"，当配置为 "local:iso" 时会得到
// "local:iso:iso/foo.iso" 这种非法卷 ID，PVE 直接拒绝，
// 表现为「点了挂载但什么也没发生」。
func proxmoxISOVolume(storageCfg string, iso *model.IMConfig) string {
	// 驱动/前端可能直接给出完整卷 ID，此时原样使用
	for _, candidate := range []string{iso.IsoPath, iso.IsoFile} {
		c := strings.TrimSpace(candidate)
		if c == "" {
			continue
		}
		if strings.Contains(c, ":") && strings.Contains(c, "/") {
			return strings.TrimSuffix(c, ",media=cdrom")
		}
	}

	file := strings.TrimSpace(iso.IsoFile)
	if file == "" {
		file = strings.TrimSpace(iso.IsoPath)
	}
	file = strings.TrimLeft(file, "/")
	// 前端可能只给 "iso/foo.iso"，避免拼成 iso/iso
	file = strings.TrimPrefix(file, "iso/")

	storage := strings.TrimSpace(storageCfg)
	if storage == "" {
		storage = "local"
	}
	if !strings.Contains(storage, ":") {
		storage += ":iso"
	}
	return storage + "/" + file
}

// ============================================================
// 密码设置
// ============================================================

// SetPassword 设置虚拟机密码（通过QEMU guest agent）
func (d *ProxmoxDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	ctx := d.Ctx(vmUUID)

	vm, err := d.getVM(vmUUID)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "Password", Message: err.Error()}, nil
	}

	// 通过QEMU guest agent设置密码
	if err := vm.AgentSetUserPassword(ctx, "root", password); err != nil {
		// 如果guest agent不可用，只更新内存中的密码
		d.mu.Lock()
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			cfg.OsPass = password
		}
		d.mu.Unlock()
		return &model.ZMessage{Success: true, Action: "Password", Message: "密码已记录（guest agent不可用）"}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.OsPass = password
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "Password", Message: "密码设置成功"}, nil
}

// ============================================================
// 定时任务
// ============================================================

// Crontab 执行定时任务（同步VM状态）
func (d *ProxmoxDriver) Crontab() bool {
	node, err := d.getNode()
	if err != nil {
		return false
	}

	vms, err := node.VirtualMachines(d.Ctx(""))
	if err != nil {
		return false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, vm := range vms {
		vmUUID := strings.ReplaceAll(vm.Name, "-", "_")
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			switch vm.Status {
			case "running":
				cfg.VmFlag = model.VMPowerStarted
			case "stopped":
				cfg.VmFlag = model.VMPowerStopped
			case "paused":
				cfg.VmFlag = model.VMPowerSuspend
			default:
				cfg.VmFlag = model.VMPowerUnknown
			}
		}
	}

	return true
}
