package driver

import (
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestPowerStatusMapIntermediate 中间态必须可映射：
// vmrest 会返回 poweringOn/poweringOff/suspending/resetting，
// 缺少映射时会被当成「未知」写回前端，表现为「拿不到开机状态」。
func TestPowerStatusMapIntermediate(t *testing.T) {
	cases := map[string]model.VMPowers{
		"运行中": model.VMPowerStarted,
		"已关机": model.VMPowerStopped,
		"已停止": model.VMPowerStopped,
		"已暂停": model.VMPowerSuspend,
		"启动中": model.VMPowerOnOpen,
		"关闭中": model.VMPowerOnStop,
		"暂停中": model.VMPowerOnSave,
		"重启中": model.VMPowerOnWake,
		"未知":  model.VMPowerUnknown,
	}
	for state, want := range cases {
		got, ok := PowerStatusMap[state]
		if !ok {
			t.Fatalf("状态 %q 未在 PowerStatusMap 中映射", state)
		}
		if got != want {
			t.Fatalf("状态 %q 映射为 %s，期望 %s", state, got, want)
		}
	}
}
