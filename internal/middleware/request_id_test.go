package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, GetRequestID(c))
	})
	return r
}

// TestRequestIDGenerated 未携带追踪号时自动生成并回写响应头
func TestRequestIDGenerated(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	r.ServeHTTP(w, req)

	id := w.Header().Get(HeaderRequestID)
	if len(id) != 16 {
		t.Fatalf("应生成 16 位十六进制追踪号，实际 %q", id)
	}
	if body := w.Body.String(); body != id {
		t.Fatalf("上下文中的追踪号应与响应头一致: body=%q header=%q", body, id)
	}
}

// TestRequestIDFromUpstream 上游传入的合法追踪号应被沿用，便于链路串联
func TestRequestIDFromUpstream(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, "trace-abc-123")
	r.ServeHTTP(w, req)

	if got := w.Header().Get(HeaderRequestID); got != "trace-abc-123" {
		t.Fatalf("应沿用上游追踪号，实际 %q", got)
	}
}

// TestRequestIDRejectsInjection 含换行/空格的上游值必须被替换，
// 否则可以被用来伪造日志行（日志注入）。
func TestRequestIDRejectsInjection(t *testing.T) {
	r := newTestRouter()

	for _, bad := range []string{
		"abc\ndef123",
		"abc 12345678",
		"../../etc/passwd",
		"short",
		strings.Repeat("a", 200),
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set(HeaderRequestID, bad)
		r.ServeHTTP(w, req)

		got := w.Header().Get(HeaderRequestID)
		if got == bad {
			t.Errorf("非法追踪号被原样沿用: %q", bad)
		}
		if len(got) != 16 {
			t.Errorf("非法追踪号应被替换为 16 位随机值，实际 %q", got)
		}
	}
}
