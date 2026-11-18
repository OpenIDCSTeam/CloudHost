//go:build manualcheck

package driver

import (
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestManualSyncCycle 模拟调度器一轮：GetPower → SetVMPowerState → Crontab，
// 确认 Crontab 不会把状态改回中间态
func TestManualSyncCycle(t *testing.T) {
	cfg := model.NewHSConfig()
	cfg.ServerAddr = "127.0.0.1"
	cfg.ServerPort = 8697
	cfg.ServerUser = "pika"
	cfg.ServerPass = "0ExY8ao*"
	cfg.SystemPath = `E:\OIDCS\VMwareWS\system`
	cfg.LaunchPath = `C:\Program Files (x86)\VMware\VMware Workstation\vmrest.exe`
	cfg.FilterName = "vmw-"
	d := NewWorkstationDriver(cfg)

	name := "vmw-zfi1o5YC"
	vm := model.NewVMConfig()
	vm.VmUUID = name
	vm.OsName = "win-10-lite-cloud.vhd"
	vm.VmFlag = model.VMPowerUnknown
	d.SetVMSaving(map[string]*model.VMConfig{name: vm})

	actual := d.GetPower(name)
	state, ok := PowerStatusMap[actual]
	t.Logf("GetPower=%q → %s (ok=%v)", actual, state, ok)
	d.SetVMPowerState(name, state)

	if !d.Crontab() {
		t.Fatalf("Crontab 执行失败")
	}

	detail, err := d.GetVMDetail(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Crontab 后 VmFlag = %s", detail.VmFlag)
	if detail.VmFlag != model.VMPowerStarted {
		t.Fatalf("Crontab 把状态改回了 %s，期望 STARTED", detail.VmFlag)
	}
}
