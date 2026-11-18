// Package driver 容器平台（Docker / LXD）宿主机命令执行。
//
// 容器类平台通过命令行管理，命令必须落在「拥有该运行时的机器」上：
//   - server_addr 为空 / localhost / 127.0.0.1：agent 与运行时同机，直接本地执行；
//   - server_addr 指向远程主机：通过 SSH 在远端执行，否则本地没有 docker/lxc
//     命令时所有操作会直接失败（历史实现只在 GetStatus 做了远程判断）。
package driver

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"
)

// execHostProc 在宿主机执行命令并返回 stdout。
//
// stdin 非空时写入远端/本地进程标准输入，用于 chpasswd 等需要管道输入的
// 场景。命令以参数数组传递，远程模式下逐参数做 shell 转义后再拼接。
func (b *BaseDriver) execHostProc(args []string, stdin string, timeout time.Duration) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	if isRemoteTarget(b.Config) {
		cfg := sshConfigFromHS(b.Config)
		cfg.Timeout = timeout
		quoted := make([]string, 0, len(args))
		for _, a := range args {
			quoted = append(quoted, shellQuote(a))
		}
		cmd := strings.Join(quoted, " ")
		res, err := RunSSHCommandInput(cfg, cmd, stdin)
		if err != nil {
			return "", fmt.Errorf("远程执行 %s 失败: %w", args[0], err)
		}
		if res == nil {
			return "", fmt.Errorf("远程执行 %s 无返回", args[0])
		}
		if res.ExitCode != 0 {
			msg := strings.TrimSpace(res.Stderr)
			if msg == "" {
				msg = strings.TrimSpace(res.Stdout)
			}
			return res.Stdout, fmt.Errorf("远程命令执行失败（exit=%d）: %s", res.ExitCode, msg)
		}
		return res.Stdout, nil
	}

	return runWithStdin(args, stdin, timeout)
}

// execHostArgs 无 stdin 版本的宿主机命令执行。
func (b *BaseDriver) execHostArgs(args []string, timeout time.Duration) (string, error) {
	return b.execHostProc(args, "", timeout)
}

// execHostShell 以 shell 脚本形式在宿主机执行（支持管道、重定向、here-doc）。
func (b *BaseDriver) execHostShell(script string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if isRemoteTarget(b.Config) {
		cfg := sshConfigFromHS(b.Config)
		cfg.Timeout = timeout
		res, err := RunSSHCommand(cfg, script)
		if err != nil {
			return "", fmt.Errorf("远程执行失败: %w", err)
		}
		if res == nil {
			return "", fmt.Errorf("远程执行无返回")
		}
		if res.ExitCode != 0 {
			msg := strings.TrimSpace(res.Stderr)
			if msg == "" {
				msg = strings.TrimSpace(res.Stdout)
			}
			return res.Stdout, fmt.Errorf("远程命令执行失败（exit=%d）: %s", res.ExitCode, msg)
		}
		return res.Stdout, nil
	}
	return runWithStdin([]string{"sh", "-c", script}, "", timeout)
}

// hostFileExists 判断宿主机上的文件是否存在。
func (b *BaseDriver) hostFileExists(path string) bool {
	if isRemoteTarget(b.Config) {
		_, err := b.execHostArgs([]string{"test", "-f", path}, 10*time.Second)
		return err == nil
	}
	_, err := os.Stat(path)
	return err == nil
}

// writeHostFile 在宿主机写入文本文件（本地直接写，远程走 here-doc）。
func (b *BaseDriver) writeHostFile(path, content string, mode os.FileMode) error {
	if isRemoteTarget(b.Config) {
		script := fmt.Sprintf("cat > %s <<'EOF_HOSTAGENT'\n%s\nEOF_HOSTAGENT\nchmod %04o %s",
			shellQuote(path), content, mode, shellQuote(path))
		_, err := b.execHostShell(script, 20*time.Second)
		return err
	}
	return os.WriteFile(path, []byte(content), mode)
}

// readHostFileBase64 读取宿主机文件（远程用 base64 传输，避免二进制损坏）。
func (b *BaseDriver) readHostFileBase64(path string) ([]byte, error) {
	if isRemoteTarget(b.Config) {
		out, err := b.execHostArgs([]string{"base64", "-w0", path}, 60*time.Second)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	}
	return os.ReadFile(path)
}
