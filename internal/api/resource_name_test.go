package api

import (
	"strings"
	"testing"
)

// 磁盘/光盘名称改为系统自动生成：
// 显式传入时保留（兼容第三方），为空时必须是 <prefix>-<年月日时分秒>。
func TestAutoResourceName(t *testing.T) {
	if got := autoResourceName("data", "custom_name"); got != "custom_name" {
		t.Errorf("显式名称应原样保留，实际 %q", got)
	}
	if got := autoResourceName("data", "  "); !strings.HasPrefix(got, "data-") {
		t.Errorf("空白名称应视为未填写，实际 %q", got)
	}

	got := autoResourceName("iso", "")
	if !strings.HasPrefix(got, "iso-") {
		t.Fatalf("应以 iso- 开头，实际 %q", got)
	}
	suffix := strings.TrimPrefix(got, "iso-")
	if len(suffix) != 14 {
		t.Errorf("时间戳应为 14 位（年月日时分秒），实际 %q", suffix)
	}
	for _, ch := range suffix {
		if ch < '0' || ch > '9' {
			t.Errorf("时间戳应全为数字，实际 %q", suffix)
			break
		}
	}
}
