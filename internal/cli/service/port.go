package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// SetPort 修改服务监听端口并重启。
func SetPort(port string) error {
	port = strings.TrimSpace(port)
	if port == "" {
		return errors.New("端口不能为空")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("端口必须是数字")
		}
	}

	switch runtime.GOOS {
	case "linux":
		unit := "/etc/systemd/system/" + linuxUnit + ".service"
		data, err := os.ReadFile(unit)
		if err != nil {
			return fmt.Errorf("读取服务文件失败（是否已安装主控？）: %w", err)
		}
		updated, err := replacePortFlag(string(data), port)
		if err != nil {
			return err
		}
		if err := writeWithSudo(unit, updated); err != nil {
			return err
		}
		if out, err := run("sudo", "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("daemon-reload 失败: %s", out)
		}
		if out, err := run("sudo", "systemctl", "restart", linuxUnit); err != nil {
			return fmt.Errorf("重启失败: %s", out)
		}
	case "darwin":
		plist := "/Library/LaunchDaemons/" + launchdName + ".plist"
		data, err := os.ReadFile(plist)
		if err != nil {
			return fmt.Errorf("读取 launchd 配置失败: %w", err)
		}
		updated, err := replacePortFlag(string(data), port)
		if err != nil {
			return err
		}
		if err := writeWithSudo(plist, updated); err != nil {
			return err
		}
		_, _ = run("sudo", "launchctl", "bootout", "system/"+launchdName)
		if out, err := run("sudo", "launchctl", "bootstrap", "system", plist); err != nil {
			return fmt.Errorf("重新加载失败: %s", out)
		}
	case "windows":
		return errors.New("Windows 请重新运行安装包并指定端口")
	default:
		return fmt.Errorf("当前系统 %s 不支持修改端口", runtime.GOOS)
	}

	fmt.Printf("端口已修改为 %s，服务已重启\n", port)
	return nil
}

func replacePortFlag(text, port string) (string, error) {
	rePort := regexp.MustCompile(`(--port[ =])\S+`)
	if rePort.MatchString(text) {
		return rePort.ReplaceAllString(text, "${1}"+port), nil
	}
	reExec := regexp.MustCompile(`(--data-dir[ =]\S+)`)
	if reExec.MatchString(text) {
		return reExec.ReplaceAllString(text, "${1} --port "+port), nil
	}
	return "", errors.New("服务定义中未找到 --port 或 --data-dir，请手动修改")
}

func writeWithSudo(path, content string) error {
	cmd := exec.Command("sudo", "tee", path)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}
