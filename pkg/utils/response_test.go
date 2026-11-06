package utils

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestInternalErrorfHidesDetails 内部错误详情只写日志，不能回显给客户端。
func TestInternalErrorfHidesDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/users", nil)

	InternalErrorf(c, "创建用户失败", errors.New("UNIQUE constraint failed: web_users.username"))

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if strings.Contains(resp.Msg, "UNIQUE constraint") {
		t.Fatalf("响应泄露了数据库内部信息: %q", resp.Msg)
	}
	if !strings.Contains(resp.Msg, "创建用户失败") {
		t.Fatalf("响应应保留面向用户的文案: %q", resp.Msg)
	}
}

// TestInternalErrorfIncludesRequestID 响应里带上追踪号，便于与日志关联
func TestInternalErrorfIncludesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	c.Set("request_id", "rid-1234abcd")

	InternalErrorf(c, "读取失败", errors.New("boom"))

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !strings.Contains(resp.Msg, "rid-1234abcd") {
		t.Fatalf("响应应包含请求号: %q", resp.Msg)
	}
	if strings.Contains(resp.Msg, "boom") {
		t.Fatalf("响应泄露了错误详情: %q", resp.Msg)
	}
}

// TestInternalErrorKeepsMessage 固定文案的 InternalError 行为保持不变（兼容前端）
func TestInternalErrorKeepsMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	InternalError(c, "电源操作失败")

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != 500 || resp.Msg != "电源操作失败" {
		t.Fatalf("响应不符: code=%d msg=%q", resp.Code, resp.Msg)
	}
}
