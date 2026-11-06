package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/appconfig"
)

var dataDirOverride string

// SetDataDir 设置运行目录（来自 --data-dir）。
func SetDataDir(dir string) {
	dataDirOverride = strings.TrimSpace(dir)
}

// DataDir 返回主控运行目录（与 CLI 保持一致）。
// 优先级：--data-dir > OPIDCS_DATA_DIR > 常见安装路径 > ./data。
func DataDir() string {
	if dataDirOverride != "" {
		return dataDirOverride
	}
	if dir := strings.TrimSpace(os.Getenv("OPIDCS_DATA_DIR")); dir != "" {
		return dir
	}
	for _, candidate := range installedDataDirs() {
		if _, err := os.Stat(filepath.Join(candidate, "data.yaml")); err == nil {
			return candidate
		}
	}
	return "./data"
}

func installedDataDirs() []string {
	switch runtime.GOOS {
	case "windows":
		if base := os.Getenv("ProgramData"); base != "" {
			return []string{filepath.Join(base, "OpenIDCS", "HostAgent", "data")}
		}
	case "darwin":
		return []string{"/usr/local/openidcs/data"}
	default:
		return []string{"/var/lib/openidcs"}
	}
	return nil
}

// ReadConfigPort 从 data.yaml 读取端口。
func ReadConfigPort() (string, bool) {
	cfg, err := appconfig.Read(filepath.Join(DataDir(), "data.yaml"))
	if err != nil {
		return "", false
	}
	return strconv.Itoa(cfg.Server.Port), true
}

func saveConfigPort(port string) error {
	path := filepath.Join(DataDir(), "data.yaml")
	cfg, err := appconfig.Read(path)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	cfg.Server.Port = n
	return appconfig.Save(path, cfg)
}

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

	configUpdated := false
	if _, err := appconfig.Read(filepath.Join(DataDir(), "data.yaml")); err == nil {
		if err := saveConfigPort(port); err != nil {
			return fmt.Errorf("写入 data.yaml 失败: %w", err)
		}
		configUpdated = true
	}

	switch runtime.GOOS {
	case "linux":
		unit := "/etc/systemd/system/" + linuxUnit + ".service"
		data, err := os.ReadFile(unit)
		if err != nil {
			if configUpdated {
				fmt.Printf("端口已写入 %s，重启主控后生效\n", filepath.Join(DataDir(), "data.yaml"))
				return nil
			}
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
