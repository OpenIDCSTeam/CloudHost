// Package service 管理主控服务（systemd / launchd / Windows SCM）。
package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	linuxUnit   = "openidcs"
	launchdName = "com.openidcst.hostagent"
	windowsName = "OpenIDCS-HostAgent"
)

// Options 安装参数。
type Options struct {
	Version  string
	Port     string
	DataDir  string
	Dir      string
	File     string
	Firewall bool
	Purge    bool
}

// Status 返回服务状态文本。
func Status() (string, error) {
	out, err := run(serviceCommand("status")...)
	return strings.TrimSpace(out), err
}

// Start 启动服务。
func Start() error { return execService("start") }

// Stop 停止服务。
func Stop() error { return execService("stop") }

// Restart 重启服务。
func Restart() error { return execService("restart") }

// Enable 设置开机自启。
func Enable() error { return execService("enable") }

// Disable 取消开机自启。
func Disable() error { return execService("disable") }

func execService(action string) error {
	argv := serviceCommand(action)
	out, err := run(argv...)
	if out != "" {
		fmt.Print(out)
	}
	if err != nil {
		return fmt.Errorf("%s 失败: %w", action, err)
	}
	return nil
}

// Install 安装或更新主控。
func Install(opt Options) error {
	if runtime.GOOS == "windows" {
		return installWindows(opt)
	}
	script, cleanup, err := installScript()
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{script}
	if opt.Version != "" {
		args = append(args, "--version", opt.Version)
	}
	if opt.Port != "" {
		args = append(args, "--port", opt.Port)
	}
	if opt.Dir != "" {
		args = append(args, "--dir", opt.Dir)
	}
	if opt.DataDir != "" {
		args = append(args, "--data-dir", opt.DataDir)
	}
	if opt.File != "" {
		args = append(args, "--file", opt.File)
	}
	if opt.Firewall {
		args = append(args, "--firewall")
	}
	cmd := exec.Command("bash", args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

// Update 重新执行安装以升级。
func Update(port string) error {
	return Install(Options{Port: port})
}

// Uninstall 卸载主控。
func Uninstall(purge bool) error {
	if runtime.GOOS == "windows" {
		return errors.New("Windows 请通过「应用和功能」卸载 OpenIDCS-HostAgent")
	}
	script, cleanup, err := installScript()
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{script, "--uninstall"}
	if purge {
		args = append(args, "--purge")
	}
	cmd := exec.Command("bash", args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

// Port 从服务定义中读取监听端口。
func Port() string {
	switch runtime.GOOS {
	case "linux":
		out, err := run("systemctl", "show", linuxUnit, "-p", "ExecStart", "--value")
		if err == nil {
			if p := flagValue(out, "--port"); p != "" {
				return p
			}
		}
	case "darwin":
		plist := filepath.Join("/Library/LaunchDaemons", launchdName+".plist")
		if data, err := os.ReadFile(plist); err == nil {
			if p := flagValue(string(data), "--port"); p != "" {
				return p
			}
		}
	case "windows":
		out, err := run("sc", "qc", windowsName)
		if err == nil {
			if p := flagValue(out, "--port"); p != "" {
				return p
			}
		}
	}
	return "1881"
}

func serviceCommand(action string) []string {
	switch runtime.GOOS {
	case "darwin":
		switch action {
		case "status":
			return []string{"launchctl", "print", "system/" + launchdName}
		case "start":
			return []string{"launchctl", "kickstart", "-k", "system/" + launchdName}
		case "stop":
			return []string{"launchctl", "kill", "SIGTERM", "system/" + launchdName}
		case "restart":
			return []string{"launchctl", "kickstart", "-k", "system/" + launchdName}
		case "enable":
			return []string{"launchctl", "enable", "system/" + launchdName}
		case "disable":
			return []string{"launchctl", "disable", "system/" + launchdName}
		}
	case "windows":
		switch action {
		case "status":
			return []string{"sc", "query", windowsName}
		case "start":
			return []string{"sc", "start", windowsName}
		case "stop":
			return []string{"sc", "stop", windowsName}
		case "restart":
			return []string{"powershell", "-NoProfile", "-Command", "Restart-Service " + windowsName}
		case "enable":
			return []string{"sc", "config", windowsName, "start=", "auto"}
		case "disable":
			return []string{"sc", "config", windowsName, "start=", "disabled"}
		}
	default:
		argv := []string{"systemctl", action, linuxUnit}
		if os.Geteuid() != 0 {
			argv = append([]string{"sudo"}, argv...)
		}
		return argv
	}
	return []string{"true"}
}

func run(args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func installWindows(opt Options) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	matches, _ := filepath.Glob(filepath.Join(dir, "OpenIDCS-HostAgent-*-setup.exe"))
	if len(matches) == 0 {
		return errors.New("未找到安装包：请从 https://github.com/OpenIDCSTeam/CloudHost/releases 下载 setup.exe 后运行")
	}
	cmd := exec.Command(matches[0])
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func installScript() (string, func(), error) {
	// 优先使用当前目录 / 可执行文件同级的 install.sh
	candidates := []string{"install.sh"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "install.sh"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs, func() {}, nil
		}
	}

	url := "https://raw.githubusercontent.com/OpenIDCSTeam/CloudHost/main/install.sh"
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", func() {}, fmt.Errorf("下载安装脚本失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, fmt.Errorf("下载安装脚本失败: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "opidcs-install-*.sh")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", func() {}, err
	}
	tmp.Close()
	cleanup := func() { os.Remove(tmp.Name()) }
	return tmp.Name(), cleanup, nil
}

func flagValue(text, flag string) string {
	fields := strings.Fields(text)
	for i, f := range fields {
		f = strings.Trim(f, `"',;`)
		if f == flag && i+1 < len(fields) {
			return strings.Trim(fields[i+1], `"',;`)
		}
		if strings.HasPrefix(f, flag+"=") {
			return strings.Trim(strings.TrimPrefix(f, flag+"="), `"',;`)
		}
	}
	return ""
}
