package model

import "testing"

// TestSafeGetBoolTypes 覆盖数据库/JSON 解码可能产生的各种数值类型。
//
// 原实现只认 bool/int：SQLite 驱动返回 int64、JSON 解出 float64 时
// 会把 is_active=1 判成 false，表现为「账号明明启用却提示被禁用」。
func TestSafeGetBoolTypes(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
		want  bool
	}{
		{"bool-true", true, true},
		{"bool-false", false, false},
		{"int-1", int(1), true},
		{"int-0", int(0), false},
		{"int32-1", int32(1), true},
		{"int64-1", int64(1), true},
		{"int64-0", int64(0), false},
		{"uint64-1", uint64(1), true},
		{"float64-1", float64(1), true},
		{"float64-0", float64(0), false},
		{"float32-1", float32(1), true},
		{"string-1", "1", true},
		{"string-true", "true", true},
		{"string-TRUE-upper", " TRUE ", true},
		{"string-0", "0", false},
		{"string-garbage", "abc", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]interface{}{"is_active": c.value}
			if got := SafeGetBool(m, "is_active"); got != c.want {
				t.Errorf("SafeGetBool(%v) = %v, want %v", c.value, got, c.want)
			}
		})
	}

	// 缺失键必须返回 false 而不是 panic
	if SafeGetBool(map[string]interface{}{}, "is_active") {
		t.Error("缺失键应返回 false")
	}
}
