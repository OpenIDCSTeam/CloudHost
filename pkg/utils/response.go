// Package utils 提供通用工具函数
package utils

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Response 统一响应结构。
//
// 与 Python 版 RestManager.api_response 保持一致：
// {code, msg, data, timestamp}，HTTP 状态码恒为 200，业务语义由 code 表达。
// 前端（React）基于 Python 版协议开发，因此这里必须保持同样的契约，
// 否则 axios 会因非 2xx 直接进入异常分支而拿不到 code。
type Response struct {
	Code      int         `json:"code"`
	Msg       string      `json:"msg"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

// now 返回秒级时间戳，与 Python 版 int(time.time()) 对齐
func now() int64 {
	return time.Now().Unix()
}

// Success 返回成功响应
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:      200,
		Msg:       "成功",
		Data:      data,
		Timestamp: now(),
	})
}

// SuccessMsg 返回带消息的成功响应
func SuccessMsg(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:      200,
		Msg:       msg,
		Data:      data,
		Timestamp: now(),
	})
}

// Fail 返回失败响应。
//
// 保持 HTTP 200 + 业务码的形态以兼容既有前端；
// 失败可观测性通过日志与监控指标补充，而不是改变响应协议。
func Fail(c *gin.Context, code int, msg string) {
	c.JSON(http.StatusOK, Response{
		Code:      code,
		Msg:       msg,
		Timestamp: now(),
	})
}

// BadRequest 400 参数错误
func BadRequest(c *gin.Context, msg string) {
	Fail(c, 400, msg)
}

// Unauthorized 401 未授权
func Unauthorized(c *gin.Context) {
	Fail(c, 401, "未授权，请先登录")
}

// Forbidden 403 权限不足
func Forbidden(c *gin.Context) {
	Fail(c, 403, "权限不足")
}

// NotFound 404 资源不存在
func NotFound(c *gin.Context, msg string) {
	Fail(c, 404, msg)
}

// TooManyRequests 429 限流
func TooManyRequests(c *gin.Context, msg string) {
	Fail(c, 429, msg)
}

// requestIDContextKey 与 middleware.ContextRequestID 保持一致。
// 这里不 import middleware 包：auth/middleware 均依赖 utils，反向依赖会成环。
const requestIDContextKey = "request_id"

// RequestID 返回当前请求的追踪号（未启用中间件时为空串）
func RequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(requestIDContextKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// InternalError 500 服务器错误（msg 必须是面向用户的固定文案）
func InternalError(c *gin.Context, msg string) {
	Fail(c, 500, msg)
}

// InternalErrorf 记录内部错误详情，并向客户端返回脱敏文案。
//
// 原实现把 err.Error() 直接拼进响应，会把表结构、文件路径、驱动内部
// 实现细节泄露给调用方；详细原因只写日志，响应里带请求号供排查关联。
func InternalErrorf(c *gin.Context, msg string, err error) {
	rid := RequestID(c)
	method, path := "", ""
	if c != nil && c.Request != nil {
		method, path = c.Request.Method, c.Request.URL.Path
	}
	log.Printf("[error] rid=%s %s %s %s: %v", rid, method, path, msg, err)

	if rid != "" {
		Fail(c, 500, msg+"（请求号: "+rid+"）")
		return
	}
	Fail(c, 500, msg)
}
