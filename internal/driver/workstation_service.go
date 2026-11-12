// Package driver VMware Workstation 的 vmrest 服务自举实现。
//
// 背景：vmrest 是随 VMware Workstation 安装的 REST 服务（默认 127.0.0.1:8697），
// 不会开机自启。主机重启或手工关闭后，驱动的所有 REST 操作都会因
// 「connectex: No connection could be made because the target machine actively refused it」失败。
//
// 本文件在每次 REST 调用前探测服务端口，端口未监听时按以下顺序定位可执行文件并拉起进程：
//  1. 主机配置 launch_path（vmrest 可执行文件，或 VMware Workstation 安装目录）
//  2. PATH 中的 vmrest（Linux 版通常安装到 /usr/bin）
//  3. 各系统默认安装目录
package driver

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// vmrestDefaultPort vmrest 默认端口
	vmrestDefaultPort = 8697
	// wsProbeTimeout 端口探测超时（本机回环 connect 通常在毫秒级完成）
	wsProbeTimeout = 800 * time.Millisecond
	// wsReadyTTL 探测成功后的结果复用窗口，避免每个 REST 调用都做一次 TCP 握手
	wsReadyTTL = 2 * time.Second
	// wsStartWait 拉起 vmrest 后等待端口就绪的最长时间
	wsStartWait = 20 * time.Second
)

var (
	// wsStartMu 串行化 vmrest 启动，避免并发请求重复拉起进程
	wsStartMu sync.Mutex
	// wsReady 记录各 vmrest 地址（host:port）最近一次探测成功的时刻（UnixNano）
	wsReady sync.Map
)

// wsMarkReady 记录探测成功时刻
func wsMarkReady(key string) {
	wsReady.Store(key, time.Now().UnixNano())
}

// wsRecentlyReady 判断地址是否在复用窗口内已确认可用
func wsRecentlyReady(key string) bool {
	v, ok := wsReady.Load(key)
	if !ok {
		return false
	}
	at, ok := v.(int64)
	return ok && time.Since(time.Unix(0, at)) < wsReadyTTL
}

// wsHostPort 解析 vmrest 监听地址与端口。
// 兼容 server_addr 的三种写法：纯主机、host:port、http://host:port。
func (d *WorkstationDriver) wsHostPort() (string, int) {
	raw := strings.TrimSpace(d.Config.ServerAddr)
	port := d.Config.ServerPort
	if port <= 0 {
		port = vmrestDefaultPort
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")

	host := raw
	if h, p, err := net.SplitHostPort(raw); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = n
		}
	} else if strings.Count(raw, ":") == 1 {
		// 形如 host:port（SplitHostPort 在缺方括号时对 IPv6 报错，这里只处理单冒号）
		parts := strings.SplitN(raw, ":", 2)
		if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
			host, port = parts[0], n
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		host = "127.0.0.1"
	}
	return host, port
}

// wsBaseURL 返回 vmrest 服务基地址
func (d *WorkstationDriver) wsBaseURL() string {
	host, port := d.wsHostPort()
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// wsEnsureRunning 确保 vmrest 可访问：本地地址且端口未监听时自动拉起 vmrest 进程。
// 远端地址不做自动启动（vmrest 需要运行在目标主机上），直接返回明确错误。
func (d *WorkstationDriver) wsEnsureRunning() error {
	host, port := d.wsHostPort()
	key := net.JoinHostPort(dialHost(host), strconv.Itoa(port))
	if wsRecentlyReady(key) {
		return nil
	}
	if d.wsReachable(wsProbeTimeout) {
		wsMarkReady(key)
		return nil
	}

	wsStartMu.Lock()
	defer wsStartMu.Unlock()

	// 双检：等待锁期间可能已被其它请求拉起
	if d.wsReachable(wsProbeTimeout) {
		wsMarkReady(key)
		return nil
	}

	if !wsIsLocalAddress(host) {
		return fmt.Errorf("vmrest 服务不可达（%s），该地址不在本机，请在目标主机启动 vmrest", d.wsBaseURL())
	}

	bin, err := d.vmrestBinary()
	if err != nil {
		return fmt.Errorf("vmrest 未运行，%w", err)
	}

	var args []string
	if port != vmrestDefaultPort {
		// 非默认端口必须显式指定；默认端口交给 vmrest 自己处理
		args = append(args, "-p", strconv.Itoa(port))
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Dir(bin)
	hideCommandWindow(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("vmrest 未运行，自动启动失败（%s）: %w", bin, err)
	}
	// 异步回收子进程，避免僵尸进程；agent 退出后 vmrest 继续常驻
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(wsStartWait)
	for time.Now().Before(deadline) {
		if d.wsReachable(wsProbeTimeout) {
			wsMarkReady(key)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("vmrest 未运行，已尝试启动（%s），但 %s 在 %s 内仍不可达，请检查端口占用后手动运行",
		bin, d.wsBaseURL(), wsStartWait)
}

// wsReachable 探测 vmrest 端口是否已监听
func (d *WorkstationDriver) wsReachable(timeout time.Duration) bool {
	host, port := d.wsHostPort()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(dialHost(host), strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// dialHost 把监听地址（0.0.0.0 / ::）换成可连接的回环地址
func dialHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return host
}

// vmrestBinary 解析 vmrest 可执行文件路径
func (d *WorkstationDriver) vmrestBinary() (string, error) {
	launch := strings.TrimSpace(d.Config.LaunchPath)
	if launch != "" {
		if info, err := os.Stat(launch); err == nil {
			if info.IsDir() {
				if p := findExecutable(launch, "vmrest"); p != "" {
					return p, nil
				}
			} else if isVMRestName(filepath.Base(launch)) {
				return launch, nil
			} else if p := findExecutable(filepath.Dir(launch), "vmrest"); p != "" {
				// launch_path 指向 vmrun.exe 等同目录工具时，从同目录取 vmrest
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath("vmrest"); err == nil {
		return p, nil
	}
	for _, dir := range vmwareInstallDirs() {
		if p := findExecutable(dir, "vmrest"); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 vmrest 可执行文件：请安装 VMware Workstation，或在主机配置的 launch_path 指定其安装目录")
}

// findExecutable 在目录下查找指定名称的可执行文件（Windows 允许省略 .exe）
func findExecutable(dir, name string) string {
	for _, n := range []string{name, name + ".exe"} {
		p := filepath.Join(dir, n)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// isVMRestName 判断文件名是否就是 vmrest 可执行文件
func isVMRestName(base string) bool {
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "vmrest" || base == "vmrest.exe"
}

// vmwareInstallDirs 返回各系统 VMware Workstation 的默认安装目录
func vmwareInstallDirs() []string {
	switch runtime.GOOS {
	case "windows":
		var dirs []string
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			if root := strings.TrimSpace(os.Getenv(env)); root != "" {
				dirs = append(dirs, filepath.Join(root, "VMware", "VMware Workstation"))
			}
		}
		return append(dirs,
			`C:\Program Files (x86)\VMware\VMware Workstation`,
			`C:\Program Files\VMware\VMware Workstation`)
	case "darwin":
		return []string{"/Applications/VMware Fusion.app/Contents/Public"}
	default:
		return []string{"/usr/bin", "/usr/lib/vmware/bin", "/usr/local/bin"}
	}
}

// wsIsLocalAddress 判断 vmrest 地址是否指向本机（只有本机才允许自动拉起进程）
func wsIsLocalAddress(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	if hn, err := os.Hostname(); err == nil && strings.EqualFold(host, hn) {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// 非 IP 且非本机名：不解析 DNS，按远端处理，避免误启动本地进程
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	return localIPSet()[ip.String()]
}

// localIPSet 返回本机网卡地址集合
func localIPSet() map[string]bool {
	result := make(map[string]bool)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return result
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP != nil {
			result[ipNet.IP.String()] = true
		}
	}
	return result
}
