// Package driver 主机级运维能力：终端之外的管理功能。
//
//	进程   —— 查看占用最高的进程、结束进程
//	服务   —— systemctl / Get-Service 列表与启停
//	防火墙 —— iptables / Windows 防火墙规则查看与增删
//	文件   —— 任意目录浏览、上传下载、新建目录、重命名、删除
//
// 与镜像目录管理（image_files.go）的区别：这里的路径由用户在界面上自由切换，
// 因此只做路径合法性校验，不限定在配置的镜像目录内。
package driver

import (
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// HostProcessItem 主机进程
type HostProcessItem struct {
	PID   int    `json:"pid"`
	User  string `json:"user"`
	CPU   float64 `json:"cpu"`
	Mem   float64 `json:"mem"`
	State string `json:"state"`
	// CPUDesc / MemDesc 展示文本：Linux 为百分比，Windows 为 CPU 秒 / 内存字节
	CPUDesc string `json:"cpu_desc"`
	MemDesc string `json:"mem_desc"`
	Command string `json:"command"`
}

// HostServiceItem 系统服务
type HostServiceItem struct {
	Name        string `json:"name"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

// HostFirewallRule 主机防火墙规则（Linux 为 INPUT 链，Windows 为入站规则）
type HostFirewallRule struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Protocol string `json:"protocol"`
	Source   string `json:"source"`
	Port     string `json:"port"`
	Comment  string `json:"comment"`
}

// HostFileItem 主机目录项
type HostFileItem struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	Owner   string `json:"owner"`
	ModTime int64  `json:"mod_time"`
}

// HostOps 主机运维能力
type HostOps interface {
	// HostOS 返回目标主机的操作系统类型：linux / windows
	HostOS() string

	ListProcesses() ([]HostProcessItem, error)
	KillProcess(pid int) error

	ListServices() ([]HostServiceItem, error)
	ControlService(name, action string) error

	ListFirewallRules() ([]HostFirewallRule, error)
	AddFirewallRule(rule HostFirewallRule) error
	DeleteFirewallRule(id string) error

	ListHostDir(dir string) (resolved string, items []HostFileItem, err error)
	CreateHostDir(dir, name string) error
	RenameHostPath(dir, oldName, newName string) error
	DeleteHostPath(dir, name string) error
	UploadHostFile(dir, name string, r io.Reader) (int64, error)
	DownloadHostFile(dir, name string) (io.ReadCloser, error)
}

// NewHostOps 按主机类型返回对应的运维实现。
//
// Hyper-V / VMware Workstation / 逍遥模拟器只能在控制端本机执行 PowerShell；
// 其余平台（PVE、QEMU、ESXi、容器）按 Linux shell 处理，远程时走 SSH。
func NewHostOps(cfg *model.HSConfig) HostOps {
	if cfg != nil && hostOSFor(cfg.ServerType) == "windows" {
		return &windowsHostOps{cfg: cfg}
	}
	return &linuxHostOps{cfg: cfg}
}

// hostOSFor 判断主机目标操作系统
func hostOSFor(serverType string) string {
	switch serverType {
	case "HyperVSetup", "VMWareSetup", "MemuAndroid":
		return "windows"
	case "VirtualBoxs":
		// VirtualBox 在 Windows / Linux 上都可能存在，按驱动宿主机判断
		if isWindowsRuntime() {
			return "windows"
		}
		return "linux"
	}
	return "linux"
}

// validateHostOpsPath 路径合法性：不允许空、换行与控制字符
func validateHostOpsPath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return fmt.Errorf("路径不能为空")
	}
	if strings.ContainsAny(p, "\r\n\x00") {
		return fmt.Errorf("路径包含非法字符")
	}
	return nil
}

// isRootHostPath 判断是否为文件系统根路径（禁止删除）
func isRootHostPath(p string) bool {
	clean := strings.TrimSpace(p)
	if clean == "" || clean == "/" {
		return true
	}
	if len(clean) == 2 && clean[1] == ':' {
		return true
	}
	return false
}

// hostFileName 校验单个文件/目录名
func hostFileName(name string) error {
	name = strings.TrimSpace(name)
	switch name {
	case "", ".", "..":
		return fmt.Errorf("名称不能为空")
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("名称不能包含路径分隔符")
	}
	return nil
}

// ============================================================
// Linux
// ============================================================

type linuxHostOps struct {
	cfg *model.HSConfig
}

func (o *linuxHostOps) HostOS() string { return "linux" }

func (o *linuxHostOps) run(cmd string, timeout time.Duration) (string, error) {
	return newHostCommandRunner(o.cfg).runShell(cmd, timeout)
}

func (o *linuxHostOps) ListProcesses() ([]HostProcessItem, error) {
	out, err := o.run("ps -eo pid,user,pcpu,pmem,stat,args --sort=-pcpu", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取进程列表失败: %w", err)
	}
	items := []HostProcessItem{}
	lines := strings.Split(out, "\n")
	for index, line := range lines {
		if index == 0 || strings.TrimSpace(line) == "" {
			continue // 表头
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		if pidErr != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		mem, _ := strconv.ParseFloat(fields[3], 64)
		items = append(items, HostProcessItem{
			PID:     pid,
			User:    fields[1],
			CPU:     cpu,
			Mem:     mem,
			CPUDesc: fmt.Sprintf("%.1f%%", cpu),
			MemDesc: fmt.Sprintf("%.1f%%", mem),
			State:   fields[4],
			Command: strings.Join(fields[5:], " "),
		})
		if len(items) >= 120 {
			break
		}
	}
	return items, nil
}

func (o *linuxHostOps) KillProcess(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("不允许结束该进程")
	}
	if _, err := o.run(fmt.Sprintf("kill -TERM %d", pid), 20*time.Second); err != nil {
		return fmt.Errorf("结束进程 %d 失败: %w", pid, err)
	}
	return nil
}

func (o *linuxHostOps) ListServices() ([]HostServiceItem, error) {
	out, err := o.run("systemctl list-units --type=service --all --no-pager --no-legend --plain", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取服务列表失败: %w", err)
	}
	items := []HostServiceItem{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// failed 单元行首带 ●，去掉后再解析
		line = strings.TrimSpace(strings.TrimPrefix(line, "●"))
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		desc := ""
		if len(fields) > 4 {
			desc = strings.Join(fields[4:], " ")
		}
		items = append(items, HostServiceItem{
			Name:        fields[0],
			Load:        fields[1],
			Active:      fields[2],
			Sub:         fields[3],
			Description: desc,
		})
	}
	return items, nil
}

func (o *linuxHostOps) ControlService(name, action string) error {
	if err := hostServiceName(name); err != nil {
		return err
	}
	var verb string
	switch action {
	case "start":
		verb = "start"
	case "stop":
		verb = "stop"
	case "restart":
		verb = "restart"
	default:
		return fmt.Errorf("不支持的操作: %s", action)
	}
	if _, err := o.run(fmt.Sprintf("systemctl %s %s", verb, shellQuote(name)), 60*time.Second); err != nil {
		return fmt.Errorf("服务操作失败: %w", err)
	}
	return nil
}

func (o *linuxHostOps) ListFirewallRules() ([]HostFirewallRule, error) {
	out, err := o.run("iptables -L INPUT -n --line-numbers", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取防火墙规则失败: %w", err)
	}
	rules := []HostFirewallRule{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Chain ") || strings.HasPrefix(line, "num ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			continue
		}
		rule := HostFirewallRule{
			ID:       fields[0],
			Target:   fields[1],
			Protocol: fields[2],
			Source:   fields[4],
		}
		extra := strings.Join(fields[6:], " ")
		for _, token := range strings.Fields(extra) {
			switch {
			case strings.HasPrefix(token, "dpt:"):
				rule.Port = strings.TrimPrefix(token, "dpt:")
			case strings.HasPrefix(token, "dpts:"):
				rule.Port = strings.TrimPrefix(token, "dpts:")
			}
		}
		if start := strings.Index(extra, "/*"); start >= 0 {
			if end := strings.Index(extra[start:], "*/"); end > 0 {
				rule.Comment = strings.TrimSpace(extra[start+2 : start+end])
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (o *linuxHostOps) AddFirewallRule(rule HostFirewallRule) error {
	proto := strings.ToLower(strings.TrimSpace(rule.Protocol))
	switch proto {
	case "tcp", "udp", "icmp", "all":
	default:
		return fmt.Errorf("不支持的协议: %s", rule.Protocol)
	}
	target := strings.ToUpper(strings.TrimSpace(rule.Target))
	switch target {
	case "ACCEPT", "DROP", "REJECT":
	default:
		return fmt.Errorf("不支持的动作: %s", rule.Target)
	}

	parts := []string{"iptables", "-A", "INPUT"}
	if proto != "all" {
		parts = append(parts, "-p", proto)
	}
	port := strings.TrimSpace(rule.Port)
	if port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("端口必须是数字: %s", port)
		}
		parts = append(parts, "--dport", port)
	}
	source := strings.TrimSpace(rule.Source)
	if source != "" && source != "0.0.0.0/0" {
		if _, _, cidrErr := net.ParseCIDR(source); cidrErr != nil && net.ParseIP(source) == nil {
			return fmt.Errorf("源地址格式不正确: %s", source)
		}
		parts = append(parts, "-s", source)
	}
	parts = append(parts, "-j", target)
	comment := strings.TrimSpace(rule.Comment)
	if comment != "" {
		parts = append(parts, "-m", "comment", "--comment", comment)
	}

	cmdParts := make([]string, 0, len(parts))
	for _, p := range parts {
		cmdParts = append(cmdParts, shellQuote(p))
	}
	if _, err := o.run(strings.Join(cmdParts, " "), 30*time.Second); err != nil {
		return fmt.Errorf("添加防火墙规则失败: %w", err)
	}
	return nil
}

func (o *linuxHostOps) DeleteFirewallRule(id string) error {
	num, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || num <= 0 {
		return fmt.Errorf("规则编号不合法: %s", id)
	}
	if _, err := o.run(fmt.Sprintf("iptables -D INPUT %d", num), 30*time.Second); err != nil {
		return fmt.Errorf("删除防火墙规则失败: %w", err)
	}
	return nil
}

func (o *linuxHostOps) ListHostDir(dir string) (string, []HostFileItem, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return "", nil, err
	}
	clean := path.Clean(dir)
	// %f 名称、%y 类型、%s 大小、%T@ 时间、%M 权限、%u 属主
	out, err := o.run(fmt.Sprintf(
		"find %s -maxdepth 1 -mindepth 1 -printf '%%f\\t%%y\\t%%s\\t%%T@\\t%%M\\t%%u\\n'",
		shellQuote(clean)), 30*time.Second)
	if err != nil {
		return clean, nil, fmt.Errorf("读取目录失败: %w", err)
	}
	items := []HostFileItem{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		size, _ := strconv.ParseInt(parts[2], 10, 64)
		ts := parts[3]
		if idx := strings.Index(ts, "."); idx >= 0 {
			ts = ts[:idx]
		}
		modTime, _ := strconv.ParseInt(ts, 10, 64)
		items = append(items, HostFileItem{
			Name:    parts[0],
			IsDir:   parts[1] == "d",
			Size:    size,
			Mode:    parts[4],
			Owner:   parts[5],
			ModTime: modTime,
		})
	}
	return clean, items, nil
}

func (o *linuxHostOps) CreateHostDir(dir, name string) error {
	if err := validateHostOpsPath(dir); err != nil {
		return err
	}
	if err := hostFileName(name); err != nil {
		return err
	}
	target := path.Join(path.Clean(dir), name)
	if _, err := o.run(fmt.Sprintf("mkdir -p %s", shellQuote(target)), 30*time.Second); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	return nil
}

func (o *linuxHostOps) RenameHostPath(dir, oldName, newName string) error {
	if err := validateHostOpsPath(dir); err != nil {
		return err
	}
	if err := hostFileName(oldName); err != nil {
		return err
	}
	if err := hostFileName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}
	base := path.Clean(dir)
	if _, err := o.run(fmt.Sprintf("mv -f %s %s",
		shellQuote(path.Join(base, oldName)), shellQuote(path.Join(base, newName))), 30*time.Second); err != nil {
		return fmt.Errorf("重命名失败: %w", err)
	}
	return nil
}

func (o *linuxHostOps) DeleteHostPath(dir, name string) error {
	if err := validateHostOpsPath(dir); err != nil {
		return err
	}
	if err := hostFileName(name); err != nil {
		return err
	}
	target := path.Join(path.Clean(dir), name)
	if isRootHostPath(target) {
		return fmt.Errorf("不允许删除根目录")
	}
	if _, err := o.run(fmt.Sprintf("rm -rf %s", shellQuote(target)), 60*time.Second); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}

func (o *linuxHostOps) UploadHostFile(dir, name string, r io.Reader) (int64, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return 0, err
	}
	if err := hostFileName(name); err != nil {
		return 0, err
	}
	base := path.Clean(dir)

	if !newHostCommandRunner(o.cfg).isRemote() {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return 0, fmt.Errorf("创建目录失败: %w", err)
		}
		tmp := filepath.Join(base, name+".uploading")
		f, err := os.Create(tmp)
		if err != nil {
			return 0, fmt.Errorf("创建文件失败: %w", err)
		}
		n, copyErr := io.Copy(f, r)
		closeErr := f.Close()
		if copyErr != nil {
			_ = os.Remove(tmp)
			return n, fmt.Errorf("写入文件失败: %w", copyErr)
		}
		if closeErr != nil {
			_ = os.Remove(tmp)
			return n, fmt.Errorf("写入文件失败: %w", closeErr)
		}
		if err := os.Rename(tmp, filepath.Join(base, name)); err != nil {
			_ = os.Remove(tmp)
			return n, fmt.Errorf("保存文件失败: %w", err)
		}
		return n, nil
	}

	if _, err := o.run(fmt.Sprintf("mkdir -p %s", shellQuote(base)), 30*time.Second); err != nil {
		return 0, fmt.Errorf("创建主机目录失败: %w", err)
	}
	target := path.Join(base, name)
	tmp := target + ".uploading"
	n, stderr, err := runSSHStream(sshConfigFromHS(o.cfg), fmt.Sprintf("cat > %s", shellQuote(tmp)), r)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return n, fmt.Errorf("上传到主机失败: %s", msg)
	}
	if _, err := o.run(fmt.Sprintf("mv -f %s %s", shellQuote(tmp), shellQuote(target)), 60*time.Second); err != nil {
		return n, fmt.Errorf("保存文件失败: %w", err)
	}
	return n, nil
}

func (o *linuxHostOps) DownloadHostFile(dir, name string) (io.ReadCloser, error) {
	if err := validateHostOpsPath(dir); err != nil {
		return nil, err
	}
	if err := hostFileName(name); err != nil {
		return nil, err
	}
	target := path.Join(path.Clean(dir), name)

	if !newHostCommandRunner(o.cfg).isRemote() {
		f, err := os.Open(target)
		if err != nil {
			return nil, fmt.Errorf("打开文件失败: %w", err)
		}
		if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
			_ = f.Close()
			return nil, fmt.Errorf("目标是目录，无法下载")
		}
		return f, nil
	}

	return openSSHReadStream(sshConfigFromHS(o.cfg), fmt.Sprintf("cat %s", shellQuote(target)))
}

// openSSHReadStream 通过 SSH 读取远端文件内容（流式，适合大文件下载）
func openSSHReadStream(cfg SSHExecConfig, cmd string) (io.ReadCloser, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("SSH 主机地址为空")
	}
	if cfg.Port <= 0 {
		cfg.Port = 22
	}
	if strings.TrimSpace(cfg.User) == "" {
		cfg.User = "root"
	}

	clientCfg := &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(cfg.Password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = cfg.Password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接 %s 失败: %w", addr, err)
	}
	session, err := conn.NewSession()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SSH 创建会话失败: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("SSH 打开读取通道失败: %w", err)
	}
	if err := session.Start(cmd); err != nil {
		_ = session.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("SSH 启动命令失败: %w", err)
	}
	return &sshStreamReader{reader: stdout, session: session, conn: conn}, nil
}

// sshStreamReader 把 SSH 会话包装成 ReadCloser
type sshStreamReader struct {
	reader  io.Reader
	session *ssh.Session
	conn    *ssh.Client
}

func (s *sshStreamReader) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *sshStreamReader) Close() error {
	_ = s.session.Close()
	return s.conn.Close()
}

// hostServiceName 服务名校验（systemd 单元名允许字母数字与 . _ @ -）
func hostServiceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("服务名不能为空")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '@', r == ':':
		default:
			return fmt.Errorf("服务名包含非法字符: %s", name)
		}
	}
	return nil
}
