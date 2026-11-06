package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// Install 安装平台运行时。
func Install(name string) error {
	name = normalize(name)
	if name == "" {
		return errors.New("请指定平台，例如：opidcs plat install docker")
	}
	steps, err := installSteps(name)
	if err != nil {
		return err
	}
	fmt.Printf("==> 安装 %s（%s/%s）\n", name, runtime.GOOS, runtime.GOARCH)
	for _, step := range steps {
		if err := runStep(step); err != nil {
			return err
		}
	}
	fmt.Printf("==> %s 安装完成，可用 opidcs plat show %s 查看凭据\n", name, name)
	return nil
}

// Uninstall 卸载平台运行时。
func Uninstall(name string) error {
	name = normalize(name)
	steps, err := uninstallSteps(name)
	if err != nil {
		return err
	}
	fmt.Printf("==> 卸载 %s\n", name)
	for _, step := range steps {
		if err := runStep(step); err != nil {
			return err
		}
	}
	return nil
}

func installSteps(name string) ([][]string, error) {
	switch runtime.GOOS {
	case "linux":
		return linuxInstall(name)
	case "darwin":
		return darwinInstall(name)
	case "windows":
		return windowsInstall(name)
	default:
		return nil, fmt.Errorf("当前系统 %s 暂不支持自动安装", runtime.GOOS)
	}
}

func uninstallSteps(name string) ([][]string, error) {
	switch runtime.GOOS {
	case "linux":
		switch name {
		case "docker":
			return [][]string{{"systemctl", "disable", "--now", "docker"}, {"pkg", "remove", "docker.io"}}, nil
		case "lxd":
			return [][]string{{"snap", "remove", "lxd"}}, nil
		case "qemu":
			return [][]string{{"systemctl", "disable", "--now", "libvirtd"}, {"pkg", "remove", "qemu-kvm", "libvirt-daemon-system"}}, nil
		case "virtualbox":
			return [][]string{{"pkg", "remove", "virtualbox"}}, nil
		}
	case "darwin":
		switch name {
		case "docker":
			return [][]string{{"brew", "uninstall", "colima", "docker"}}, nil
		case "virtualbox":
			return [][]string{{"brew", "uninstall", "--cask", "virtualbox"}}, nil
		}
	case "windows":
		switch name {
		case "docker":
			return [][]string{{"winget", "uninstall", "-e", "--id", "Docker.DockerDesktop"}}, nil
		case "virtualbox":
			return [][]string{{"winget", "uninstall", "-e", "--id", "Oracle.VirtualBox"}}, nil
		case "hyperv":
			return [][]string{{"powershell", "-NoProfile", "-Command",
				"Disable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All -NoRestart"}}, nil
		}
	}
	return nil, fmt.Errorf("暂不支持自动卸载 %s（%s）", name, runtime.GOOS)
}

func linuxInstall(name string) ([][]string, error) {
	pkg := packageManager()
	switch name {
	case "docker":
		steps := [][]string{}
		switch pkg {
		case "apt":
			steps = append(steps, []string{"apt-get", "update"}, []string{"apt-get", "install", "-y", "docker.io"})
		case "dnf":
			steps = append(steps, []string{"dnf", "install", "-y", "docker"})
		case "pacman":
			steps = append(steps, []string{"pacman", "-Sy", "--noconfirm", "docker"})
		default:
			return nil, errors.New("未识别的包管理器，请手动安装 Docker")
		}
		steps = append(steps, []string{"systemctl", "enable", "--now", "docker"})
		if u := currentUser(); u != "" {
			steps = append(steps, []string{"usermod", "-aG", "docker", u})
		}
		return steps, nil
	case "lxd":
		if _, err := exec.LookPath("snap"); err == nil {
			return [][]string{{"snap", "install", "lxd"}, {"lxd", "init", "--auto"}}, nil
		}
		return [][]string{{"apt-get", "update"}, {"apt-get", "install", "-y", "lxd", "lxd-client"}, {"lxd", "init", "--auto"}}, nil
	case "qemu":
		switch pkg {
		case "dnf":
			return [][]string{{"dnf", "install", "-y", "qemu-kvm", "libvirt", "virt-install"}, {"systemctl", "enable", "--now", "libvirtd"}}, nil
		default:
			return [][]string{
				{"apt-get", "update"},
				{"apt-get", "install", "-y", "qemu-kvm", "libvirt-daemon-system", "libvirt-clients", "bridge-utils", "virtinst"},
				{"systemctl", "enable", "--now", "libvirtd"},
			}, nil
		}
	case "virtualbox":
		switch pkg {
		case "dnf":
			return [][]string{{"dnf", "install", "-y", "VirtualBox"}}, nil
		default:
			return [][]string{{"apt-get", "update"}, {"apt-get", "install", "-y", "virtualbox"}}, nil
		}
	case "smolvm":
		return [][]string{{"mkdir", "-p", "/var/lib/smolvm"}}, nil
	}
	return nil, fmt.Errorf("Linux 暂不支持安装 %s", name)
}

func darwinInstall(name string) ([][]string, error) {
	if _, err := exec.LookPath("brew"); err != nil {
		return nil, errors.New("未安装 Homebrew，请先安装：https://brew.sh")
	}
	switch name {
	case "docker":
		return [][]string{{"brew", "install", "colima", "docker"}, {"colima", "start"}}, nil
	case "virtualbox":
		return [][]string{{"brew", "install", "--cask", "virtualbox"}}, nil
	case "smolvm":
		return [][]string{{"mkdir", "-p", os.Getenv("HOME") + "/.smolvm"}}, nil
	}
	return nil, fmt.Errorf("macOS 暂不支持安装 %s", name)
}

func windowsInstall(name string) ([][]string, error) {
	winget := func(id string) [][]string {
		return [][]string{{"winget", "install", "-e", "--id", id, "--accept-package-agreements", "--accept-source-agreements"}}
	}
	switch name {
	case "docker":
		return winget("Docker.DockerDesktop"), nil
	case "hyperv":
		return [][]string{{"powershell", "-NoProfile", "-Command",
			"Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All -All -NoRestart"}}, nil
	case "vmware":
		return winget("VMware.WorkstationPro"), nil
	case "virtualbox":
		return winget("Oracle.VirtualBox"), nil
	case "memu":
		return nil, errors.New("MuMu 需手动安装：https://mumu.163.com/，安装后开启 ADB")
	}
	return nil, fmt.Errorf("Windows 暂不支持安装 %s", name)
}

func runStep(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	args := argv
	if runtime.GOOS != "windows" && argv[0] != "pkg" && os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err == nil {
			args = append([]string{"sudo"}, argv...)
		}
	}
	if argv[0] == "pkg" {
		args = append([]string{packageManager()}, argv[1:]...)
	}
	fmt.Println("  $", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("执行失败: %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func packageManager() string {
	for _, pm := range []string{"apt-get", "dnf", "yum", "pacman"} {
		if _, err := exec.LookPath(pm); err == nil {
			switch pm {
			case "yum":
				return "dnf"
			case "apt-get":
				return "apt"
			default:
				return pm
			}
		}
	}
	return ""
}

func currentUser() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
