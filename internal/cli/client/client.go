// Package client 是 opidcs 使用的 REST 客户端。
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// APIError 主控返回的业务错误。
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("接口错误 %d: %s", e.Code, e.Msg)
}

// IsAuth 认证失败。
func (e *APIError) IsAuth() bool { return e.Code == 401 }

// Client HTTP 客户端。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client

	mu   sync.Mutex
	csrf string
}

// New 创建客户端。
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			Timeout: 40 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 主控常用自签证书
			},
		},
	}
}

// Do 发起请求并返回 data 字段。
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body interface{}) (interface{}, error) {
	return c.do(ctx, method, path, query, body, true)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body interface{}, retry bool) (interface{}, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求失败: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		token, err := c.ensureCSRF(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-CSRF-Token", token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接主控 %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden && retry && strings.Contains(string(raw), "CSRF") {
		c.resetCSRF()
		return c.do(ctx, method, path, query, body, false)
	}
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return parseEnvelope(raw)
}

func parseEnvelope(raw []byte) (interface{}, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &env); err == nil && env.Code != 0 {
		if env.Code != 200 {
			return nil, &APIError{Code: env.Code, Msg: env.Msg}
		}
		if len(env.Data) == 0 || string(env.Data) == "null" {
			return nil, nil
		}
		var out interface{}
		if err := json.Unmarshal(env.Data, &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	// 非统一信封（例如 /api/csrf-token）
	var out interface{}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return string(trimmed), nil
	}
	return out, nil
}

func (c *Client) ensureCSRF(ctx context.Context) (string, error) {
	c.mu.Lock()
	cached := c.csrf
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/csrf-token", nil)
	if err != nil {
		return "", err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 CSRF Token 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var payload struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.CSRF == "" {
		return "", errors.New("主控未返回 csrf_token")
	}
	c.mu.Lock()
	c.csrf = payload.CSRF
	c.mu.Unlock()
	return payload.CSRF, nil
}

func (c *Client) resetCSRF() {
	c.mu.Lock()
	c.csrf = ""
	c.mu.Unlock()
}

// Get 便捷方法。
func (c *Client) Get(ctx context.Context, path string, query url.Values) (interface{}, error) {
	return c.Do(ctx, http.MethodGet, path, query, nil)
}

// Post 便捷方法。
func (c *Client) Post(ctx context.Context, path string, body interface{}) (interface{}, error) {
	return c.Do(ctx, http.MethodPost, path, nil, body)
}

// Put 便捷方法。
func (c *Client) Put(ctx context.Context, path string, body interface{}) (interface{}, error) {
	return c.Do(ctx, http.MethodPut, path, nil, body)
}

// Delete 便捷方法。
func (c *Client) Delete(ctx context.Context, path string) (interface{}, error) {
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// Ping 检查连通性。
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Get(ctx, "/api/system/statis", nil)
	return err
}
