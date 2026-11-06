package service

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freePort 申请一个当前未被监听的本地端口，避免测试命中真实的 Caddy Admin API。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请本地端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// Caddy 可能放在程序目录/bin 下，必须能被自动发现；
// 之前只认 PATH，部署包里自带 caddy 也会报 executable file not found。
func TestProxyServiceResolveCaddyBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, caddyExecName())
	if err := os.WriteFile(bin, []byte("stub"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &ProxyService{}
	s.SetBinaryPath(bin)
	if got := s.BinaryPath(); got != bin {
		t.Errorf("应解析出显式配置的 caddy 路径 %q，实际 %q", bin, got)
	}

	// 显式路径不存在且 PATH 里也没有时必须返回空串，而不是一个假路径
	t.Setenv("PATH", filepath.Join(dir, "empty-bin"))
	missing := &ProxyService{}
	missing.SetBinaryPath(filepath.Join(dir, "no-such-caddy"))
	if got := missing.BinaryPath(); got != "" {
		t.Errorf("不存在的 caddy 路径不应可用，实际 %q", got)
	}
}

// 缺少 caddy 时 Reload 必须给出可操作提示，且不再执行两次无效命令。
func TestProxyServiceReloadWithoutCaddy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "temp"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "temp", "Caddyfile"),
		[]byte("{\n\tadmin localhost:2019\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewProxyService(nil, dir)
	s.managePort = freePort(t)
	s.SetBinaryPath(filepath.Join(dir, "no-such-caddy"))
	t.Setenv("PATH", filepath.Join(dir, "empty-bin"))

	err := s.Reload()
	if err == nil {
		t.Fatal("缺少 caddy 时应返回错误")
	}
	for _, want := range []string{"caddy", "CADDY_BIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应包含 %q，实际 %q", want, err)
		}
	}
}

// Admin API 未就绪时 waitAdminReady 应按超时返回，而不是无限等待。
func TestProxyServiceWaitAdminReadyTimeout(t *testing.T) {
	s := NewProxyService(nil, t.TempDir())
	s.managePort = freePort(t)

	if err := s.waitAdminReady(100 * time.Millisecond); err == nil {
		t.Error("Admin API 未监听时应返回超时错误")
	}
}
