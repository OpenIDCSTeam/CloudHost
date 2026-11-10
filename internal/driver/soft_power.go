// Package driver 软电源操作后台监控。
//
// 对应 Python 版 BasicServer.soft_pwr / _monitor_power_operation：
// 软关机与软重启是「请求」而非「结果」——平台受理后虚拟机内部还需要时间
// 走完关机流程，期间状态停留在中间态（ON_STOP）。若不做监控，
// 前端会一直显示「关闭中」，且后续操作（改配置、挂盘、备份）会因状态判断失败被拒。
//
// 本文件以 5 秒间隔、最长 5 分钟轮询平台实际状态，直到状态稳定或超时。
package driver

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// 监控参数（与 Python 版一致：间隔 5 秒，最长 5 分钟）
const (
	softPowerCheckInterval = 5 * time.Second
	softPowerMaxDuration   = 300 * time.Second

	// gracefulStopWait 平台软关机后等待 guest 真正断电的上限。
	// PVE 的 qm shutdown 默认超时 60s，超过仍未断电说明 guest 没有响应
	// ACPI 关机（或没有可用的关机代理），继续等待只会拖住删除/重装流程。
	gracefulStopWait = 60 * time.Second
)

// 等待关机状态收敛的轮询参数，声明为变量仅用于测试注入更短的间隔。
var (
	// stopPollInterval 关机等待的轮询间隔：比状态监控更密，避免多余等待。
	stopPollInterval = 2 * time.Second
	// forceStopWait 强制断电后等待平台状态收敛的上限。
	forceStopWait = 30 * time.Second
)

// SoftPowerMonitor 后台监控软电源操作直到状态变化或超时。
//
// expected 为调用方设置的中间状态（软关机/软重启均为 ON_STOP）。
// 监控过程中以「平台实际状态」回写内存，保证前端看到的是真实状态。
func (b *BaseDriver) SoftPowerMonitor(vmUUID string, expected model.VMPowers) {
	self := b.SelfDriver()
	if self == nil {
		return
	}

	go func() {
		deadline := time.Now().Add(softPowerMaxDuration)
		for time.Now().Before(deadline) {
			time.Sleep(softPowerCheckInterval)

			// 虚拟机已被删除则停止监控
			b.mu.RLock()
			cfg, exists := b.VmSaving[vmUUID]
			current := model.VMPowerUnknown
			if exists {
				current = cfg.VmFlag
			}
			b.mu.RUnlock()
			if !exists {
				return
			}

			// 内存状态已脱离中间态：说明操作已完成或被其它操作改变
			if current != expected {
				return
			}

			// 以平台实际状态为准回写
			actual := self.GetPower(vmUUID)
			if state, ok := NormalizePowerState[actual]; ok {
				b.SetVMPowerState(vmUUID, state)
				if state != expected {
					return
				}
			}
		}

		// 超时仍未完成：回落到平台实际状态，避免状态永久卡在「关闭中」
		b.SetVMPowerState(vmUUID, model.VMPowerUnknown)
	}()
}

// StopGracefully 先请求平台软关机，等待 guest 真正断电；超时或平台不支持时
// 强制关机。
//
// 供「必须先关机」的流程（删除、重装、还原等）复用：这些流程过去直接调用
// 强制断电，运行中的系统没有机会落盘。只有声明了 SupportsGracefulShutdown()
// 的驱动才会先走平台软关机；其余驱动保持原来的强制关机语义，避免无谓等待。
//
// gracefulWait 为软关机的等待上限，<=0 时使用默认值（60 秒）。
func (b *BaseDriver) StopGracefully(vmUUID string, gracefulWait time.Duration) error {
	self := b.SelfDriver()
	if self == nil {
		return fmt.Errorf("驱动未初始化")
	}
	if gracefulWait <= 0 {
		gracefulWait = gracefulStopWait
	}

	if g, ok := self.(GracefulShutdownDriver); ok && g.SupportsGracefulShutdown() {
		msg, err := self.PowerControl(vmUUID, model.VMPowerSClose)
		if err == nil && msg != nil && !msg.Success {
			err = errors.New(msg.Message)
		}
		switch {
		case err != nil:
			log.Printf("[power] %s 平台软关机失败，改用强制关机: %v", vmUUID, err)
		case b.waitPowerStopped(self, vmUUID, gracefulWait):
			return nil
		default:
			log.Printf("[power] %s 软关机等待 %s 仍未断电，改用强制关机", vmUUID, gracefulWait)
		}
	}

	msg, err := self.PowerControl(vmUUID, model.VMPowerHClose)
	if err == nil && msg != nil && !msg.Success {
		err = errors.New(msg.Message)
	}
	if err != nil {
		return err
	}
	if !b.waitPowerStopped(self, vmUUID, forceStopWait) {
		return fmt.Errorf("虚拟机未在 %s 内关机", forceStopWait)
	}
	return nil
}

// waitPowerStopped 轮询平台状态，直到虚拟机真正关机或超时。
func (b *BaseDriver) waitPowerStopped(self HostDriver, vmUUID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if state, ok := NormalizePowerState[self.GetPower(vmUUID)]; ok && state == model.VMPowerStopped {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(stopPollInterval)
	}
}
