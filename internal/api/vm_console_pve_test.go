package api

import (
	"strings"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// Proxmox 的 VNC 必须走 vncwebsocket 通道：
//   - token 必须拼进 path，否则 6080 的 /websockify 收不到 token；
//   - 必须带上 VNC 密码（vncproxy ticket 的前 8 位），
//     否则 QEMU 会以 Authentication failed 拒绝连接。
func TestBuildConsoleURLForProxmox(t *testing.T) {
	d := driver.NewProxmoxDriver(&model.HSConfig{ServerAddr: "10.0.0.1"})
	app := &App{}

	results := map[string]interface{}{
		"type":     "proxmox",
		"host":     "10.0.0.1",
		"api_host": "10.0.0.1:8006",
		"node":     "pve1",
		"vmid":     103,
		"port":     5900,
		"ticket":   "vnc-ticket",
		"password": "PVEVNC:6",
		"cookie":   "api-ticket",
		"csrf":     "csrf-token",
	}

	got := app.buildConsoleURL(nil, d, "h", "u", results)
	if got == "" {
		t.Fatal("Proxmox 应能生成控制台地址")
	}
	if !strings.Contains(got, "path=websockify%3Ftoken%3D") {
		t.Errorf("token 必须随 path 传递，实际 %q", got)
	}
	if !strings.Contains(got, "password=PVEVNC%3A6") {
		t.Errorf("必须带上 VNC 密码，实际 %q", got)
	}
}

// 缺少 API 会话（cookie）时无法建立 vncwebsocket，应如实返回空地址，
// 由前端展示 console_error，而不是给出一个必然失败的链接。
func TestBuildConsoleURLProxmoxWithoutCookie(t *testing.T) {
	d := driver.NewProxmoxDriver(&model.HSConfig{ServerAddr: "10.0.0.1"})
	app := &App{}

	results := map[string]interface{}{
		"type":     "proxmox",
		"host":     "10.0.0.1",
		"api_host": "10.0.0.1:8006",
		"node":     "pve1",
		"vmid":     103,
	}

	if got := app.buildConsoleURL(nil, d, "h", "u", results); got != "" {
		t.Errorf("缺少 API 会话时不应生成地址，实际 %q", got)
	}
}
