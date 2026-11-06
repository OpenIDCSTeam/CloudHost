package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 前端「外网端口留空自动分配」会发 wan_port: ""，
// 直接绑定到 int 字段会导致 400「请求参数错误」。
func TestBindPortDataTolerant(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		body    string
		wantWan int
		wantLan int
	}{
		{"空串自动分配", `{"wan_port":"","lan_port":22,"lan_addr":"10.2.20.100"}`, 0, 22},
		{"数字", `{"wan_port":10000,"lan_port":22}`, 10000, 22},
		{"字符串数字", `{"wan_port":"10000","lan_port":"8080"}`, 10000, 8080},
		{"null", `{"wan_port":null,"lan_port":22}`, 0, 22},
		{"缺省字段", `{"lan_port":22}`, 0, 22},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			req := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req

			p, err := bindPortData(ctx)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if p.WanPort != c.wantWan || p.LanPort != c.wantLan {
				t.Errorf("端口 = (%d,%d)，期望 (%d,%d)", p.WanPort, p.LanPort, c.wantWan, c.wantLan)
			}
		})
	}

	// 非法 JSON 仍然要报错
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	if _, err := bindPortData(ctx); err == nil {
		t.Error("非法 JSON 应返回错误")
	}
}
