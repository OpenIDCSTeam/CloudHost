package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvelopeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"成功","data":{"name":"pve"},"timestamp":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "token")
	data, err := c.Get(context.Background(), "/api/system/statis", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := data.(map[string]interface{})
	if m["name"] != "pve" {
		t.Fatalf("data 解析错误: %+v", data)
	}
}

func TestEnvelopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":401,"msg":"未认证"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	_, err := c.Get(context.Background(), "/api/server/detail", nil)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != 401 || !apiErr.IsAuth() {
		t.Fatalf("应返回 401 APIError: %v", err)
	}
}

func TestCSRFAutoFetch(t *testing.T) {
	csrfHits := 0
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/csrf-token" {
			csrfHits++
			_ = json.NewEncoder(w).Encode(map[string]string{"csrf_token": "csrf-123"})
			return
		}
		gotHeader = r.Header.Get("X-CSRF-Token")
		if gotHeader != "csrf-123" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"CSRF token 无效"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	if _, err := c.Post(context.Background(), "/api/client/powers/a/b", map[string]string{"action": "start"}); err != nil {
		t.Fatal(err)
	}
	if csrfHits != 1 {
		t.Fatalf("CSRF 应只获取一次，实际 %d", csrfHits)
	}
	if gotHeader != "csrf-123" {
		t.Fatalf("请求头未携带 CSRF: %q", gotHeader)
	}
}

func TestCSRFRetryOnForbidden(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/csrf-token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"csrf_token": "new"})
			return
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"CSRF token 无效或已过期"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"msg":"ok","data":{"ok":true}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	c.csrf = "stale"
	if _, err := c.Post(context.Background(), "/api/token/reset", nil); err != nil {
		t.Fatalf("应重试成功: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("应重试一次，实际请求 %d 次", attempts)
	}
}
