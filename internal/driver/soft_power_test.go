package driver

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// gracefulStubDriver 用于验证「先平台软关机、超时再强制关机」的调度顺序。
type gracefulStubDriver struct {
	*BaseDriver
	graceful   bool
	stopOnSoft bool // 模拟 guest 是否响应平台软关机

	mu      sync.Mutex
	actions []model.VMPowers
	state   string
}

func newGracefulStub(t *testing.T, graceful, stopOnSoft bool) *gracefulStubDriver {
	t.Helper()
	d := &gracefulStubDriver{
		BaseDriver: NewBaseDriver(testConfig(t, "PromoxSetup")),
		graceful:   graceful,
		stopOnSoft: stopOnSoft,
		state:      "运行中",
	}
	d.SetSelf(d)
	return d
}

func (d *gracefulStubDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.actions = append(d.actions, action)
	switch action {
	case model.VMPowerSClose:
		if d.stopOnSoft {
			d.state = "已关机"
		}
	case model.VMPowerHClose:
		d.state = "已关机"
	}
	return &model.ZMessage{Success: true, Action: "VMPowers"}, nil
}

func (d *gracefulStubDriver) GetPower(vmUUID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

func (d *gracefulStubDriver) SupportsGracefulShutdown() bool { return d.graceful }

func (d *gracefulStubDriver) calls() []model.VMPowers {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]model.VMPowers(nil), d.actions...)
}

// 测试用短超时
func shortenPowerWaits(t *testing.T) {
	t.Helper()
	oldInterval, oldForce := stopPollInterval, forceStopWait
	stopPollInterval, forceStopWait = 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		stopPollInterval, forceStopWait = oldInterval, oldForce
	})
}

// TestStopGracefully 覆盖三条路径：软关机生效、软关机超时后强制关机、
// 平台不支持软关机时直接强制关机。
func TestStopGracefully(t *testing.T) {
	shortenPowerWaits(t)

	cases := []struct {
		name       string
		graceful   bool
		stopOnSoft bool
		want       []model.VMPowers
	}{
		{"平台软关机生效", true, true, []model.VMPowers{model.VMPowerSClose}},
		{"软关机超时后强制关机", true, false, []model.VMPowers{model.VMPowerSClose, model.VMPowerHClose}},
		{"平台不支持软关机直接强制关机", false, false, []model.VMPowers{model.VMPowerHClose}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newGracefulStub(t, tc.graceful, tc.stopOnSoft)
			if err := d.StopGracefully("vm-1", 100*time.Millisecond); err != nil {
				t.Fatalf("StopGracefully 返回错误: %v", err)
			}
			if got := d.calls(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("电源调用顺序 = %v，期望 %v", got, tc.want)
			}
			if state := d.GetPower("vm-1"); state != "已关机" {
				t.Fatalf("关机后平台状态 = %q，期望 已关机", state)
			}
		})
	}
}

// noStopDriver 的强制关机不生效，用于验证超时判定与「不假成功」。
type noStopDriver struct {
	*gracefulStubDriver
}

func (d *noStopDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	return &model.ZMessage{Success: true, Action: "VMPowers"}, nil
}

// TestStopGracefullyNeverFakeSuccess 强制关机后虚拟机仍在运行，必须返回错误。
func TestStopGracefullyNeverFakeSuccess(t *testing.T) {
	shortenPowerWaits(t)

	d := &noStopDriver{gracefulStubDriver: newGracefulStub(t, false, false)}
	d.SetSelf(d)
	if err := d.StopGracefully("vm-1", 50*time.Millisecond); err == nil {
		t.Fatal("强制关机后虚拟机仍在运行，StopGracefully 不应返回成功")
	}
}

// TestGracefulShutdownCapability 平台软关机能力必须与驱动真实实现一致。
func TestGracefulShutdownCapability(t *testing.T) {
	want := map[string]bool{
		"PromoxSetup": true,  // PVE /status/shutdown（ACPI）
		"vSphereESXi": true,  // ShutdownGuest（VMware Tools）
		"QEMUServer":  true,  // virsh shutdown（ACPI）
		"LxContainer": true,  // lxc stop（信号）
		"HyperVSetup": true,  // Stop-VM（集成服务）
		"VMWareSetup": true,  // vmrest shutdown（VMware Tools）
		"VirtualBoxs": true,  // acpipowerbutton
		"OCInterface": true,  // docker stop（SIGTERM）
		"SmolVM":      true,  // SendCtrlAltDel
		"MemuAndroid": false, // memuc 只有 stop，无优雅关机
	}

	for engine, expect := range want {
		engine, expect := engine, expect
		t.Run(engine, func(t *testing.T) {
			d := NewDriver(testConfig(t, engine))
			if d == nil {
				t.Fatalf("引擎 %s 未注册驱动实现", engine)
			}
			g, ok := d.(GracefulShutdownDriver)
			if !ok {
				t.Fatalf("引擎 %s 未实现 GracefulShutdownDriver", engine)
			}
			if got := g.SupportsGracefulShutdown(); got != expect {
				t.Fatalf("SupportsGracefulShutdown() = %v，期望 %v", got, expect)
			}
		})
	}
}
