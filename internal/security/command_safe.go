// Package security 命令执行安全模块
// 提供统一的命令执行入口，防止命令注入
package security

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// allowedCommands 允许执行的命令名（段级白名单）。
//
// 这里比对的是「每一段 shell 命令的首个词」，而不是整串字符串前缀。
// 旧实现用 strings.HasPrefix 判断整串前缀，`kill 1; rm -rf /` 因为以
// `kill ` 开头会被整体放行，白名单形同虚设。
var allowedCommands = map[string]bool{
	// 容器 / 虚拟机
	"docker": true, "firecracker": true, "cloud-hypervisor": true,
	"qemu-img": true, "qemu-system-x86_64": true, "qemu-system-aarch64": true,
	"qm": true, "pvesh": true, "virsh": true, "VBoxManage": true, "vmrest": true,
	// 端口转发与网络
	"socat": true, "nohup": true, "ip": true, "iptables": true, "ip6tables": true,
	"iptables-save": true, "iptables-restore": true, "nft": true,
	"brctl": true, "bridge": true, "tc": true, "ifconfig": true, "route": true,
	// 进程
	"kill": true, "ps": true, "pgrep": true, "pkill": true, "top": true, "free": true,
	"nproc": true, "uptime": true, "uname": true, "dmesg": true, "lsmod": true,
	// 文件
	"mkdir": true, "rm": true, "cp": true, "mv": true, "cat": true, "ls": true,
	"chmod": true, "chown": true, "tar": true, "truncate": true, "sync": true,
	"gunzip": true, "gzip": true, "head": true, "tail": true, "sed": true,
	"grep": true, "awk": true, "cut": true, "sort": true, "uniq": true, "wc": true,
	"stat": true, "find": true, "df": true, "du": true, "lsblk": true, "blkid": true,
	// 磁盘
	"fdisk": true, "parted": true, "mount": true, "umount": true,
	"mkfs.ext4": true, "mkfs.xfs": true, "mkfs.btrfs": true,
	// 系统与服务
	"systemctl": true, "service": true, "sysctl": true, "modprobe": true,
	"dpkg": true, "apt": true, "apt-get": true, "yum": true, "dnf": true,
	"openssl": true, "ssh-keygen": true, "which": true, "command": true,
	// 其它
	"curl": true, "wget": true, "echo": true, "test": true, "sleep": true,
	"true": true, "false": true, "expr": true, "printf": true, "hostname": true,
	"id": true, "whoami": true, "ss": true, "netstat": true, "ping": true,
}

var (
	// reCmdSeparator 命令分隔符：分号、与/或运算、单个与号、管道、换行。
	// 单字符横向制表符不算分隔符，但会在入口处直接拒绝（见 ValidateCmd）。
	reCmdSeparator = regexp.MustCompile(`[;&|\n\r]+`)
	// reCommandName 合法命令名形态（防止用路径、反斜杠转义绕过比对）。
	reCommandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	// reRedirectOnly 纯重定向段（如 `> /dev/null`、`2>/dev/null`）。
	reRedirectOnly = regexp.MustCompile(`^[0-9]?>>?&?[0-9]*$`)
)

// ValidateIP 校验字符串是否为合法的 IP 地址（IPv4 / IPv6）。
// 用于把用户可控的值拼进 shell 命令之前的前置校验。
func ValidateIP(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("IP 地址为空")
	}
	if net.ParseIP(s) == nil {
		// 截断后再拼进错误信息：输入可能长达数 KB，完整回显会污染日志
		return fmt.Errorf("非法 IP 地址: %s", truncate(s, 64))
	}
	return nil
}

// IsSafeCmd 检查命令字符串是否全部由白名单命令组成
func IsSafeCmd(cmd string) bool {
	return ValidateCmd(cmd, true) == nil
}

// ValidateCmd 验证 shell 命令是否安全。
//
// 校验分四层：
//  1. 控制字符（换行、回车、制表）与命令替换（反引号、$()）一律拒绝；
//  2. 归一化重定向写法（2>&1 / &>）后按 ; & | 切分为多段；
//  3. 每一段的首个词必须是白名单命令，杜绝「白名单前缀 + 分号注入」；
//  4. allowPipe 为 false 时禁止管道。
func ValidateCmd(cmd string, allowPipe bool) error {
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("命令为空")
	}

	// 控制字符：tab 在 shell 中等价于空格分隔，常被用来绕过基于空格的过滤
	if strings.ContainsAny(cmd, "\n\r\t") {
		return fmt.Errorf("命令包含控制字符: %s", truncate(cmd, 80))
	}
	if strings.Contains(cmd, "`") {
		return fmt.Errorf("命令包含反引号命令替换: %s", truncate(cmd, 80))
	}
	if strings.Contains(cmd, "$(") || strings.Contains(cmd, "${") {
		return fmt.Errorf("命令包含命令替换或变量展开: %s", truncate(cmd, 80))
	}
	if !allowPipe && strings.Contains(cmd, "|") {
		return fmt.Errorf("命令包含管道符（未启用allowPipe）: %s", truncate(cmd, 80))
	}

	// 命令串联符一律禁止。
	//
	// 仅靠「每段首命令在白名单」挡不住 `kill 1; rm -rf /`：
	// rm 本身是白名单命令（删除临时文件需要）。真正要拦的是
	// 「一条命令变成多条命令」这个动作本身。
	if strings.Contains(cmd, ";") {
		return fmt.Errorf("命令包含分号（多条命令串联）: %s", truncate(cmd, 80))
	}
	if strings.Contains(cmd, "&&") || strings.Contains(cmd, "||") {
		return fmt.Errorf("命令包含与/或运算串联: %s", truncate(cmd, 80))
	}

	// 重定向里的 `2>&1` 含 `&`，会被误当成命令分隔符，先归一化掉
	normalized := cmd
	for _, redir := range []string{"2>&1", "1>&2", ">&2", "&>"} {
		normalized = strings.ReplaceAll(normalized, redir, " ")
	}

	// 单个 `&` 只允许出现在命令末尾（后台执行，如 `nohup socat ... &`）。
	// 出现多次或位于中间，意味着后面还挂着别的命令。
	if idx := strings.Index(normalized, "&"); idx >= 0 {
		if strings.Contains(normalized[idx+1:], "&") {
			return fmt.Errorf("命令包含多个后台符: %s", truncate(cmd, 80))
		}
		if strings.TrimSpace(normalized[idx+1:]) != "" {
			return fmt.Errorf("后台符后不允许再接命令: %s", truncate(cmd, 80))
		}
	}

	for _, segment := range reCmdSeparator.Split(normalized, -1) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		// 纯重定向段（`> /dev/null` 被切开后可能只剩 `>`）
		if reRedirectOnly.MatchString(segment) {
			continue
		}
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if !reCommandName.MatchString(name) {
			return fmt.Errorf("命令名非法: %q（%s）", name, truncate(cmd, 80))
		}
		if !allowedCommands[name] {
			return fmt.Errorf("命令不在白名单内: %q（%s）", name, truncate(cmd, 80))
		}
	}
	return nil
}

// SafeExec 安全执行本地命令（列表形式，无 shell 注入风险）
func SafeExec(ctx context.Context, args []string) (string, string, error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("命令参数为空")
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	output, err := cmd.CombinedOutput()
	stdout := string(output)

	if err != nil {
		return stdout, "", fmt.Errorf("命令执行失败 [%s]: %w", strings.Join(args, " "), err)
	}
	return stdout, "", nil
}

// SafeExecWithTimeout 带超时的安全命令执行
func SafeExecWithTimeout(args []string, timeout time.Duration) (string, string, error) {
	return SafeExecWithTimeoutContext(context.Background(), args, timeout)
}

// SafeExecWithTimeoutContext 带超时与外部 context 的安全命令执行。
// 外部 context 被取消（请求中断 / 驱动关闭）时，子进程会一并被终止。
func SafeExecWithTimeoutContext(parent context.Context, args []string, timeout time.Duration) (string, string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return SafeExec(ctx, args)
}

// SafeShellExec 安全执行 shell 命令（带注入检查，仅限白名单命令）
func SafeShellExec(cmd string, timeout time.Duration, allowPipe bool) (string, string, error) {
	return SafeShellExecContext(context.Background(), cmd, timeout, allowPipe)
}

// SafeShellExecContext 带外部 context 的 shell 命令执行。
func SafeShellExecContext(parent context.Context, cmd string, timeout time.Duration, allowPipe bool) (string, string, error) {
	if err := ValidateCmd(cmd, allowPipe); err != nil {
		return "", "", fmt.Errorf("安全检查未通过: %w", err)
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	output, err := c.CombinedOutput()
	stdout := string(output)

	if err != nil {
		return stdout, "", fmt.Errorf("命令执行失败: %w", err)
	}
	return stdout, "", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
