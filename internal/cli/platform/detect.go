// Package platform 负责本机虚拟化平台的探测、安装与凭据信息。
package platform

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Info 平台探测结果。
type Info struct {
	Name      string `json:"name" yaml:"name"`
	Label     string `json:"label" yaml:"label"`
	Installed bool   `json:"installed" yaml:"installed"`
	Version   string `json:"version,omitempty" yaml:"version,omitempty"`
	Path      string `json:"path,omitempty" yaml:"path,omitempty"`
	Note      string `json:"note,omitempty" yaml:"note,omitempty"`
}

type probe struct {
	name  string
	label string
	cmd   []string
	parse func(string) string
}

var probes = []probe{
	{"docker", "Docker", []string{"docker", "version", "--format", "{{.Server.Version}}"}, firstLine},
	{"podman", "Podman", []string{"podman", "--version"}, firstLine},
	{"lxd", "LXD", []string{"lxc", "--version"}, firstLine},
	{"pve", "Proxmox VE", []string{"pveversion"}, firstLine},
	{"qemu", "QEMU / KVM", []string{"virsh", "--version"}, firstLine},
	{"virtualbox", "VirtualBox", []string{"VBoxManage", "--version"}, firstLine},
	{"vmware", "VMware Workstation (vmrest)", []string{"vmrest", "-v"}, firstLine},
	{"memu", "MuMu (memuc)", []string{"memuc", "version"}, firstLine},
}

// Detect 探测本机已安装的平台。
func Detect() []Info {
	result := make([]Info, 0, len(probes))
	for _, p := range probes {
		path, err := exec.LookPath(p.cmd[0])
		info := Info{Name: p.name, Label: p.label, Path: path}
		if err != nil {
			info.Note = installHint(p.name)
			result = append(result, info)
			continue
		}
		info.Installed = true
		if out, err := run(p.cmd[0], p.cmd[1:]...); err == nil {
			info.Version = p.parse(out)
		}
		result = append(result, info)
	}
	if runtime.GOOS == "windows" {
		result = append(result, detectHyperV())
	}
	return result
}

func detectHyperV() Info {
	info := Info{Name: "hyperv", Label: "Windows Hyper-V"}
	out, err := run("powershell", "-NoProfile", "-Command", "(Get-Command Get-VM -ErrorAction SilentlyContinue) -ne $null")
	if err == nil && strings.EqualFold(strings.TrimSpace(out), "True") {
		info.Installed = true
		info.Version = "enabled"
	} else {
		info.Note = installHint("hyperv")
	}
	return info
}

// Find 查找指定平台。
func Find(name string) (Info, bool) {
	name = normalize(name)
	for _, info := range Detect() {
		if normalize(info.Name) == name {
			return info, true
		}
	}
	return Info{}, false
}

// Supported 返回当前系统可安装的平台。
func Supported() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{"docker", "lxd", "smolvm", "qemu", "virtualbox"}
	case "darwin":
		return []string{"docker", "smolvm", "virtualbox"}
	case "windows":
		return []string{"docker", "hyperv", "vmware", "memu", "virtualbox"}
	default:
		return []string{"docker"}
	}
}

// Virtualization 返回本机虚拟化能力。
func Virtualization() map[string]interface{} {
	info := map[string]interface{}{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
		"cpus": runtime.NumCPU(),
	}
	info["addresses"] = LocalAddresses()
	switch runtime.GOOS {
	case "linux":
		info["kvm_device"] = exists("/dev/kvm")
		info["cpu_flags"] = linuxCPUFlags()
	case "darwin":
		out, _ := run("sysctl", "-n", "kern.hv_support")
		info["hypervisor"] = strings.TrimSpace(out) == "1"
	case "windows":
		out, _ := run("powershell", "-NoProfile", "-Command",
			"(Get-CimInstance Win32_Processor | Select-Object -First 1 -ExpandProperty VirtualizationFirmwareEnabled)")
		info["hypervisor"] = strings.EqualFold(strings.TrimSpace(out), "True")
	}
	return info
}

// LocalAddresses 返回非回环 IPv4 地址。
func LocalAddresses() []string {
	addrs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range addrs {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		list, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range list {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			out = append(out, ipNet.IP.String())
		}
	}
	return out
}

func linuxCPUFlags() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	text := string(data)
	switch {
	case strings.Contains(text, " vmx "):
		return "vmx"
	case strings.Contains(text, " svm "):
		return "svm"
	default:
		return ""
	}
}

func installHint(name string) string {
	switch normalize(name) {
	case "docker":
		return "opidcs plat install docker"
	case "lxd":
		return "opidcs plat install lxd"
	case "smolvm":
		return "opidcs plat install smolvm（需 KVM + Firecracker/Cloud-Hypervisor）"
	case "qemu":
		return "opidcs plat install qemu"
	case "virtualbox":
		return "opidcs plat install virtualbox"
	case "hyperv":
		return "opidcs plat install hyperv（Windows 功能）"
	case "vmware":
		return "安装 VMware Workstation Pro 并启动 vmrest"
	case "memu":
		return "安装 MuMu 模拟器并在设置中开启 ADB"
	default:
		return ""
	}
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return string(out), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "platform", "plat":
		return ""
	case "podman":
		return "docker"
	case "lxc", "lxd":
		return "lxd"
	case "proxmox", "pve", "promoxsetup":
		return "pve"
	case "kvm", "libvirt":
		return "qemu"
	case "vbox", "virtualboxs":
		return "virtualbox"
	case "workstation", "vmwareworkstation":
		return "vmware"
	case "mumu", "memuandroid":
		return "memu"
	case "hyper-v", "hypervsetup":
		return "hyperv"
	}
	return n
}
