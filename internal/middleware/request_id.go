// Package middleware HTTP 中间件
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// HeaderRequestID 请求追踪号响应头
	HeaderRequestID = "X-Request-ID"
	// ContextRequestID gin 上下文中的键名（utils 包按同一字面量读取）
	ContextRequestID = "request_id"

	requestIDBytes = 8
)

// RequestID 为每个请求生成（或沿用上游传入的）追踪号。
//
// 作用：
//   - 响应头返回 X-Request-ID，前端报障时可直接给出；
//   - 写进 gin 上下文，供错误响应与业务日志关联；
//   - 访问日志每行带上 rid，一次请求的全链路日志可以串起来。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(HeaderRequestID))
		if !validRequestID(id) {
			id = newRequestID()
		}
		c.Set(ContextRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// GetRequestID 从上下文读取请求追踪号
func GetRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(ContextRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// validRequestID 只接受由字母、数字、点、下划线、连字符组成、
// 长度 8~64 的上游追踪号。带换行或空格的取值会被拒绝，
// 否则可以把伪造内容注入日志（日志注入）。
func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	b := make([]byte, requestIDBytes)
	if _, err := rand.Read(b); err != nil {
		// 随机源不可用时退化为固定短串，宁可追踪号重复也不要 panic
		return "unknown-request"
	}
	return hex.EncodeToString(b)
}
