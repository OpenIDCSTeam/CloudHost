package driver

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseVMRestPower 覆盖 vmrest 电源响应的两种形态
func TestParseVMRestPower(t *testing.T) {
	cases := map[string]string{
		`{"power_state": "poweredOn"}`:  "poweredOn",
		`{"power_state": "poweredOff"}`: "poweredOff",
		`"poweredOn"`:                   "poweredOn",
		` poweredOn `:                   "poweredOn",
		``:                              "",
	}
	for raw, want := range cases {
		if got := parseVMRestPower([]byte(raw)); got != want {
			t.Fatalf("parseVMRestPower(%q) = %q，期望 %q", raw, got, want)
		}
	}
}

// TestReadVMXVNCConfig 读取 VMX 的 VNC 开关与端口
func TestReadVMXVNCConfig(t *testing.T) {
	dir := t.TempDir()

	vmx := filepath.Join(dir, "vm.vmx")
	content := ".encoding = \"UTF-8\"\n" +
		"displayName = \"vm\"\n" +
		"RemoteDisplay.vnc.enabled = \"TRUE\"\n" +
		"RemoteDisplay.vnc.port = \"6856\"\n"
	if err := os.WriteFile(vmx, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	found, enabled, port := readVMXVNCConfig(vmx)
	if !found || !enabled || port != 6856 {
		t.Fatalf("读取 VNC 配置 = (%v,%v,%d)，期望 (true,true,6856)", found, enabled, port)
	}

	off := filepath.Join(dir, "off.vmx")
	if err := os.WriteFile(off, []byte("RemoteDisplay.vnc.enabled = \"FALSE\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, enabled, _ := readVMXVNCConfig(off); enabled {
		t.Fatalf("RemoteDisplay.vnc.enabled=FALSE 应解析为关闭")
	}

	// 文件不存在：found=false，交由上层回退到配置端口
	if found, _, _ := readVMXVNCConfig(filepath.Join(dir, "missing.vmx")); found {
		t.Fatalf("VMX 不存在时 found 应为 false")
	}
}

// TestEnsureVMXVNC 开机前补齐 VNC 配置：只追加、不覆盖、可重复调用
func TestEnsureVMXVNC(t *testing.T) {
	dir := t.TempDir()

	vmx := filepath.Join(dir, "vm.vmx")
	if err := os.WriteFile(vmx, []byte("displayName = \"vm\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureVMXVNC(vmx, "6375"); err != nil {
		t.Fatalf("补齐 VNC 配置失败: %v", err)
	}
	_, enabled, port := readVMXVNCConfig(vmx)
	if !enabled || port != 6375 {
		t.Fatalf("补齐后 = (%v,%d)，期望 (true,6375)", enabled, port)
	}

	// 幂等：已有配置时不再追加
	before, _ := os.ReadFile(vmx)
	if err := ensureVMXVNC(vmx, "6500"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(vmx)
	if string(before) != string(after) {
		t.Fatalf("已有 VNC 配置时不应再修改 VMX")
	}

	// 显式关闭：保持关闭
	off := filepath.Join(dir, "off.vmx")
	if err := os.WriteFile(off, []byte("RemoteDisplay.vnc.enabled = \"FALSE\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureVMXVNC(off, "6375"); err != nil {
		t.Fatal(err)
	}
	if _, enabled, _ := readVMXVNCConfig(off); enabled {
		t.Fatalf("显式关闭 VNC 的虚拟机不应被自动开启")
	}
}

// TestResolveVNCConfigPort 端口优先级与明确关闭的提示
func TestResolveVNCConfigPort(t *testing.T) {
	dir := t.TempDir()

	withVNC := filepath.Join(dir, "with.vmx")
	if err := os.WriteFile(withVNC, []byte(
		"RemoteDisplay.vnc.enabled = \"TRUE\"\nRemoteDisplay.vnc.port = \"6856\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// VMX 端口优先于配置记录
	if port, err := resolveVNCConfigPort(withVNC, "6375"); err != nil || port != 6856 {
		t.Fatalf("端口 = %d, %v，期望 6856", port, err)
	}

	// VMX 不可读时回退配置端口
	if port, err := resolveVNCConfigPort(filepath.Join(dir, "missing.vmx"), "6375"); err != nil || port != 6375 {
		t.Fatalf("回退端口 = %d, %v，期望 6375", port, err)
	}

	// 明确关闭：直接报错
	off := filepath.Join(dir, "off.vmx")
	if err := os.WriteFile(off, []byte("RemoteDisplay.vnc.enabled = \"FALSE\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveVNCConfigPort(off, "6375"); err == nil {
		t.Fatalf("VNC 明确关闭时应返回错误")
	}

	// VMX 存在但完全没有 VNC 键：同样视为未启用
	plain := filepath.Join(dir, "plain.vmx")
	if err := os.WriteFile(plain, []byte("displayName = \"vm\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveVNCConfigPort(plain, "6375"); err == nil {
		t.Fatalf("VMX 无 VNC 配置时应返回错误")
	}

	// 无任何端口来源：报错
	if _, err := resolveVNCConfigPort(filepath.Join(dir, "missing.vmx"), ""); err == nil {
		t.Fatalf("无端口来源时应返回错误")
	}
}
