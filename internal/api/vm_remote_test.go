package api

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// namedPort 模拟第三方库里的命名整型（如 proxmox.StringOrInt 的底层类型）
type namedPort int

// 各驱动返回的端口类型并不统一：早期只认 int/int64/float64，
// Proxmox（命名类型）与 ESXi（int32）的控制台地址会恒为空，
// 前端表现为「获取的控制台地址无效」。
func TestToPortIntCoversDriverTypes(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int
	}{
		{"int", 5900, 5900},
		{"int32", int32(5901), 5901},
		{"int64", int64(5902), 5902},
		{"uint16", uint16(5903), 5903},
		{"float64", float64(5904), 5904},
		{"string", "5905", 5905},
		{"json-number", json.Number("5906"), 5906},
		{"named-int", namedPort(5907), 5907},
		{"zero", 0, 0},
		{"negative", -1, 0},
		{"garbage", "abc", 0},
		{"nil", nil, 0},
	}
	for _, c := range cases {
		if got := toPortInt(c.in); got != c.want {
			t.Errorf("%s: toPortInt(%#v) = %d，期望 %d", c.name, c.in, got, c.want)
		}
	}
}

func TestComposeConsoleURL(t *testing.T) {
	raw := composeConsoleURL("http", "10.2.1.2", 6080, "tok en", "p@ss word")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("生成的地址无法解析: %v", err)
	}
	if parsed.Path != "/vnc.html" {
		t.Errorf("路径应为 /vnc.html，实际 %q", parsed.Path)
	}
	q := parsed.Query()
	// token 必须随 path 一起传给 noVNC（websockify?token=xxx），
	// 否则 6080 的 /websockify 收不到 token 会 401。
	for key, want := range map[string]string{
		"autoconnect": "true",
		"path":        "websockify?token=tok en",
		"password":    "p@ss word",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("参数 %s = %q，期望 %q", key, got, want)
		}
	}
	if q.Get("token") != "" {
		t.Errorf("token 不应出现在外层查询参数中，实际 %q", q.Get("token"))
	}

	// 没有密码时不应带上空 password 参数
	if got := composeConsoleURL("http", "h", 6080, "t", ""); got !=
		"http://h:6080/vnc.html?autoconnect=true&path=websockify%3Ftoken%3Dt" {
		t.Errorf("无密码时地址不符: %s", got)
	}
}

// ESXi 的 WebMKS 票据无法被 websockify 代理，不应拼出会黑屏的 noVNC 地址。
func TestBuildConsoleURLSkipsWebMKS(t *testing.T) {
	app := &App{}
	results := map[string]interface{}{"type": "vmrc", "host": "10.0.0.1", "port": 443}
	if got := app.buildConsoleURL(nil, nil, "h", "u", results); got != "" {
		t.Errorf("vmrc 类型不应生成 noVNC 地址，实际 %q", got)
	}
}

func TestConsoleUnavailableReason(t *testing.T) {
	cases := []struct {
		name    string
		results map[string]interface{}
		want    string
	}{
		{"tty", map[string]interface{}{"type": "tty"}, "该平台仅提供命令行控制台，请使用终端入口"},
		{"vmconnect", map[string]interface{}{"type": "vmconnect"}, "Hyper-V 需使用 VMConnect 客户端连接，浏览器控制台不可用"},
		{"no-port", map[string]interface{}{"type": "vnc", "host": "10.0.0.1"}, "平台未返回可用的控制台端口，请确认虚拟机已启用 VNC"},
		{"unknown", map[string]interface{}{"type": "vnc", "port": 5900}, "无法生成控制台地址"},
	}
	for _, c := range cases {
		if got := consoleUnavailableReason(c.results); got != c.want {
			t.Errorf("%s: 期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// noVNC 页面目录必须能在部署目录被发现，
// 否则 6080 的 /vnc.html 恒为 404，控制台地址有效也打不开。
func TestResolveConsoleWebRootFindsDeployDir(t *testing.T) {
	dir := t.TempDir()
	consoleDir := filepath.Join(dir, "VNCConsole")
	if err := os.MkdirAll(consoleDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consoleDir, "vnc.html"), []byte("<!doctype html>"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got := resolveConsoleWebRoot()
	want, _ := filepath.Abs(consoleDir)
	if got != want {
		t.Errorf("应解析出 %q，实际 %q", want, got)
	}
}

func TestResolveConsoleWebRootMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := resolveConsoleWebRoot(); got != "" {
		t.Errorf("目录不存在时应返回空串，实际 %q", got)
	}
}
