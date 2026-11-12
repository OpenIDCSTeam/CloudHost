package service

import "strings"

// QuotaEnabled 返回全局配额开关。
//
// 设置项缺失时视为开启，保持历史行为；关闭后跳过所有配额校验，
// 前端据此隐藏用户侧/管理员侧的配额录入与展示。
func (s *SystemService) QuotaEnabled() bool {
	settings, err := s.GetSettings()
	if err != nil {
		return true
	}
	return parseQuotaEnabled(settings["quota_enabled"])
}

// parseQuotaEnabled 解析配额的布尔设置。
// 兼容前端写入的 "1"/"0" 字符串、历史布尔值与数字。
func parseQuotaEnabled(v interface{}) bool {
	switch val := v.(type) {
	case nil:
		return true
	case bool:
		return val
	case string:
		s := strings.ToLower(strings.TrimSpace(val))
		return s != "0" && s != "false" && s != "off"
	case float64:
		return val != 0
	case int:
		return val != 0
	}
	return true
}
