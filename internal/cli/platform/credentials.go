package platform

import (
	"fmt"
	"runtime"
	"strings"
)

// Field 一项回填到主控的参数。
type Field struct {
	Key   string `json:"key" yaml:"key"`
	Value string `json:"value" yaml:"value"`
	Note  string `json:"note,omitempty" yaml:"note,omitempty"`
}

// Credentials 返回把该平台填入主控所需的参数。
func Credentials(name string) ([]Field, error) {
	name = normalize(name)
	info, ok := Find(name)
	if !ok {
		return nil, fmt.Errorf("未知平台: %s", name)
	}
	addr := "localhost"
	if list := LocalAddresses(); len(list) > 0 {
		addr = list[0]
	}
	version := info.Version
	if version == "" {
		version = "(未检测到)"
	}

	base := []Field{
		{Key: "platform", Value: info.Label},
		{Key: "installed", Value: fmt.Sprint(info.Installed)},
		{Key: "version", Value: version},
		{Key: "path", Value: info.Path},
	}

	switch name {
	case "docker":
		return append(base,
			Field{Key: "server_type", Value: "OCInterface", Note: "Docker Runtime Env"},
			Field{Key: "server_addr", Value: addr, Note: "本机填 localhost；远程填宿主 IP"},
			Field{Key: "server_user", Value: "root", Note: "需在 docker 组"},
			Field{Key: "images_path", Value: "/data/images"},
			Field{Key: "extern_path", Value: "/data/vms"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name docker-01 --type OCInterface --addr " + addr},
		), nil
	case "lxd":
		return append(base,
			Field{Key: "server_type", Value: "LxContainer", Note: "LinuxContainer Env"},
			Field{Key: "server_addr", Value: addr},
			Field{Key: "server_user", Value: "root", Note: "需在 lxd 组"},
			Field{Key: "images_path", Value: "/var/lib/lxd/images"},
			Field{Key: "extern_path", Value: "/var/lib/lxd/storage-pools"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name lxd-01 --type LxContainer --addr " + addr},
		), nil
	case "pve":
		return append(base,
			Field{Key: "server_type", Value: "PromoxSetup"},
			Field{Key: "server_addr", Value: addr + ":8006"},
			Field{Key: "server_user", Value: "root@pam", Note: "必须带 realm，且关闭 2FA"},
			Field{Key: "images_path", Value: "local:iso"},
			Field{Key: "extern_path", Value: "local-lvm"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name pve-01 --type PromoxSetup --addr " + addr + ":8006 --user root@pam"},
		), nil
	case "qemu":
		return append(base,
			Field{Key: "server_type", Value: "QEMUServer"},
			Field{Key: "server_addr", Value: addr},
			Field{Key: "server_user", Value: "root", Note: "需能执行 virsh"},
			Field{Key: "images_path", Value: "/var/lib/libvirt/images"},
			Field{Key: "extern_path", Value: "/data/vms"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name kvm-01 --type QEMUServer --addr " + addr},
		), nil
	case "virtualbox":
		return append(base,
			Field{Key: "server_type", Value: "VirtualBoxs", Note: "仅本机，is_remote=false"},
			Field{Key: "server_addr", Value: "localhost"},
			Field{Key: "images_path", Value: defaultVBoxPath("images")},
			Field{Key: "extern_path", Value: defaultVBoxPath("vms")},
			Field{Key: "controller_cmd", Value: "opidcs host add --name vbox-local --type VirtualBoxs --addr localhost"},
		), nil
	case "vmware":
		return append(base,
			Field{Key: "server_type", Value: "VMWareSetup", Note: "仅 Windows 宿主"},
			Field{Key: "server_addr", Value: addr + ":8697"},
			Field{Key: "server_port", Value: "8697"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name ws-01 --type VMWareSetup --addr " + addr + ":8697"},
		), nil
	case "hyperv":
		return append(base,
			Field{Key: "server_type", Value: "HyperVSetup"},
			Field{Key: "server_addr", Value: addr + ":5985"},
			Field{Key: "server_user", Value: "Administrator"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name win-hv --type HyperVSetup --addr " + addr + ":5985"},
		), nil
	case "smolvm":
		return append(base,
			Field{Key: "server_type", Value: "SmolVM"},
			Field{Key: "server_addr", Value: "localhost"},
			Field{Key: "images_path", Value: "/var/lib/smolvm"},
			Field{Key: "smolvm_kernel", Value: "/var/lib/smolvm/vmlinux-smolvm"},
			Field{Key: "smolvm_hv", Value: "firecracker"},
			Field{Key: "controller_cmd", Value: "opidcs host add --name smol-01 --type SmolVM --addr localhost"},
		), nil
	case "memu":
		return append(base,
			Field{Key: "server_type", Value: "MemuAndroid", Note: "仅 Windows 本机"},
			Field{Key: "server_addr", Value: "localhost"},
			Field{Key: "launch_path", Value: `C:\Program Files\Netease\MuMuPlayer-12.0\shell`},
			Field{Key: "controller_cmd", Value: "opidcs host add --name mumu-01 --type MemuAndroid --addr localhost"},
		), nil
	}
	return base, nil
}

// ResetCredentials 说明如何重置本机平台凭据（不做破坏性操作）。
func ResetCredentials(name string) (string, error) {
	name = normalize(name)
	switch name {
	case "docker":
		return "Docker 无需平台凭据；如需更换 SSH 账号，请更新该账号的 SSH 密钥并同步主控主机配置。", nil
	case "lxd":
		return "LXD 通过本机 CLI 管理；如需重置信任，执行：lxc config trust list && lxc config trust remove <fingerprint>", nil
	case "pve", "esxi", "hyperv", "vmware":
		return fmt.Sprintf("%s 的凭据在平台侧修改；修改后执行 opidcs host add 重新登记或编辑主控主机配置。", strings.ToUpper(name)), nil
	case "virtualbox", "memu", "smolvm":
		return "该平台使用本机命令通道，无独立平台凭据。", nil
	}
	return "", fmt.Errorf("未知平台: %s", name)
}

func defaultVBoxPath(kind string) string {
	if runtime.GOOS == "windows" {
		if kind == "images" {
			return `D:\VBox\Images`
		}
		return `D:\VBox\VMs`
	}
	if kind == "images" {
		return "/opt/vbox/images"
	}
	return "/opt/vbox/vms"
}
