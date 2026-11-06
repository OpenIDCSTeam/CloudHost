// Package service Web反向代理下发服务（Caddy）
// 对应 Python 版 HostModule/HttpManager 的 config_all / reload_web 能力。
// 与 Python 的差异：Python 维护内存态 proxys_list，Go 直接从 vm_saving 全量重建配置，
// 避免重启后内存态丢失，配置生成规则保持一致。
package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/data"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// ProxyService 反向代理下发服务
type ProxyService struct {
	db       *data.DB
	resolver func(string) driver.HostDriver
	mu       sync.Mutex
	// publishMu 串行化「重建配置 → 下发」全过程。
	// mu 只保护单次构建，无法阻止两次变更交错：
	// A 读到旧配置、B 先完成下发、A 后完成，会把运行态退回旧版本。
	// 该锁必须在 mu 外层获取，避免与 Rebuild 内部加锁形成自锁。
	publishMu  sync.Mutex
	binaryPath string
	configFile string
	managePort int
	dataDir    string
	// natSvc 中转模式下用于自动创建端口映射（relay 必须有一个公网入口）
	natSvc *NATService
}

// SetNATService 注入 NAT 服务：中转模式的反向代理需要先有端口映射，
// 缺失时由代理流程自动创建，而不是要求用户手工添加。
func (s *ProxyService) SetNATService(svc *NATService) {
	s.natSvc = svc
}

// SetDriverResolver 注入驱动解析器（用于识别平台自有的代理通道）
func (s *ProxyService) SetDriverResolver(fn func(string) driver.HostDriver) {
	s.resolver = fn
}

// provider 返回主机驱动实现的平台自有代理通道（若支持）
func (s *ProxyService) provider(hsName string) (driver.NatProxyProvider, bool) {
	if s.resolver == nil {
		return nil, false
	}
	d := s.resolver(hsName)
	if d == nil {
		return nil, false
	}
	p, ok := d.(driver.NatProxyProvider)
	return p, ok
}

// NewProxyService 创建反向代理服务
//
// dataDir / Caddyfile 统一转绝对路径：caddy 由子进程启动，
// 相对路径会随工作目录变化而解析到不同位置。
func NewProxyService(db *data.DB, dataDir string) *ProxyService {
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		absDir = dataDir
	}
	// Caddyfile 位于 data/temp/，首次运行先确保目录存在
	_ = os.MkdirAll(filepath.Join(absDir, "temp"), 0o750)
	return &ProxyService{
		db:         db,
		binaryPath: strings.TrimSpace(os.Getenv("CADDY_BIN")),
		configFile: filepath.Join(absDir, "temp", "Caddyfile"),
		managePort: 2019,
		dataDir:    absDir,
	}
}

// SetBinaryPath 设置 caddy 可执行文件路径
func (s *ProxyService) SetBinaryPath(path string) {
	if path != "" {
		s.binaryPath = path
	}
}

// BinaryPath 返回当前可用的 caddy 可执行文件绝对路径，未找到时返回空串。
func (s *ProxyService) BinaryPath() string {
	return s.resolveBinary()
}

// caddyExecName 返回当前平台的 caddy 可执行文件名
func caddyExecName() string {
	if runtime.GOOS == "windows" {
		return "caddy.exe"
	}
	return "caddy"
}

// resolveBinary 定位 caddy 可执行文件，顺序：
//  1. CADDY_BIN / SetBinaryPath 的显式配置
//  2. 程序所在目录/bin、程序所在目录、上级目录/bin（源码运行时即仓库 bin）
//  3. PATH
//
// 之前只认 "caddy" 走 PATH：部署包里把 caddy 放在 hostagent 旁边时
// 会直接报 "executable file not found in %PATH%"，且每次下发都重试。
func (s *ProxyService) resolveBinary() string {
	if s.binaryPath != "" {
		if p := lookCaddy(s.binaryPath); p != "" {
			return p
		}
	}
	for _, dir := range s.caddySearchDirs() {
		if p := lookCaddy(filepath.Join(dir, caddyExecName())); p != "" {
			return p
		}
	}
	if p, err := exec.LookPath("caddy"); err == nil {
		return p
	}
	return ""
}

// lookCaddy 校验候选路径：纯文件名按 PATH 查找，带路径的按文件存在性判断。
func lookCaddy(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	if !strings.ContainsAny(candidate, `/\`) {
		if p, err := exec.LookPath(candidate); err == nil {
			return p
		}
		return ""
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return ""
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return ""
	}
	return abs
}

// caddySearchDirs 返回 caddy 的候选目录。
//
// 除了「程序所在目录」，还必须包含「部署目录」与「工作目录」：
// 用 GoLand 等 IDE 直接运行时，os.Executable() 指向 IDE 的临时目录
// （...\GoLand\tmp\...），真正的 bin/caddy.exe 在项目里，
// 只按 exe 目录搜索会永远找不到，表现为「未找到 caddy 可执行文件」。
func (s *ProxyService) caddySearchDirs() []string {
	var dirs []string
	add := func(dir string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		dirs = append(dirs, filepath.Join(abs, "bin"), abs)
	}

	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		add(base)
		add(filepath.Join(base, ".."))
	}
	// <部署目录>/data/temp/Caddyfile：向外两级即部署目录
	if s.configFile != "" {
		add(filepath.Dir(filepath.Dir(filepath.Dir(s.configFile))))
		add(filepath.Join(s.dataDir, "bins"))
	}
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	return dirs
}

// ConfigFile 返回配置文件路径
func (s *ProxyService) ConfigFile() string {
	return s.configFile
}

// Rebuild 从数据库全量重建 Caddyfile
func (s *ProxyService) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hosts, err := s.db.GetAllHosts()
	if err != nil {
		return fmt.Errorf("获取主机列表失败: %w", err)
	}

	// 域名去重（与 Python proxys_list 以 domain 为 key 的行为一致）
	// 同时按主机的代理模式计算实际上游：直连虚拟机内网 IP，或经爱快公网中转。
	entries := make(map[string]proxyEntry)
	for _, h := range hosts {
		// 平台自有代理通道由平台 API 管理，不纳入本地 Caddy 配置
		if _, ok := s.provider(h.HsName); ok {
			continue
		}
		mode := strings.TrimSpace(h.ProxyMode)
		if mode == "" {
			mode = "direct"
		}
		relay := strings.TrimSpace(h.RelayAddr)
		if mode == "relay" && relay == "" {
			relay = firstPublicAddr(h.PublicAddr)
		}
		vms, err := s.db.GetVMSaving(h.HsName)
		if err != nil {
			continue
		}
		for _, vmJSON := range vms {
			vm, decodeErr := DecodeVMConfig(vmJSON)
			if decodeErr != nil || vm == nil {
				continue
			}
			// 正在删除（存在未完成墓碑）的虚拟机不参与配置生成：
			// 其本地配置尚未清理时，重建会把已下线的代理重新写回 Caddy。
			if s.db.HasIncompleteTombstone(h.HsName, vm.VmUUID) {
				continue
			}
			for _, web := range vm.WebAll {
				if web.WebAddr == "" {
					continue
				}
				upHost, upPort := web.LanAddr, web.LanPort
				if mode == "relay" {
					// 中转模式绝不能回退直连：用户选择中转通常正是因为直连不通。
					// 上游必须是「中转地址:公网端口」，缺任何一项都跳过并明确报错。
					wanPort := natWanPortFor(vm.NatAll, web.LanPort)
					if relay == "" {
						log.Printf("[proxy] %s：中转模式未配置中转地址（relay_addr 或服务器公网IP），已跳过",
							web.WebAddr)
						continue
					}
					if wanPort == 0 {
						log.Printf("[proxy] %s：中转模式缺少 %s:%d 的端口映射，已跳过",
							web.WebAddr, web.LanAddr, web.LanPort)
						continue
					}
					upHost, upPort = relay, wanPort
				}
				if _, exists := entries[web.WebAddr]; !exists {
					entries[web.WebAddr] = proxyEntry{web: web, host: upHost, port: upPort}
				}
			}
		}
	}

	var sb strings.Builder
	// 注意：这里**不能**写 admin 段。
	// Caddy 的 /load 会尝试按新配置重新绑定管理端点，而该端口正被
	// 它自己占用，于是返回 400：
	//   starting caddy administration endpoint: bind: Only one usage of ...
	// 结果是每次下发都失败。管理端点用 Caddy 默认值（localhost:2019）即可，
	// 我们只需要把**客户端**的请求地址写成 127.0.0.1（避免 Windows 上
	// localhost 解析到 IPv6 ::1）。
	for _, e := range entries {
		sb.WriteString(buildCaddyBlock(e.web, e.host, e.port))
	}
	if len(entries) == 0 {
		// 全部规则删除后 Caddyfile 只剩空行，Caddy 适配器会以
		// 「adapting config using caddyfile: EOF」拒绝加载，
		// 于是每次删除最后一条规则都失败并回滚。留一行注释便于识别。
		sb.WriteString("# 当前没有启用中的反向代理规则\n")
	}

	if err := os.MkdirAll(filepath.Dir(s.configFile), 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	if err := os.WriteFile(s.configFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("写入Caddyfile失败: %w", err)
	}
	return nil
}

// Reload 重载 Caddy 配置
// 优先级：
//  1. Admin API 直连（Caddy 进程已运行时可用，无需 caddy 可执行文件）
//  2. caddy reload CLI（未启用 Admin API 或进程由 CLI 管理时）
//  3. caddy start 后台启动（进程尚未运行时）
func (s *ProxyService) Reload() error {
	caddyfile, err := os.ReadFile(s.configFile)
	if err != nil {
		return fmt.Errorf("读取Caddyfile失败: %w", err)
	}

	// 1) Admin API：POST /load?adapter=caddyfile
	// Caddy 已由外部托管时，这一步不需要本地可执行文件。
	apiErr := s.reloadViaAdminAPI(caddyfile)
	if apiErr == nil {
		return nil
	}

	// 2) CLI reload：先确认可执行文件存在，避免连续两次无效 exec
	bin := s.resolveBinary()
	if bin == "" {
		return fmt.Errorf("未找到 caddy 可执行文件（Admin API 不可用: %v）："+
			"请安装 Caddy，把 %s 放到程序目录或 bin 子目录，或设置环境变量 CADDY_BIN",
			apiErr, caddyExecName())
	}
	log.Printf("[proxy] Admin API 不可用（%v），改用 %s", apiErr, bin)

	args := []string{
		bin, "reload",
		"--config", s.configFile,
		"--adapter", "caddyfile",
		"--address", fmt.Sprintf("127.0.0.1:%d", s.managePort),
	}
	if out, _, err := security.SafeExecWithTimeout(args, 30*time.Second); err == nil {
		return nil
	} else {
		log.Printf("[proxy] Caddy reload 失败，尝试启动: %v (%s)", err, strings.TrimSpace(out))
	}

	// 3) 回退：后台启动
	startArgs := []string{
		bin, "start",
		"--config", s.configFile,
		"--adapter", "caddyfile",
	}
	if out, _, err := security.SafeExecWithTimeout(startArgs, 30*time.Second); err != nil {
		return fmt.Errorf("启动Caddy失败: %v (%s)", err, strings.TrimSpace(out))
	}
	// start 是异步拉起进程：稍等 Admin API 就绪，避免「命令成功但配置尚未生效」
	if err := s.waitAdminReady(3 * time.Second); err != nil {
		log.Printf("[proxy] Caddy 已启动，但 Admin API 在 3s 内未就绪: %v", err)
	}
	return nil
}

// adminAlive 判断 Caddy Admin API 是否可用（本地探测，成本极低）。
func (s *ProxyService) adminAlive() bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/config/", s.managePort))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// waitAdminReady 轮询 Admin API，等待 Caddy 完成启动。
func (s *ProxyService) waitAdminReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/config/", s.managePort)
	client := &http.Client{Timeout: time.Second}

	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("超时")
	}
	return lastErr
}

// isEmptyCaddyfile 判断配置里是否只有注释与空行（没有任何站点）。
func isEmptyCaddyfile(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return false
	}
	return true
}

// reloadEmptyJSON 用 JSON 空配置清空 Caddy 运行态。
//
// caddyfile 适配器不接受空文件（EOF），但 Admin API 接受 JSON，
// 传 `{}` 即可移除全部站点。
func (s *ProxyService) reloadEmptyJSON() error {
	url := fmt.Sprintf("http://127.0.0.1:%d/load", s.managePort)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// reloadViaAdminAPI 通过 Caddy Admin API 直接加载 Caddyfile
// Caddy 支持 POST /load 并以 Content-Type + ?adapter 指定配置格式，
// 因此无需引入 caddy Go 模块，也无需本地 caddy 可执行文件。
func (s *ProxyService) reloadViaAdminAPI(caddyfile []byte) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/load?adapter=caddyfile", s.managePort)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(caddyfile))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/caddyfile")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// RebuildAndReload 重建配置并重载（串行发布，避免变更交错导致配置回退）。
//
// 新增回滚：原实现在 reload 失败时只返回错误，磁盘上留下的是一份
// 与运行态不一致的新配置；这里先备份上一份可用配置，失败时恢复并重载。
func (s *ProxyService) RebuildAndReload() error {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	backup, backupErr := os.ReadFile(s.configFile)

	if err := s.Rebuild(); err != nil {
		return err
	}

	// 配置内容没有变化且 Caddy 仍在运行时无需重载：
	// 删除虚拟机、重复下发等操作会反复触发全量重建，
	// 每次都 POST /load 属于无谓的全量配置替换。
	if backupErr == nil && s.adminAlive() {
		if current, err := os.ReadFile(s.configFile); err == nil && bytes.Equal(backup, current) {
			return nil
		}
	}

	// 没有任何规则时，Caddyfile 只有注释：caddyfile 适配器会报 EOF。
	// 直接用 JSON 空配置清空运行态（等价于移除所有站点）。
	if current, err := os.ReadFile(s.configFile); err == nil && isEmptyCaddyfile(current) {
		if err := s.reloadEmptyJSON(); err != nil {
			log.Printf("[proxy] 清空 Caddy 配置失败: %v", err)
			return err
		}
		return nil
	}

	if err := s.Reload(); err != nil {
		if backupErr == nil {
			if restoreErr := os.WriteFile(s.configFile, backup, 0644); restoreErr != nil {
				log.Printf("[proxy] 回滚 Caddyfile 失败: %v", restoreErr)
			} else if reloadErr := s.Reload(); reloadErr != nil {
				log.Printf("[proxy] 回滚后重新加载失败: %v", reloadErr)
			} else {
				log.Printf("[proxy] 下发失败，已回滚到上一份可用配置")
			}
		}
		return err
	}
	return nil
}

// HasProvider 判断主机是否使用平台自有的代理通道。
// 这类通道由云端 API 管理，删除时需要逐条调用云端接口销账。
func (s *ProxyService) HasProvider(hsName string) bool {
	_, ok := s.provider(hsName)
	return ok
}

// RevokeProxy 撤销一条反向代理。
// 平台自有通道调用云端接口；本地 Caddy 场景触发配置重建。
func (s *ProxyService) RevokeProxy(hsName, vmUUID string, proxy model.WebProxy) error {
	return s.ApplyProxy(hsName, vmUUID, proxy, false)
}

// ApplyProxy 下发反向代理
// 平台自有通道优先，否则重建并重载本地 Caddy 配置。
func (s *ProxyService) ApplyProxy(hsName, vmUUID string, proxy model.WebProxy, add bool) error {
	if p, ok := s.provider(hsName); ok {
		msg, err := p.ProxyMap(vmUUID, proxy, add)
		if err != nil {
			return err
		}
		if msg != nil && !msg.Success {
			return fmt.Errorf("%s", msg.Message)
		}
		return nil
	}

	// 中转模式下反向代理必须有公网入口：没有对应端口映射时自动创建一条，
	// 而不是要求用户先去端口映射页手工添加。
	if add && s.isRelayMode(hsName) {
		if err := s.ensureProxyPortMap(hsName, vmUUID, proxy); err != nil {
			return err
		}
	}
	return s.RebuildAndReload()
}

// isRelayMode 判断主机是否配置为「经爱快中转」。
func (s *ProxyService) isRelayMode(hsName string) bool {
	host, err := s.db.GetHost(hsName)
	if err != nil || host == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(host.ProxyMode), "relay")
}

// ensureProxyPortMap 确保「内网端口」有一条可用的端口映射。
//
// 中转模式的上游是「中转地址:公网端口」，公网端口来自端口映射；
// 没有映射时反向代理无从转发，因此在创建代理前先自动补一条。
func (s *ProxyService) ensureProxyPortMap(hsName, vmUUID string, proxy model.WebProxy) error {
	if s.natSvc == nil {
		return fmt.Errorf("中转模式需要端口映射，但 NAT 服务不可用")
	}
	raw, err := s.db.GetVMConfig(hsName, vmUUID)
	if err != nil || raw == "" {
		return fmt.Errorf("读取虚拟机配置失败")
	}
	vm, err := DecodeVMConfig(raw)
	if err != nil || vm == nil {
		return fmt.Errorf("解析虚拟机配置失败")
	}
	if natWanPortFor(vm.NatAll, proxy.LanPort) > 0 {
		return nil
	}

	nat := model.PortData{
		LanPort: proxy.LanPort,
		LanAddr: proxy.LanAddr,
		NatTips: fmt.Sprintf("proxy-%s", proxy.WebAddr),
	}
	if err := s.natSvc.ApplyPortMap(hsName, vmUUID, &nat, 0); err != nil {
		return fmt.Errorf("中转模式需要端口映射，自动创建 %d 端口映射失败: %w", proxy.LanPort, err)
	}
	vm.NatAll = append(vm.NatAll, nat)
	if err := s.db.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		log.Printf("[proxy] 自动创建端口映射后保存虚拟机配置失败: %v", err)
	}
	log.Printf("[proxy] 中转模式已自动创建端口映射：%s:%d → 公网端口 %d",
		nat.LanAddr, nat.LanPort, nat.WanPort)
	return nil
}

// proxyEntry 一条代理规则及其实际生效的上游地址。
//
// 上游取决于主机的代理模式：
//   - direct：虚拟机内网 IP : 内网端口
//   - relay ：中转地址（爱快公网） : 该内网端口对应的公网端口
type proxyEntry struct {
	web  model.WebProxy
	host string
	port int
}

// firstPublicAddr 取 public_addr（JSON 数组）中的首个非空地址。
func firstPublicAddr(raw string) string {
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return ""
	}
	for _, a := range list {
		if v := strings.TrimSpace(a); v != "" {
			return v
		}
	}
	return ""
}

// natWanPortFor 找到内网端口对应的公网端口（同端口多条时取第一条）。
func natWanPortFor(rules []model.PortData, lanPort int) int {
	for _, r := range rules {
		if r.LanPort == lanPort && r.WanPort > 0 {
			return r.WanPort
		}
	}
	return 0
}

// buildCaddyBlock 生成单个反向代理的 Caddyfile 片段
// 规则与 Python HttpManager.config_all 保持一致
func buildCaddyBlock(web model.WebProxy, ip string, port int) string {
	domain := web.WebAddr
	isHTTPS := web.IsHTTPS

	backendProtocol := "http"
	if isHTTPS {
		backendProtocol = "https"
	}

	// 与 Python 一致：普通场景 listen_port 为空，走默认分支
	url := domain
	if !isHTTPS {
		url = "http://" + domain
	}

	tlsSkip := ""
	if isHTTPS {
		tlsSkip = "\n\t\ttransport http {\n\t\t\ttls_insecure_skip_verify\n\t\t}"
	}

	// 子路径代理
	if idx := strings.Index(domain, "/"); idx >= 0 {
		subPath := domain[idx:]
		return fmt.Sprintf("%s {\n\t@secret path %s\n\treverse_proxy %s://%s:%d\n}\n\n",
			url, subPath, backendProtocol, ip, port)
	}

	return fmt.Sprintf("%s {\n\treverse_proxy %s://%s:%d {%s\n\t}\n}\n\n",
		url, backendProtocol, ip, port, tlsSkip)
}
