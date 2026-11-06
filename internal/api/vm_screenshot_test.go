package api

import "testing"

// 驱动历史上有的返回裸 base64、有的返回 data URL；
// 前端固定拼 data:image/png;base64, 前缀，多一层前缀会让图片解码失败。
func TestNormalizeScreenshot(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"纯base64", "iVBORw0KGgo=", "iVBORw0KGgo="},
		{"data-url", "data:image/png;base64,iVBORw0KGgo=", "iVBORw0KGgo="},
		{"jpeg-url", "data:image/jpeg;base64,AAAA", "AAAA"},
		{"带空白", "  data:image/png;base64,AAAA  ", "AAAA"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		if got := normalizeScreenshot(c.in); got != c.want {
			t.Errorf("%s: normalizeScreenshot(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}
