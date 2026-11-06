package api

import "testing"

// 代理内部用 ws/wss 表示 WebSocket 目标，但创建 vnc 会话走的是普通 HTTP。
// 直接把 wss:// 交给 net/http 会报 unsupported protocol scheme "wss"。
func TestHTTPScheme(t *testing.T) {
	cases := map[string]string{
		"wss":   "https",
		"ws":    "http",
		"https": "https",
		"http":  "http",
		"WSS":   "https",
		" wss ": "https",
		"":      "https",
	}
	for in, want := range cases {
		if got := httpScheme(in); got != want {
			t.Errorf("httpScheme(%q) = %q，期望 %q", in, got, want)
		}
	}
}
