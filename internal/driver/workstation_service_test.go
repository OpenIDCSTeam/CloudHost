package driver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// writeFakeExecutable 在临时目录写入占位可执行文件
func writeFakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	fileName := name
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	p := filepath.Join(dir, fileName)
	if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
		t.Fatalf("写入 %s 失败: %v", p, err)
	}
	return p
}

// TestWorkstationHostPort 覆盖 server_addr 的常见写法
func TestWorkstationHostPort(t *testing.T) {
	cases := []struct {
		addr     string
		port     int
		wantHost string
		wantPort int
	}{
		{"127.0.0.1", 0, "127.0.0.1", 8697},
		{"127.0.0.1", 9000, "127.0.0.1", 9000},
		{"127.0.0.1:9001", 0, "127.0.0.1", 9001},
		{"127.0.0.1:9002", 9000, "127.0.0.1", 9002},
		{"http://127.0.0.1:9003", 0, "127.0.0.1", 9003},
		{"http://127.0.0.1:9003/", 0, "127.0.0.1", 9003},
		{"10.0.0.71:8697", 0, "10.0.0.71", 8697},
		{"", 0, "127.0.0.1", 8697},
		{"::1", 0, "::1", 8697},
	}
	for _, tc := range cases {
		cfg := model.NewHSConfig()
		cfg.ServerAddr = tc.addr
		cfg.ServerPort = tc.port
		d := NewWorkstationDriver(cfg)
		host, port := d.wsHostPort()
		if host != tc.wantHost || port != tc.wantPort {
			t.Fatalf("server_addr=%q port=%d 解析为 %s:%d，期望 %s:%d",
				tc.addr, tc.port, host, port, tc.wantHost, tc.wantPort)
		}
	}
}

// TestWorkstationBaseURL 校验基地址拼接（IPv6 需带方括号）
func TestWorkstationBaseURL(t *testing.T) {
	cfg := model.NewHSConfig()
	cfg.ServerAddr = "::1"
	cfg.ServerPort = 0
	d := NewWorkstationDriver(cfg)
	if got := d.wsBaseURL(); got != "http://[::1]:8697" {
		t.Fatalf("wsBaseURL = %q，期望 http://[::1]:8697", got)
	}
}

// TestWsIsLocalAddress 只有本机地址才允许自动拉起 vmrest
func TestWsIsLocalAddress(t *testing.T) {
	cases := map[string]bool{
		"":            true,
		"localhost":   true,
		"127.0.0.1":   true,
		"::1":         true,
		"0.0.0.0":     true,
		"10.0.0.71":   false,
		"example.com": false,
	}
	for host, want := range cases {
		if got := wsIsLocalAddress(host); got != want {
			t.Fatalf("wsIsLocalAddress(%q) = %v，期望 %v", host, got, want)
		}
	}
}

// TestVMRestBinaryFromLaunchPath launch_path 支持目录、vmrest 可执行文件、同目录工具三种写法
func TestVMRestBinaryFromLaunchPath(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeExecutable(t, dir, "vmrest")

	cfg := model.NewHSConfig()
	cfg.LaunchPath = dir
	if got, err := NewWorkstationDriver(cfg).vmrestBinary(); err != nil || got != bin {
		t.Fatalf("launch_path=目录 得到 %q, %v，期望 %q", got, err, bin)
	}

	cfg.LaunchPath = bin
	if got, err := NewWorkstationDriver(cfg).vmrestBinary(); err != nil || got != bin {
		t.Fatalf("launch_path=可执行文件 得到 %q, %v，期望 %q", got, err, bin)
	}

	tool := writeFakeExecutable(t, dir, "vmrun")
	cfg.LaunchPath = tool
	if got, err := NewWorkstationDriver(cfg).vmrestBinary(); err != nil || got != bin {
		t.Fatalf("launch_path=同目录工具 得到 %q, %v，期望 %q", got, err, bin)
	}
}
