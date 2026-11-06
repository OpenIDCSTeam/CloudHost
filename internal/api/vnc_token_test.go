package api

import (
	"testing"
	"time"
)

// noVNC 断线后会重连，token 必须能在有效期内复用；
// 若一次性使用，重连必然 401（浏览器报 no valid credentials available）。
func TestVNCTokenReusableWithinTTL(t *testing.T) {
	RegisterVNCToken("tok-reuse", "h", "u", "127.0.0.1", "", 5900)
	for i := 0; i < 3; i++ {
		if got := GetVNCToken("tok-reuse", "127.0.0.1"); got == nil {
			t.Fatalf("第 %d 次校验失败：token 应可重复使用", i+1)
		}
	}
}

// 过期后仍然必须拒绝。
func TestVNCTokenExpired(t *testing.T) {
	vncTokensMu.Lock()
	vncTokens["tok-expired"] = &VNCToken{
		HsName:   "h",
		VmUUID:   "u",
		Host:     "127.0.0.1",
		Port:     5900,
		ExpireAt: time.Now().Add(-time.Minute),
	}
	vncTokensMu.Unlock()

	if GetVNCToken("tok-expired", "127.0.0.1") != nil {
		t.Error("过期 token 应被拒绝")
	}
	// 过期条目应被清理
	vncTokensMu.Lock()
	_, exists := vncTokens["tok-expired"]
	vncTokensMu.Unlock()
	if exists {
		t.Error("过期 token 应从表中删除")
	}
}

func TestVNCTokenUnknown(t *testing.T) {
	if GetVNCToken("tok-not-exist", "127.0.0.1") != nil {
		t.Error("未知 token 应被拒绝")
	}
}
