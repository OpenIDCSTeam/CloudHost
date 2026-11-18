// Package driver SmolVM microVM 驱动（Firecracker + Docker rootfs）
// 参考 Python 版 HostServer/SmolVM.py 对等实现。
package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// ============================================================
// SmolVM Driver
// ============================================================

// SmolVMDriver 轻量级 microVM 驱动
type SmolVMDriver struct {
	*BaseDriver
	sockRoot string
	once     sync.Once
	procMu   sync.Mutex
	// sshMu/sshCli 远程模式下复用的 SSH 连接（用于访问对端 Firecracker UDS）
	sshMu  sync.Mutex
	sshCli *ssh.Client
}

// NewSmolVMDriver 创建 SmolVM 驱动
func NewSmolVMDriver(cfg *model.HSConfig) *SmolVMDriver {
	return &SmolVMDriver{
		BaseDriver: NewBaseDriver(cfg),
		sockRoot:   "/run/smolvm",
	}
}

// ============================================================
// 工具函数：本地/远程命令执行
// ============================================================

// isRemote 判断是否为远程主机
func (d *SmolVMDriver) isRemote() bool {
	addr := d.Config.ServerAddr
	if addr == "" || addr == "localhost" || addr == "127.0.0.1" {
		return false
	}
	return true
}

// execHost 在宿主机上执行命令（本地直接 exec；远程通过 SSH）
func (d *SmolVMDriver) execHost(cmd string, timeout time.Duration) (bool, string, string) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	if d.isRemote() {
		cfg := sshConfigFromHS(d.Config)
		cfg.Timeout = timeout
		res, err := RunSSHCommand(cfg, cmd)
		if err != nil {
			return false, "", err.Error()
		}
		if res == nil {
			return false, "", "远程命令无返回"
		}
		return res.ExitCode == 0, res.Stdout, res.Stderr
	}

	ctx, cancel := context.WithTimeout(d.Ctx(""), timeout)
	defer cancel()

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	return err == nil, stdout.String(), stderr.String()
}

// ============================================================
// 路径 / 名称约定
// ============================================================

func (d *SmolVMDriver) vmDir(vmUUID string) string {
	base := d.Config.ExternPath
	if base == "" {
		base = "/var/lib/smolvm"
	}
	return filepath.ToSlash(filepath.Join(base, vmUUID))
}

func (d *SmolVMDriver) vmSock(vmUUID string) string {
	return filepath.ToSlash(filepath.Join(d.sockRoot, vmUUID+".sock"))
}

func (d *SmolVMDriver) vmPidFile(vmUUID string) string {
	return filepath.ToSlash(filepath.Join(d.sockRoot, vmUUID+".pid"))
}

func (d *SmolVMDriver) tapName(vmUUID string) string {
	clean := strings.ReplaceAll(vmUUID, "-", "")
	if len(clean) > 10 {
		clean = clean[:10]
	}
	name := "tap" + clean
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

func (d *SmolVMDriver) kernelPath() string {
	if d.Config.ExtendData != nil {
		if v, ok := d.Config.ExtendData["smolvm_kernel"].(string); ok && v != "" {
			return v
		}
	}
	base := d.Config.ImagesPath
	if base == "" {
		base = "/var/lib/smolvm/images"
	}
	return filepath.ToSlash(filepath.Join(base, "vmlinux-smolvm"))
}

func (d *SmolVMDriver) hypervisorBin() string {
	if d.Config.ExtendData != nil {
		if v, ok := d.Config.ExtendData["smolvm_hv_path"].(string); ok && v != "" {
			return v
		}
	}
	return "firecracker"
}

// ============================================================
// Firecracker HTTP over UDS 客户端
// ============================================================

// fcClient 返回一个指向指定 socket 的 *http.Client
func (d *SmolVMDriver) fcClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.dialFCSock(sock)
			},
		},
	}
}

// dialFCSock 连接 Firecracker API 的 unix socket。
//
// 本地主机直接 net.Dial；远程主机通过 SSH 的 streamlocal（direct-streamlocal）
// 通道访问对端 UDS —— 原实现无论本地远程都 Dial 本机路径，
// 远程主机必然在装配阶段失败。
func (d *SmolVMDriver) dialFCSock(sock string) (net.Conn, error) {
	if !d.isRemote() {
		return net.DialTimeout("unix", sock, 3*time.Second)
	}
	cli, err := d.remoteSSHClient()
	if err != nil {
		return nil, err
	}
	conn, err := cli.Dial("unix", sock)
	if err == nil {
		return conn, nil
	}
	// 连接可能已被对端关闭：清空缓存重连一次
	d.resetSSHClient()
	cli, err2 := d.remoteSSHClient()
	if err2 != nil {
		return nil, err2
	}
	return cli.Dial("unix", sock)
}

// remoteSSHClient 返回（并缓存）到远程宿主机的 SSH 连接
func (d *SmolVMDriver) remoteSSHClient() (*ssh.Client, error) {
	d.sshMu.Lock()
	defer d.sshMu.Unlock()
	if d.sshCli != nil {
		return d.sshCli, nil
	}
	cli, err := dialSSHClient(sshConfigFromHS(d.Config))
	if err != nil {
		return nil, err
	}
	d.sshCli = cli
	return cli, nil
}

// resetSSHClient 关闭并丢弃缓存的 SSH 连接
func (d *SmolVMDriver) resetSSHClient() {
	d.sshMu.Lock()
	defer d.sshMu.Unlock()
	if d.sshCli != nil {
		_ = d.sshCli.Close()
		d.sshCli = nil
	}
}

// fcDo 执行一次 HTTP 请求到 firecracker socket
func (d *SmolVMDriver) fcDo(sock, method, path string, body interface{}) (int, map[string]interface{}, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, "http://unix"+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := d.fcClient(sock).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	result := map[string]interface{}{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return resp.StatusCode, result, nil
}

// waitSockReady 等待 socket 可连接
func (d *SmolVMDriver) waitSockReady(sock string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok, _, _ := d.execHost(fmt.Sprintf("test -S \"%s\" && echo YES || echo NO", sock), 3*time.Second); ok {
			// 本地与远程都实际连接一次：远程经 SSH streamlocal 通道，
			// 能同时验证 socket 可用与通道放行。
			if c, err := d.dialFCSock(sock); err == nil {
				_ = c.Close()
				return true
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// ============================================================
// 镜像与 rootfs 构建（通过宿主机命令完成）
// ============================================================

// ensureDockerImage 确保 Docker 镜像存在
func (d *SmolVMDriver) ensureDockerImage(image string) error {
	ok, out, _ := d.execHost(fmt.Sprintf(
		"docker image inspect %s >/dev/null 2>&1 && echo YES || echo NO", image),
		15*time.Second)
	if ok && strings.Contains(out, "YES") {
		return nil
	}
	ok, _, err := d.execHost(fmt.Sprintf("docker pull %s", image), 15*time.Minute)
	if !ok {
		return fmt.Errorf("docker pull 失败: %s", err)
	}
	return nil
}

// buildRootfs 构建 ext4 rootfs 镜像
func (d *SmolVMDriver) buildRootfs(image, outRootfs string, sizeMB int, rootPass string) error {
	if sizeMB <= 0 {
		sizeMB = 8 * 1024
	}

	// 处理 tar 文件离线加载
	if strings.HasSuffix(image, ".tar") || strings.HasSuffix(image, ".tar.gz") {
		tarPath := image
		if !filepath.IsAbs(tarPath) {
			tarPath = filepath.ToSlash(filepath.Join(d.Config.ImagesPath, image))
		}
		// 加载
		loadCmd := fmt.Sprintf("docker load -i \"%s\"", tarPath)
		if strings.HasSuffix(image, ".tar.gz") {
			loadCmd = fmt.Sprintf("gunzip -c \"%s\" | docker load", tarPath)
		}
		if ok, _, err := d.execHost(loadCmd, 15*time.Minute); !ok {
			return fmt.Errorf("docker load 失败: %s", err)
		}
		image = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(image), ".tar.gz"), ".tar")
	} else if err := d.ensureDockerImage(image); err != nil {
		return err
	}

	outDir := filepath.ToSlash(filepath.Dir(outRootfs))
	workTag := fmt.Sprintf("%d", time.Now().UnixNano())
	workDir := fmt.Sprintf("%s/.build-%s", outDir, workTag)
	exportTar := fmt.Sprintf("%s/.export-%s.tar", outDir, workTag)
	containerName := fmt.Sprintf("smolvm-build-%s", workTag[len(workTag)-8:])

	cleanupFns := []func(){
		func() {
			d.execHost(fmt.Sprintf("docker rm -f %s >/dev/null 2>&1 || true", containerName), 30*time.Second)
		},
		func() { d.execHost(fmt.Sprintf("rm -f \"%s\"", exportTar), 10*time.Second) },
		func() { d.execHost(fmt.Sprintf("rm -rf \"%s\"", workDir), 30*time.Second) },
	}
	defer func() {
		for _, f := range cleanupFns {
			f()
		}
	}()

	d.execHost(fmt.Sprintf("mkdir -p \"%s\" \"%s\"", outDir, workDir), 10*time.Second)

	if ok, _, err := d.execHost(fmt.Sprintf("docker create --name %s %s /bin/sh", containerName, image), 2*time.Minute); !ok {
		return fmt.Errorf("docker create 失败: %s", err)
	}
	if ok, _, err := d.execHost(fmt.Sprintf("docker export %s -o \"%s\"", containerName, exportTar), 10*time.Minute); !ok {
		return fmt.Errorf("docker export 失败: %s", err)
	}
	if ok, _, err := d.execHost(fmt.Sprintf("tar -xf \"%s\" -C \"%s\"", exportTar, workDir), 10*time.Minute); !ok {
		return fmt.Errorf("解压 rootfs 失败: %s", err)
	}

	// 注入最小 init + SSHD 配置
	initScript := `#!/bin/sh
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sys /sys 2>/dev/null
mount -t devtmpfs dev /dev 2>/dev/null || true
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts 2>/dev/null
[ -e /dev/ptmx ] || ln -s /dev/pts/ptmx /dev/ptmx 2>/dev/null
ip link set lo up 2>/dev/null || true
[ -x /sbin/udhcpc ] && udhcpc -i eth0 -q -f -n 2>/dev/null &
if [ -x /usr/sbin/sshd ]; then
    mkdir -p /var/run/sshd
    /usr/bin/ssh-keygen -A 2>/dev/null || true
    /usr/sbin/sshd -D &
fi
exec /bin/sh
`
	d.writeRemoteFile(filepath.ToSlash(filepath.Join(workDir, "init")), initScript, 0755)
	sshdConfig := "Port 22\nPermitRootLogin yes\nPasswordAuthentication yes\nPermitEmptyPasswords no\nUsePAM no\n"
	d.execHost(fmt.Sprintf("mkdir -p \"%s/etc/ssh\"", workDir), 5*time.Second)
	d.writeRemoteFile(filepath.ToSlash(filepath.Join(workDir, "etc/ssh/sshd_config")), sshdConfig, 0644)

	// 生成密码哈希并写入 shadow
	if rootPass != "" {
		shadow := filepath.ToSlash(filepath.Join(workDir, "etc/shadow"))
		if err := d.writeShadowPassword(shadow, workDir, rootPass); err != nil {
			return fmt.Errorf("写入 root 密码失败: %v", err)
		}
	}

	// 构建 ext4 镜像
	d.execHost(fmt.Sprintf("truncate -s %dM \"%s\"", sizeMB, outRootfs), 15*time.Second)
	if ok, _, err := d.execHost(fmt.Sprintf("mkfs.ext4 -F -L rootfs -d \"%s\" \"%s\"", workDir, outRootfs),
		5*time.Minute); !ok {
		// 回退：mkfs + mount + cp
		mnt := outRootfs + ".mnt"
		d.execHost(fmt.Sprintf("mkfs.ext4 -F -L rootfs \"%s\" && mkdir -p \"%s\"", outRootfs, mnt), 2*time.Minute)
		if ok2, _, merr := d.execHost(fmt.Sprintf("mount -o loop \"%s\" \"%s\"", outRootfs, mnt), 10*time.Second); !ok2 {
			return fmt.Errorf("mkfs/mount 失败: %s %s", err, merr)
		}
		d.execHost(fmt.Sprintf("cp -a \"%s\"/. \"%s\"/", workDir, mnt), 20*time.Minute)
		d.execHost(fmt.Sprintf("sync && umount \"%s\" || umount -l \"%s\"; rmdir \"%s\" 2>/dev/null || true",
			mnt, mnt, mnt), 30*time.Second)
	}

	return nil
}

// writeRemoteFile 在宿主机（远程/本地）写入文本文件
func (d *SmolVMDriver) writeRemoteFile(path, content string, mode os.FileMode) {
	// 使用带引号的 here-doc：内容是字面量，不做变量展开。
	// 注意不能把 "$" 转义成 "\$"——带引号的 heredoc 里反斜杠是普通字符，
	// 转义会把密码哈希（$6$salt$hash）写成错误内容。
	cmd := fmt.Sprintf(
		"cat >\"%s\" <<'EOF_SMOLVM'\n%s\nEOF_SMOLVM\nchmod %o \"%s\"",
		path, content, mode, path)
	d.execHost(cmd, 10*time.Second)
}

// writeShadowPassword 生成 root 口令哈希并写入 rootfs 的 /etc/shadow。
//
// 不能直接把哈希拼进 `sh -c "... sed ..."`：哈希里的 $6$salt$hash 会被
// 内层 shell 当作变量展开，最终写进 shadow 的是一段残缺字符串，
// 表现为「改密成功但 SSH 登录始终拒绝」。
func (d *SmolVMDriver) writeShadowPassword(shadowPath, tmpDir, password string) error {
	okp, out, _ := d.execHost(fmt.Sprintf(
		"openssl passwd -6 '%s' 2>/dev/null || openssl passwd -1 '%s'",
		shEscape(password), shEscape(password)), 10*time.Second)
	if !okp {
		return fmt.Errorf("openssl 生成密文失败")
	}
	hash := strings.TrimSpace(out)
	lines := strings.Split(hash, "\n")
	if len(lines) > 0 {
		hash = lines[len(lines)-1]
	}
	if hash == "" {
		return fmt.Errorf("openssl 未返回密文")
	}

	linePath := filepath.ToSlash(filepath.Join(tmpDir, ".root-shadow-line"))
	restPath := filepath.ToSlash(filepath.Join(tmpDir, ".root-shadow-rest"))
	hashPath := filepath.ToSlash(filepath.Join(tmpDir, ".root-shadow-hash"))
	d.writeRemoteFile(hashPath, hash+"\n", 0600)
	// lastchg 必须写「今天」（epoch 天数）：写 0 会被 PAM/sshd 判为
	// 「密码已过期，必须先改密」，SSH 登录直接报 Password change required。
	genCmd := fmt.Sprintf(
		"sh -c 'H=$(cat \"%s\"); D=$(( $(date +%%s) / 86400 )); "+
			"printf \"root:%%s:%%s:0:99999:7:::\\n\" \"$H\" \"$D\" > \"%s\"; rm -f \"%s\"'",
		hashPath, linePath, hashPath)
	if ok, _, err := d.execHost(genCmd, 10*time.Second); !ok {
		return fmt.Errorf("生成 shadow 行失败: %s", err)
	}

	cmd := fmt.Sprintf(
		"sh -c 'if [ -f \"%s\" ]; then grep -v \"^root:\" \"%s\" > \"%s\"; cat \"%s\" \"%s\" > \"%s\"; rm -f \"%s\"; "+
			"else cp \"%s\" \"%s\"; fi; rm -f \"%s\"'",
		shadowPath, shadowPath, restPath,
		linePath, restPath, shadowPath, restPath,
		linePath, shadowPath, linePath)
	ok, _, err := d.execHost(cmd, 10*time.Second)
	if !ok {
		return fmt.Errorf("写入 shadow 失败: %s", err)
	}
	return nil
}

// ============================================================
// 网络：tap 设备
// ============================================================

func (d *SmolVMDriver) bindTap(cfg *model.VMConfig, up bool) error {
	tap := d.tapName(cfg.VmUUID)
	if !up {
		d.execHost(fmt.Sprintf("ip link set %s down 2>/dev/null || true", tap), 5*time.Second)
		d.execHost(fmt.Sprintf("for i in 1 2 3 4 5 6; do ip tuntap del dev %s mode tap 2>/dev/null && break; "+
			"ip link del %s 2>/dev/null && break; sleep 0.3; done", tap, tap), 10*time.Second)
		return nil
	}
	// 先清理同名残留 tap：Firecracker 自己也会 open tap，若上一次停止时未删除，
	// TUNSETIFF 会返回 EBUSY（Resource busy），装配阶段 400 而虚拟机实际未启动。
	d.execHost(fmt.Sprintf("ip link set %s down 2>/dev/null || true", tap), 5*time.Second)
	d.execHost(fmt.Sprintf("for i in 1 2 3 4 5 6; do ip tuntap del dev %s mode tap 2>/dev/null && break; "+
		"ip link del %s 2>/dev/null && break; sleep 0.3; done", tap, tap), 10*time.Second)
	d.execHost(fmt.Sprintf("ip tuntap add dev %s mode tap 2>/dev/null || true", tap), 5*time.Second)
	d.execHost(fmt.Sprintf("ip link set %s up", tap), 5*time.Second)

	bridge := d.Config.NetworkNat
	// 第一个网卡默认使用 NAT bridge；若有 NetworkPub 则覆盖（简化处理）
	_ = cfg
	if bridge == "" && d.Config.NetworkPub != "" {
		bridge = d.Config.NetworkPub
	}
	if bridge != "" {
		d.execHost(fmt.Sprintf(
			"ip link set %s master %s 2>/dev/null || brctl addif %s %s 2>/dev/null || true",
			tap, bridge, bridge, tap), 5*time.Second)
	}
	return nil
}

// ============================================================
// 核心接口实现
// ============================================================

// GetPower 获取虚拟机电源状态
func (d *SmolVMDriver) GetPower(vmUUID string) string {
	pid := d.readPid(vmUUID)
	if pid <= 0 || !d.pidAlive(pid) {
		return "已关机"
	}
	// 以 Firecracker 实例状态为准：仅凭 pid 存活会把「进程在但未 InstanceStart」
	// 的失败实例误报为运行中。远程模式经 SSH streamlocal 访问对端 UDS。
	code, resp, err := d.fcDo(d.vmSock(vmUUID), "GET", "/", nil)
	if err == nil && code == 200 {
		state, _ := resp["state"].(string)
		switch strings.ToLower(state) {
		case "running":
			return "运行中"
		case "paused":
			return "已暂停"
		case "not started":
			return "已关机"
		case "restarting":
			return "重启中"
		}
	}
	return "运行中"
}

func (d *SmolVMDriver) readPid(vmUUID string) int {
	ok, out, _ := d.execHost(fmt.Sprintf("cat \"%s\" 2>/dev/null || true", d.vmPidFile(vmUUID)), 5*time.Second)
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

func (d *SmolVMDriver) pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	ok, out, _ := d.execHost(fmt.Sprintf("kill -0 %d 2>/dev/null && echo YES || echo NO", pid),
		5*time.Second)
	return ok && strings.Contains(out, "YES")
}

// GuestIP 从实例串口日志中解析 guest 的 DHCP 地址。
//
// Firecracker 没有 guest 网络查询接口，内核启动时的 IP-Config 会把
// `ipaddr=10.x.x.x` 打到串口（即 fc.log），这是唯一可靠的地址来源。
func (d *SmolVMDriver) GuestIP(vmUUID string) string {
	logPath := filepath.ToSlash(filepath.Join(d.vmDir(vmUUID), "fc.log"))
	// 不能只 tail 固定行数：串口日志会被 Firecracker 的 API 访问日志迅速撑大，
	// 启动阶段的 ipaddr 行会被挤出窗口。取最后一条 ipaddr 记录（最后一次启动）。
	ok, out, _ := d.execHost(fmt.Sprintf("grep -h 'ipaddr=' \"%s\" 2>/dev/null | tail -n 1", logPath), 10*time.Second)
	if !ok {
		return ""
	}
	idx := strings.LastIndex(out, "ipaddr=")
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(out[idx+len("ipaddr="):])
	if len(fields) == 0 {
		return ""
	}
	ip := strings.Trim(fields[0], ",")
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

// killPid 终止指定进程
func (d *SmolVMDriver) killPid(pid int, force bool) {
	if pid <= 0 {
		return
	}
	sig := "-15"
	if force {
		sig = "-9"
	}
	d.execHost(fmt.Sprintf("kill %s %d 2>/dev/null || true", sig, pid), 5*time.Second)
}

// killOrphan 按 socket 路径回收遗留的 firecracker 进程。
//
// 启动失败（装配 400）或 pid 文件被清理后，进程可能仍在运行并持有 tap，
// 后续启动会因 TUNSETIFF EBUSY 永久失败。pid 文件不可靠，必须按命令行兜底。
func (d *SmolVMDriver) killOrphan(sock string) {
	// 注意 "[f]irecracker"：pkill -f 会匹配到远程 shell 自身的命令行，
	// 直接写 firecracker 会让它把自己（以及当前 SSH 会话）一起杀掉。
	pattern := shellQuote("[f]irecracker --api-sock " + sock)
	d.execHost(fmt.Sprintf("pkill -9 -f %s 2>/dev/null || true; sleep 0.3", pattern), 10*time.Second)
}

// CreateVM 创建 microVM
func (d *SmolVMDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	d.ensureDirs()
	vmDir := d.vmDir(cfg.VmUUID)
	rootfs := filepath.ToSlash(filepath.Join(vmDir, "rootfs.ext4"))
	sock := d.vmSock(cfg.VmUUID)
	pidFile := d.vmPidFile(cfg.VmUUID)

	// 1) 构建 rootfs
	sizeMB := cfg.HddNum
	if sizeMB <= 0 {
		sizeMB = 8 * 1024
	} else {
		sizeMB = cfg.HddNum
	}
	d.execHost(fmt.Sprintf("mkdir -p \"%s\"", vmDir), 5*time.Second)
	if err := d.buildRootfs(cfg.OsName, rootfs, sizeMB, cfg.OsPass); err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("rootfs 构建失败: %v", err)}, nil
	}

	// 2) tap 设备（先回收可能遗留的实例，避免 tap 被占用）
	d.killOrphan(sock)
	d.bindTap(cfg, true)

	// 3) 启动 firecracker
	d.execHost(fmt.Sprintf("rm -f \"%s\"", sock), 5*time.Second)
	hv := d.hypervisorBin()
	startCmd := fmt.Sprintf("setsid %s --api-sock \"%s\" >\"%s/fc.log\" 2>&1 & echo $! > \"%s\"",
		hv, sock, vmDir, pidFile)
	if ok, _, err := d.execHost(startCmd, 10*time.Second); !ok {
		d.bindTap(cfg, false)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("启动 firecracker 失败: %s", err)}, nil
	}
	d.execHost(fmt.Sprintf("sleep 0.3; chmod 0600 \"%s\" 2>/dev/null || true", sock), 3*time.Second)

	// 4) FC API 装配
	if !d.waitSockReady(sock, 10*time.Second) {
		d.killPid(d.readPid(cfg.VmUUID), true)
		d.bindTap(cfg, false)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "firecracker socket 未就绪"}, nil
	}
	if err := d.assembleVM(cfg, sock, rootfs); err != nil {
		d.killPid(d.readPid(cfg.VmUUID), true)
		d.execHost(fmt.Sprintf("rm -f \"%s\" \"%s\"", sock, pidFile), 3*time.Second)
		d.bindTap(cfg, false)
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("FC API 装配失败: %v", err)}, nil
	}

	// 5) 写 vm.json
	d.saveVMJSON(cfg)

	// 6) 同步到内存
	d.mu.Lock()
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMCreate",
		Message: fmt.Sprintf("microVM %s 创建成功", cfg.VmUUID)}, nil
}

// assembleVM 通过 FC API 装配虚拟机
func (d *SmolVMDriver) assembleVM(cfg *model.VMConfig, sock, rootfs string) error {
	// init 指向 buildRootfs 注入的 /init：Docker 基础镜像通常没有可用的
	// /sbin/init（alpine 的 /sbin/init 依赖 openrc，debian 基础镜像无 systemd），
	// 继续按 /sbin/init 引导会出现「内核能启动、guest 无网无控制台」的假运行。
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off init=/init ip=dhcp"
	if code, _, err := d.fcDo(sock, "PUT", "/boot-source", map[string]interface{}{
		"kernel_image_path": d.kernelPath(), "boot_args": bootArgs}); err != nil || code >= 400 {
		return fmt.Errorf("/boot-source code=%d err=%v", code, err)
	}
	if code, _, err := d.fcDo(sock, "PUT", "/drives/rootfs", map[string]interface{}{
		"drive_id": "rootfs", "path_on_host": rootfs,
		"is_root_device": true, "is_read_only": false}); err != nil || code >= 400 {
		return fmt.Errorf("/drives/rootfs code=%d err=%v", code, err)
	}

	// 数据盘：Firecracker 以额外 drive 形式挂载（已挂载的才下发）
	for name, hdd := range cfg.HddAll {
		if hdd.HddFlag != 1 {
			continue
		}
		p := hdd.HddPath
		if p == "" {
			p = filepath.ToSlash(filepath.Join(d.vmDir(cfg.VmUUID), name+".ext4"))
		}
		if code, _, err := d.fcDo(sock, "PUT", "/drives/"+name, map[string]interface{}{
			"drive_id": name, "path_on_host": p,
			"is_root_device": false, "is_read_only": false}); err != nil || code >= 400 {
			return fmt.Errorf("/drives/%s code=%d err=%v", name, code, err)
		}
	}
	mac := ""
	for _, nic := range cfg.NicAll {
		mac = nic.MacAddr
		break
	}
	iface := map[string]interface{}{
		"iface_id": "eth0", "host_dev_name": d.tapName(cfg.VmUUID),
	}
	if mac != "" {
		iface["guest_mac"] = mac
	}
	if code, _, err := d.fcDo(sock, "PUT", "/network-interfaces/eth0", iface); err != nil || code >= 400 {
		return fmt.Errorf("/network-interfaces/eth0 code=%d err=%v", code, err)
	}
	vcpu := cfg.CpuNum
	if vcpu <= 0 {
		vcpu = 1
	}
	mem := cfg.MemNum
	if mem <= 0 {
		mem = 512
	}
	if code, _, err := d.fcDo(sock, "PUT", "/machine-config", map[string]interface{}{
		"vcpu_count": vcpu, "mem_size_mib": mem, "smt": false}); err != nil || code >= 400 {
		return fmt.Errorf("/machine-config code=%d err=%v", code, err)
	}
	if code, _, err := d.fcDo(sock, "PUT", "/actions", map[string]interface{}{
		"action_type": "InstanceStart"}); err != nil || code >= 400 {
		return fmt.Errorf("/actions code=%d err=%v", code, err)
	}
	return nil
}

// saveVMJSON 持久化 VM 配置
func (d *SmolVMDriver) saveVMJSON(cfg *model.VMConfig) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	path := filepath.ToSlash(filepath.Join(d.vmDir(cfg.VmUUID), "vm.json"))
	d.writeRemoteFile(path, string(data), 0644)
}

// loadVMJSON 读回 VM 配置
func (d *SmolVMDriver) loadVMJSON(vmUUID string) *model.VMConfig {
	path := filepath.ToSlash(filepath.Join(d.vmDir(vmUUID), "vm.json"))
	ok, out, _ := d.execHost(fmt.Sprintf("cat \"%s\" 2>/dev/null || true", path), 5*time.Second)
	if !ok || strings.TrimSpace(out) == "" {
		return nil
	}
	var cfg model.VMConfig
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return nil
	}
	return &cfg
}

// ensureDirs 确保基础目录存在
func (d *SmolVMDriver) ensureDirs() {
	d.once.Do(func() {
		d.execHost(fmt.Sprintf("mkdir -p \"%s\" && chmod 0700 \"%s\"", d.sockRoot, d.sockRoot), 5*time.Second)
		if d.Config.ExternPath != "" {
			d.execHost(fmt.Sprintf("mkdir -p \"%s\"", d.Config.ExternPath), 5*time.Second)
		}
		if d.Config.BackupPath != "" {
			d.execHost(fmt.Sprintf("mkdir -p \"%s\"", d.Config.BackupPath), 5*time.Second)
		}
	})
}

// DeleteVM 删除 microVM
func (d *SmolVMDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	d.mu.RLock()
	cfg, ok := d.VmSaving[vmUUID]
	d.mu.RUnlock()
	if !ok {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: "microVM 不存在"}, nil
	}

	pid := d.readPid(vmUUID)
	if pid > 0 && d.pidAlive(pid) {
		d.killPid(pid, true)
	}
	d.execHost(fmt.Sprintf("rm -f \"%s\" \"%s\"", d.vmSock(vmUUID), d.vmPidFile(vmUUID)), 5*time.Second)
	d.bindTap(cfg, false)
	d.execHost(fmt.Sprintf("rm -rf \"%s\"", d.vmDir(vmUUID)), 30*time.Second)

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDelete",
		Message: "microVM 已删除"}, nil
}

// PowerControl 电源操作
func (d *SmolVMDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	d.mu.RLock()
	cfg := d.VmSaving[vmUUID]
	d.mu.RUnlock()
	if cfg == nil {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: "microVM 不存在"}, nil
	}

	// 先设置中间状态
	if newState, ok := PowerActionMap[action]; ok {
		cfg.VmFlag = newState
	}

	sock := d.vmSock(vmUUID)
	pid := d.readPid(vmUUID)

	switch action {
	case model.VMPowerSStart:
		if pid > 0 && d.pidAlive(pid) {
			d.fcDo(sock, "PATCH", "/vm", map[string]interface{}{"state": "Resumed"})
		} else {
			d.ensureDirs()
			d.killOrphan(sock)
			d.bindTap(cfg, true)
			d.execHost(fmt.Sprintf("rm -f \"%s\"", sock), 3*time.Second)
			startCmd := fmt.Sprintf("setsid %s --api-sock \"%s\" >\"%s/fc.log\" 2>&1 & echo $! > \"%s\"",
				d.hypervisorBin(), sock, d.vmDir(vmUUID), d.vmPidFile(vmUUID))
			d.execHost(startCmd, 10*time.Second)
			d.execHost(fmt.Sprintf("sleep 0.3; chmod 0600 \"%s\" 2>/dev/null || true", sock), 3*time.Second)
			if !d.waitSockReady(sock, 10*time.Second) {
				return &model.ZMessage{Success: false, Action: "VMPowers",
					Message: "socket 未就绪"}, nil
			}
			if err := d.assembleVM(cfg, sock, filepath.ToSlash(filepath.Join(d.vmDir(vmUUID), "rootfs.ext4"))); err != nil {
				// 装配失败必须回收 firecracker 进程：否则 pid 存活会让
				// GetPower 误报「运行中」，而实例实际从未 InstanceStart。
				d.killPid(d.readPid(vmUUID), true)
				d.execHost(fmt.Sprintf("rm -f \"%s\" \"%s\"", sock, d.vmPidFile(vmUUID)), 5*time.Second)
				return &model.ZMessage{Success: false, Action: "VMPowers",
					Message: fmt.Sprintf("启动失败: %v", err)}, nil
			}
		}
	case model.VMPowerHClose:
		if pid > 0 {
			d.killPid(pid, true)
		}
		d.execHost(fmt.Sprintf("rm -f \"%s\" \"%s\"", sock, d.vmPidFile(vmUUID)), 5*time.Second)
		d.bindTap(cfg, false)
	case model.VMPowerSClose:
		d.fcDo(sock, "PUT", "/actions", map[string]interface{}{"action_type": "SendCtrlAltDel"})
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if pid > 0 && !d.pidAlive(pid) {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if pid > 0 && d.pidAlive(pid) {
			d.killPid(pid, true)
		}
		d.execHost(fmt.Sprintf("rm -f \"%s\" \"%s\"", sock, d.vmPidFile(vmUUID)), 5*time.Second)
		// 关机后删除 tap，避免下次启动时 TUNSETIFF 冲突
		d.bindTap(cfg, false)
	case model.VMPowerSReset, model.VMPowerHReset:
		if action == model.VMPowerSReset {
			d.PowerControl(vmUUID, model.VMPowerSClose)
		} else {
			d.PowerControl(vmUUID, model.VMPowerHClose)
		}
		time.Sleep(1 * time.Second)
		d.PowerControl(vmUUID, model.VMPowerSStart)
	case model.VMPowerAPause:
		d.fcDo(sock, "PATCH", "/vm", map[string]interface{}{"state": "Paused"})
	case model.VMPowerAWaked:
		d.fcDo(sock, "PATCH", "/vm", map[string]interface{}{"state": "Resumed"})
	}

	// 操作成功后同步最终状态：只写中间态（ON_OPEN 等）会让前端与状态接口
	// 在下一次 60 秒调度前一直显示「启动中/关机中」。
	switch action {
	case model.VMPowerSStart, model.VMPowerAWaked:
		cfg.VmFlag = model.VMPowerStarted
	case model.VMPowerAPause:
		cfg.VmFlag = model.VMPowerSuspend
	case model.VMPowerSClose, model.VMPowerHClose:
		cfg.VmFlag = model.VMPowerStopped
	}

	return &model.ZMessage{Success: true, Action: "VMPowers",
		Message: "电源操作已执行"}, nil
}

// SupportsGracefulShutdown 软关机通过 Firecracker 的 SendCtrlAltDel 触发。
func (d *SmolVMDriver) SupportsGracefulShutdown() bool { return true }

// ScanVMs 扫描 extern_path 下的 microVM 目录
func (d *SmolVMDriver) ScanVMs() (*model.ZMessage, error) {
	base := d.Config.ExternPath
	if base == "" {
		return &model.ZMessage{Success: false, Action: "VMDetect",
			Message: "extern_path 未配置"}, nil
	}
	prefix := d.Config.FilterName
	ok, out, _ := d.execHost(fmt.Sprintf("ls -1 \"%s\" 2>/dev/null || true", base), 15*time.Second)
	if !ok {
		return &model.ZMessage{Success: true, Action: "VMDetect",
			Message: "扫描完成（目录不存在）"}, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(out))
	scanned, added := 0, 0
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == "" {
			continue
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		scanned++

		d.mu.RLock()
		_, existed := d.VmSaving[name]
		d.mu.RUnlock()
		if existed {
			continue
		}

		vmCfg := d.loadVMJSON(name)
		if vmCfg == nil {
			// 目录下没有 vm.json 的（如 ExternPath 内的 backup / images 子目录）
			// 不是虚拟机。原实现会为其凭空创建一条配置，导致实例列表出现
			// "backup"/"images" 假实例，误点删除还会删掉备份/镜像目录。
			continue
		}
		d.mu.Lock()
		d.VmSaving[name] = vmCfg
		d.mu.Unlock()
		added++
	}
	return &model.ZMessage{Success: true, Action: "VMDetect",
		Message: fmt.Sprintf("扫描完成：共 %d 个，新增 %d 个", scanned, added)}, nil
}

// SetPassword 设置 root 密码（在线走 SSH，离线挂载 rootfs 改 shadow）
func (d *SmolVMDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	// 标识会参与宿主机目录与命令拼接，必须先校验
	if err := safeIdent(vmUUID, 128); err != nil {
		return &model.ZMessage{Success: false, Action: "Password",
			Message: "microVM 标识无效: " + err.Error()}, nil
	}
	d.mu.RLock()
	cfg, ok := d.VmSaving[vmUUID]
	d.mu.RUnlock()
	if !ok {
		return &model.ZMessage{Success: false, Action: "Password",
			Message: "microVM 不存在"}, nil
	}

	pid := d.readPid(vmUUID)
	if pid > 0 && d.pidAlive(pid) {
		// 在线改密（通过宿主机 ssh 到 VM IP）
		vmIP := ""
		for _, nic := range cfg.NicAll {
			if nic.IP4Addr != "" {
				vmIP = nic.IP4Addr
				break
			}
		}
		if vmIP != "" && net.ParseIP(strings.TrimSpace(vmIP)) != nil {
			// 口令进入本地 shell 命令串，必须转义；
			// IP 只接受合法地址，避免被拼成主机名或附加参数。
			cmd := fmt.Sprintf(
				"sshpass -p '%s' ssh -o StrictHostKeyChecking=no root@%s \"echo 'root:%s' | chpasswd\"",
				shEscape(password), vmIP, shEscape(password))
			if ok2, _, _ := d.execHost(cmd, 30*time.Second); ok2 {
				cfg.OsPass = password
				return &model.ZMessage{Success: true, Action: "Password",
					Message: "密码已更新（在线）"}, nil
			}
		}
		// 在线不可用（未分配 IP / 分配的 IP 与 DHCP 实际地址不一致 / sshd 未启动）
		// 时回退离线改密：停止实例 → loop 挂载 rootfs 改 shadow → 重新启动。
		// microVM 的地址由 guest DHCP 获取，控制面分配的 IP 未必等于实际地址，
		// 只做在线改密会让「有 IP 但改不了密码」变成常态。
		_, _ = d.PowerControl(vmUUID, model.VMPowerSClose)
		if msg := d.setPasswordOffline(vmUUID, password); msg != nil {
			_, _ = d.PowerControl(vmUUID, model.VMPowerSStart)
			return msg, nil
		}
		_, _ = d.PowerControl(vmUUID, model.VMPowerSStart)
		cfg.OsPass = password
		return &model.ZMessage{Success: true, Action: "Password",
			Message: "密码已更新（离线，实例已重新启动）"}, nil
	}

	if msg := d.setPasswordOffline(vmUUID, password); msg != nil {
		return msg, nil
	}
	cfg.OsPass = password
	return &model.ZMessage{Success: true, Action: "Password",
		Message: "密码已更新（离线）"}, nil
}

// setPasswordOffline 挂载 rootfs 直接改写 /etc/shadow（实例必须处于停止状态）
func (d *SmolVMDriver) setPasswordOffline(vmUUID, password string) *model.ZMessage {
	rootfs := filepath.ToSlash(filepath.Join(d.vmDir(vmUUID), "rootfs.ext4"))
	mnt := filepath.ToSlash(filepath.Join(d.vmDir(vmUUID), ".mnt-passwd"))
	d.execHost(fmt.Sprintf("mkdir -p \"%s\"", mnt), 5*time.Second)
	if ok2, _, err := d.execHost(fmt.Sprintf("mount -o loop \"%s\" \"%s\"", rootfs, mnt), 10*time.Second); !ok2 {
		return &model.ZMessage{Success: false, Action: "Password",
			Message: fmt.Sprintf("挂载 rootfs 失败: %s", err)}
	}
	defer func() {
		d.execHost(fmt.Sprintf("sync && umount \"%s\" || umount -l \"%s\"; rmdir \"%s\" 2>/dev/null || true",
			mnt, mnt, mnt), 10*time.Second)
	}()
	shadow := filepath.ToSlash(filepath.Join(mnt, "etc/shadow"))
	if err := d.writeShadowPassword(shadow, d.vmDir(vmUUID), password); err != nil {
		return &model.ZMessage{Success: false, Action: "Password", Message: err.Error()}
	}
	return nil
}

// CreateBackup 备份（tar -czf extern_path/<vm>）
func (d *SmolVMDriver) CreateBackup(vmUUID string, tips string) (*model.ZMessage, error) {
	d.mu.RLock()
	cfg, ok := d.VmSaving[vmUUID]
	d.mu.RUnlock()
	if !ok {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: "microVM 不存在"}, nil
	}
	_ = cfg
	if d.Config.BackupPath == "" {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: "backup_path 未配置"}, nil
	}
	wasRunning := false
	pid := d.readPid(vmUUID)
	if pid > 0 && d.pidAlive(pid) {
		wasRunning = true
		d.PowerControl(vmUUID, model.VMPowerSClose)
	}
	// 备份名必须使用 <vm_uuid>-<时间戳>：DeleteBackup 与基类 ScanBackups
	// 都以该前缀识别归属，原实现用下划线拼接导致备份无法被列出/删除。
	bakFile := fmt.Sprintf("%s-%s.tar.gz", vmUUID, time.Now().Format("20060102_150405"))
	bakPath := filepath.ToSlash(filepath.Join(d.Config.BackupPath, bakFile))
	d.execHost(fmt.Sprintf("mkdir -p \"%s\"", d.Config.BackupPath), 5*time.Second)
	cmd := fmt.Sprintf("tar -C \"%s\" -czf \"%s\" \"%s\"", d.Config.ExternPath, bakPath, vmUUID)
	if ok2, _, err := d.execHost(cmd, 60*time.Minute); !ok2 {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: fmt.Sprintf("备份打包失败: %s", err)}, nil
	}
	if wasRunning {
		d.PowerControl(vmUUID, model.VMPowerSStart)
	}
	// 记录到虚拟机配置：备份列表接口直接返回 cfg.Backups，
	// 原实现只返回文件名、不落记录，导致备份成功但列表始终为空。
	entry := model.VMBackup{BackupName: bakFile, BackupTime: time.Now(), BackupTips: tips}
	d.mu.Lock()
	if cur, exists := d.VmSaving[vmUUID]; exists {
		cur.Backups = append(cur.Backups, entry)
		d.saveVMJSON(cur)
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "VMBackup",
		Message: fmt.Sprintf("microVM 备份成功: %s", bakFile),
		Results: map[string]string{"backup_file": bakFile, "backup_path": bakPath, "tips": tips}}, nil
}

// SyncBackups 以宿主机备份目录为准重建备份列表。
//
// 基类 ScanBackups 用 os.ReadDir 读本机目录，远程主机永远读不到；
// 备份列表接口会优先调用该方法（存在时）。
func (d *SmolVMDriver) SyncBackups(vmUUID string) {
	dir := strings.TrimSpace(d.Config.BackupPath)
	if dir == "" {
		return
	}
	pattern := shellQuote(vmUUID + "-*")
	cmd := fmt.Sprintf("find %s -maxdepth 1 -type f -name %s -printf '%%f\\t%%T@\\n'",
		shellQuote(dir), pattern)
	ok, out, _ := d.execHost(cmd, 20*time.Second)
	if !ok {
		return
	}
	backups := []model.VMBackup{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" || !strings.HasPrefix(name, vmUUID+"-") {
			continue
		}
		bt := time.Now()
		if len(parts) == 2 {
			var sec float64
			if _, err := fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &sec); err == nil && sec > 0 {
				bt = time.Unix(int64(sec), 0)
			}
		}
		backups = append(backups, model.VMBackup{BackupName: name, BackupTime: bt})
	}
	d.mu.Lock()
	if cur, exists := d.VmSaving[vmUUID]; exists {
		cur.Backups = backups
		d.saveVMJSON(cur)
	}
	d.mu.Unlock()
}

// RestoreBackup 恢复
func (d *SmolVMDriver) RestoreBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	bakPath := filepath.ToSlash(filepath.Join(d.Config.BackupPath, backupName))
	ok, out, _ := d.execHost(fmt.Sprintf("test -f \"%s\" && echo YES || echo NO", bakPath), 5*time.Second)
	if !ok || !strings.Contains(out, "YES") {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("备份不存在: %s", bakPath)}, nil
	}
	pid := d.readPid(vmUUID)
	if pid > 0 && d.pidAlive(pid) {
		d.killPid(pid, true)
	}
	d.execHost(fmt.Sprintf("rm -f \"%s\" \"%s\"", d.vmSock(vmUUID), d.vmPidFile(vmUUID)), 5*time.Second)
	d.execHost(fmt.Sprintf("rm -rf \"%s\"", d.vmDir(vmUUID)), 30*time.Second)
	if ok2, _, err := d.execHost(fmt.Sprintf("tar -C \"%s\" -xzf \"%s\"",
		d.Config.ExternPath, bakPath), 60*time.Minute); !ok2 {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("解压备份失败: %s", err)}, nil
	}

	cfg := d.loadVMJSON(vmUUID)
	if cfg == nil {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: "备份中未包含 vm.json"}, nil
	}
	d.mu.Lock()
	d.VmSaving[vmUUID] = cfg
	d.mu.Unlock()
	return d.PowerControl(vmUUID, model.VMPowerSStart)
}

// DeleteBackup 删除备份
func (d *SmolVMDriver) DeleteBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	// 只取文件名本身并绑定虚拟机前缀：
	// 原实现直接把请求值拼进 rm -f，可用 ../../ 删除备份目录之外的文件。
	name, err := sanitizeFileName(backupName)
	if err != nil || !strings.HasPrefix(name, vmUUID+"-") {
		return &model.ZMessage{Success: false, Action: "RMBackup",
			Message: "备份名称无效或与目标虚拟机不匹配"}, nil
	}
	if strings.TrimSpace(d.Config.BackupPath) == "" {
		return &model.ZMessage{Success: false, Action: "RMBackup",
			Message: "主机未配置备份路径"}, nil
	}
	path := filepath.ToSlash(filepath.Join(d.Config.BackupPath, name))
	d.execHost(fmt.Sprintf("rm -f \"%s\"", path), 5*time.Second)
	return &model.ZMessage{Success: true, Action: "RMBackup",
		Message: "备份已删除"}, nil
}

// Crontab 定时任务：同步所有 VM 的电源状态
func (d *SmolVMDriver) Crontab() bool {
	d.mu.RLock()
	snapshot := make(map[string]*model.VMConfig, len(d.VmSaving))
	for k, v := range d.VmSaving {
		snapshot[k] = v
	}
	d.mu.RUnlock()

	for uuid, cfg := range snapshot {
		state := d.GetPower(uuid)
		if newState, ok := PowerStatusMap[state]; ok {
			cfg.VmFlag = newState
		}
	}
	return true
}

// ReinstallVM 重装系统：停止VM，删除旧rootfs，重新构建，启动VM
func (d *SmolVMDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	vmUUID := cfg.VmUUID
	rootfs := filepath.ToSlash(filepath.Join(d.vmDir(vmUUID), "rootfs.ext4"))

	// 1) 停止虚拟机
	pid := d.readPid(vmUUID)
	if pid > 0 && d.pidAlive(pid) {
		d.PowerControl(vmUUID, model.VMPowerSClose)
		time.Sleep(2 * time.Second)
		// 强制停止
		d.killPid(pid, true)
		time.Sleep(1 * time.Second)
	}

	// 2) 删除旧 rootfs
	d.execHost(fmt.Sprintf("rm -f \"%s\"", rootfs), 5*time.Second)

	// 3) 重新构建 rootfs
	sizeMB := cfg.HddNum
	if sizeMB <= 0 {
		sizeMB = 8 * 1024
	}
	if err := d.buildRootfs(cfg.OsName, rootfs, sizeMB, cfg.OsPass); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: fmt.Sprintf("rootfs 构建失败: %v", err)}, nil
	}

	// 4) 重新启动虚拟机
	d.execHost(fmt.Sprintf("rm -f \"%s\"", d.vmSock(vmUUID)), 5*time.Second)
	if _, err := d.PowerControl(vmUUID, model.VMPowerSStart); err != nil {
		return &model.ZMessage{Success: false, Action: "VMReinstall",
			Message: fmt.Sprintf("启动虚拟机失败: %v", err)}, nil
	}

	return &model.ZMessage{Success: true, Action: "VMReinstall",
		Message: "系统重装成功"}, nil
}
