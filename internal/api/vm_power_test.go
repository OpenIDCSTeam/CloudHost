package api

import (
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// 前端部分页面发送语义值（start/stop/hard_stop/...），驱动只认
// S_START/S_CLOSE/...；不归一化会全部落到 default 分支，
// 表现为「不支持的电源操作」。这里同时校验两种写法。
func TestNormalizePowerAction(t *testing.T) {
	cases := []struct {
		in   string
		want model.VMPowers
	}{
		// 驱动指令常量（Python / 既有接口的写法）
		{"S_START", model.VMPowerSStart},
		{"s_start", model.VMPowerSStart},
		{"S_CLOSE", model.VMPowerSClose},
		{"S_RESET", model.VMPowerSReset},
		{"H_CLOSE", model.VMPowerHClose},
		{"H_RESET", model.VMPowerHReset},
		{"A_PAUSE", model.VMPowerAPause},
		{"A_WAKED", model.VMPowerAWaked},

		// 前端语义值
		{"start", model.VMPowerSStart},
		{"stop", model.VMPowerSClose},
		{"shutdown", model.VMPowerSClose},
		{"hard_stop", model.VMPowerHClose},
		{"reset", model.VMPowerSReset},
		{"hard_reset", model.VMPowerHReset},
		{"pause", model.VMPowerAPause},
		{"resume", model.VMPowerAWaked},

		// 空白与大小写
		{" Start ", model.VMPowerSStart},
		{"HARD_RESET", model.VMPowerHReset},
	}

	for _, c := range cases {
		got, ok := normalizePowerAction(c.in)
		if !ok {
			t.Errorf("normalizePowerAction(%q) 不应失败", c.in)
			continue
		}
		if got != c.want {
			t.Errorf("normalizePowerAction(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}
}

func TestNormalizePowerActionRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "explode", "s_pause", "S_RESUME"} {
		if got, ok := normalizePowerAction(in); ok {
			t.Errorf("normalizePowerAction(%q) 应拒绝，实际返回 %s", in, got)
		}
	}
}
