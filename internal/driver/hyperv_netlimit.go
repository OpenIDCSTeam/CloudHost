// Package driver Hyper-V 按虚拟机上下行带宽限速。
//
// 【默认不安装、不启动】NetLimiter.sys 在部分宿主上加载后会导致虚拟交换机
// 网络中断，因此默认部署由 nlFeatureEnabled 关闭（见 netlimiter.go）：
// 主机加载、创建虚拟机、改配、Crontab 都不会释放驱动文件或创建内核服务。
// 代码与运维接口完整保留：显式调用 InstallBandwidthDriver 即可启用，
// 或把 nlFeatureEnabled 改回 true 重新编译。
//
// Hyper-V 原生的 Set-VMNetworkAdapter -MaximumBandwidth 只能限制网卡双向
// 合计带宽，无法区分上行/下行。这里通过随附的 NetLimiter WFP 驱动
// （\\Device\\TransMonitor）按虚拟机网卡 MAC 下发规则，实现：
//
//	上行（虚拟机发出）→ 规则 RateUp  字段
//	下行（虚拟机接收）→ 规则 RateDown 字段
//
// 规则常驻驱动内核表，与虚拟机电源状态无关；主机重启或驱动重载后由
// Crontab 周期同步自动恢复。
package driver

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// hvLimitInterval 全量同步最小间隔，避免每轮状态采集都去枚举网卡。
const hvLimitInterval = 30 * time.Second

// HyperVDriver 实现限速驱动生命周期接口（编译期约束）。
var _ BandwidthDriverProvider = (*HyperVDriver)(nil)

// hvAdapterInfo Hyper-V 虚拟网卡条目（Get-VMNetworkAdapter 输出）
type hvAdapterInfo struct {
	VM  string `json:"vm"`
	ID  string `json:"id"`
	MAC string `json:"mac"`
}

// hvDriverPath 返回限速驱动落地路径（支持 extend_data.net_limit_driver 覆盖）。
func (d *HyperVDriver) hvDriverPath() string {
	return defaultNetLimiterPath(d.Config)
}

// hvNetworkAdapters 枚举宿主机全部虚拟机网卡 MAC。
// 返回 map：Hyper-V GUID（小写）与虚拟机名均指向该虚拟机的 MAC 列表。
func (d *HyperVDriver) hvNetworkAdapters() (map[string][][6]byte, error) {
	script := `$ErrorActionPreference='SilentlyContinue'; ` +
		`Get-VMNetworkAdapter -ErrorAction SilentlyContinue | ` +
		`Where-Object { -not $_.IsManagementOs } | ` +
		`ForEach-Object { [pscustomobject]@{ vm=$_.VMName; id=[string]$_.VMId; mac=$_.MacAddress } } | ` +
		`ConvertTo-Json -Compress`

	out, err := d.execPS(script)
	if err != nil {
		return nil, fmt.Errorf("枚举虚拟机网卡失败: %v", err)
	}
	result := make(map[string][][6]byte)
	body := strings.TrimSpace(out)
	if body == "" || body == "null" {
		return result, nil
	}

	var items []hvAdapterInfo
	if strings.HasPrefix(body, "[") {
		if err := jsonUnmarshalLoose(body, &items); err != nil {
			return nil, fmt.Errorf("解析虚拟机网卡失败: %w", err)
		}
	} else {
		var one hvAdapterInfo
		if err := jsonUnmarshalLoose(body, &one); err != nil {
			return nil, fmt.Errorf("解析虚拟机网卡失败: %w", err)
		}
		items = append(items, one)
	}

	for _, it := range items {
		mac, err := parseMAC(it.MAC)
		if err != nil {
			continue
		}
		keys := []string{strings.ToLower(strings.TrimSpace(it.ID)), strings.TrimSpace(it.VM)}
		for _, key := range keys {
			if key == "" {
				continue
			}
			if !slices.Contains(result[key], mac) {
				result[key] = append(result[key], mac)
			}
		}
	}
	return result, nil
}

// hvMACsOf 按 VmUUID（Hyper-V GUID）优先、虚拟机名兜底匹配网卡 MAC。
func hvMACsOf(adapters map[string][][6]byte, cfg *model.VMConfig) [][6]byte {
	if cfg == nil {
		return nil
	}
	keys := []string{strings.ToLower(strings.TrimSpace(cfg.VmUUID)), strings.TrimSpace(cfg.OsName)}
	var macs [][6]byte
	for _, key := range keys {
		if key == "" {
			continue
		}
		for _, mac := range adapters[key] {
			if !slices.Contains(macs, mac) {
				macs = append(macs, mac)
			}
		}
	}
	return macs
}

// hvLimitValues 依据虚拟机配置换算 (上行, 下行) 字节/秒。
func hvLimitValues(cfg *model.VMConfig) (uint64, uint64) {
	if cfg == nil {
		return nlUnlimited, nlUnlimited
	}
	return mbpsToBytesPerSec(cfg.SpeedU), mbpsToBytesPerSec(cfg.SpeedD)
}

// hvLimitWarn 生成限速下发失败时的提示后缀（成功返回空串）。
func hvLimitWarn(err error) string {
	if err == nil {
		return ""
	}
	return "（带宽限速下发失败：" + err.Error() + "）"
}

// hvLimitDue 判断是否到了本轮同步时间（并记录本次尝试时间）。
func (d *HyperVDriver) hvLimitDue() bool {
	d.hvLimitMu.Lock()
	defer d.hvLimitMu.Unlock()
	if !d.hvLimitAt.IsZero() && time.Since(d.hvLimitAt) < hvLimitInterval {
		return false
	}
	d.hvLimitAt = time.Now()
	return true
}

// EnsureBandwidthDriver 按需安装并启动内核限速驱动（幂等）。
//
// 触发时机：主机加载 / 启用、agent 启动加载配置。
// 当前没有任何虚拟机配置带宽时直接跳过，避免为不用的功能加载内核组件；
// 管理员手动卸载过的驱动不会被后台流程装回（需显式 InstallBandwidthDriver）。
func (d *HyperVDriver) EnsureBandwidthDriver() error {
	if !nlFeatureEnabled {
		// 默认部署不安装、不启动，也不去动宿主上已有的服务：
		// 主机加载 / 配置重载都会被这条分支挡住，内核组件不会被拉起。
		return nil
	}
	if !nlSupported() {
		return errNetLimiterUnsupported
	}
	if len(d.hvLimitedConfigs()) == 0 {
		return nil
	}
	return d.installBandwidthDriver(false)
}

// InstallBandwidthDriver 显式安装（运维接口使用）：解除手动卸载抑制并强制加载。
//
// 这是唯一的非默认入口，专门用于需要恢复限速时人工触发；
// 默认流程（主机加载 / 创建 / 改配 / Crontab）不会走到这里。
func (d *HyperVDriver) InstallBandwidthDriver() error {
	if !nlSupported() {
		return errNetLimiterUnsupported
	}
	return d.installBandwidthDriver(true)
}

// installBandwidthDriver 驱动安装主流程：force=false 时尊重手动卸载标记。
func (d *HyperVDriver) installBandwidthDriver(force bool) error {
	wasReady := netLimiterAvailable()
	d.hvLimitMu.Lock()
	d.hvLimitAt = time.Time{}
	d.hvLimitMu.Unlock()

	var err error
	if force {
		err = netLimiterEnsureForce(d.hvDriverPath())
	} else {
		err = netLimiterEnsure(d.hvDriverPath())
	}
	if err != nil {
		return err
	}
	if wasReady {
		// 已就绪（主机配置被重新保存等场景）：不做全量同步，交给 Crontab 的节流同步
		return nil
	}
	// 首次就绪：立即补一次全量同步，避免最多一个调度周期的限速空窗
	return d.hvSyncBandwidth()
}

// UninstallBandwidthDriver 停止并删除内核服务与驱动文件。
//
// 卸载后虚拟机的限速立即失效，且后台 Crontab / 创建虚拟机不会把驱动装回
// （机器级抑制标记，跨主机实例保留）；需要恢复时调用 InstallBandwidthDriver，
// 或重启 agent 服务（内存标记复位后按配置重新加载）。
func (d *HyperVDriver) UninstallBandwidthDriver() error {
	d.hvLimitMu.Lock()
	d.hvLimitAt = time.Time{}
	d.hvLimitMu.Unlock()
	return netLimiterUninstall(d.hvDriverPath())
}

// BandwidthDriverStatus 返回限速驱动的运行状态，供运维接口自检。
func (d *HyperVDriver) BandwidthDriverStatus() map[string]interface{} {
	st := netLimiterReport(d.hvDriverPath())
	rules := st.Rules
	out := map[string]interface{}{
		"enabled":     st.Enabled,
		"supported":   st.Supported,
		"installed":   st.Installed,
		"running":     st.Running,
		"device_open": st.DeviceOpen,
		"rules":       rules,
		"driver_path": st.DriverPath,
		"uninstalled": st.Disabled,
	}
	if rules > 0 {
		if list, err := netLimiterRules(); err == nil {
			items := make([]map[string]interface{}, 0, len(list))
			for _, r := range list {
				items = append(items, map[string]interface{}{
					"mac":       formatMAC(r.MAC),
					"rate_up":   rateLabel(r.RateUp),
					"rate_down": rateLabel(r.RateDown),
				})
			}
			out["rule_list"] = items
		}
	}
	return out
}

// rateLabel 把驱动内的字节/秒还原成人类可读的带宽描述。
func rateLabel(bytesPerSec uint64) interface{} {
	if bytesPerSec == nlUnlimited {
		return "unlimited"
	}
	return fmt.Sprintf("%d B/s (%.1f Mbps)", bytesPerSec, float64(bytesPerSec)*8/1024/1024)
}

// hvLimitedConfigs 取出配置了限速的虚拟机快照（已软删除的跳过）。
func (d *HyperVDriver) hvLimitedConfigs() []*model.VMConfig {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cfgs := make([]*model.VMConfig, 0, len(d.VmSaving))
	for _, cfg := range d.VmSaving {
		if cfg == nil || cfg.VmDeleted {
			continue
		}
		up, down := hvLimitValues(cfg)
		if up == nlUnlimited && down == nlUnlimited {
			continue
		}
		cfgs = append(cfgs, cfg)
	}
	return cfgs
}

// hvApplyVMBandwidth 单独下发一台虚拟机的限速（创建 / 修改配置后立即生效）。
//
// 默认不安装驱动：驱动未就绪时直接返回成功，既不影响创建 / 改配流程，
// 也不会在返回消息里带出「限速下发失败」的干扰信息；
// 管理员显式安装过驱动后，这里自动恢复正常下发。
func (d *HyperVDriver) hvApplyVMBandwidth(cfg *model.VMConfig) error {
	if !nlFeatureEnabled && !netLimiterAvailable() {
		return nil
	}
	if cfg == nil {
		return nil
	}
	if !nlSupported() {
		return errNetLimiterUnsupported
	}
	up, down := hvLimitValues(cfg)
	if up == nlUnlimited && down == nlUnlimited {
		return d.hvClearVMBandwidth(cfg)
	}
	if err := netLimiterEnsure(d.hvDriverPath()); err != nil {
		return err
	}
	adapters, err := d.hvNetworkAdapters()
	if err != nil {
		return err
	}
	macs := hvMACsOf(adapters, cfg)
	if len(macs) == 0 {
		return fmt.Errorf("未匹配到虚拟机 %s 的网卡 MAC 地址", cfg.VmUUID)
	}
	for _, mac := range macs {
		if err := netLimiterSetRule(mac, up, down); err != nil {
			return err
		}
	}
	return nil
}

// hvClearVMBandwidth 取消一台虚拟机的限速（网卡规则改为不限）。
func (d *HyperVDriver) hvClearVMBandwidth(cfg *model.VMConfig) error {
	if cfg == nil || !nlSupported() || !netLimiterAvailable() {
		return nil
	}
	adapters, err := d.hvNetworkAdapters()
	if err != nil {
		return err
	}
	for _, mac := range hvMACsOf(adapters, cfg) {
		if err := netLimiterClearRule(mac); err != nil {
			return err
		}
	}
	return nil
}

// hvSyncBandwidth 全量同步限速规则（供 Crontab 周期调用）。
// 驱动重启后规则表会清空，周期同步可自动恢复。
func (d *HyperVDriver) hvSyncBandwidth() error {
	// 驱动未就绪直接返回：Crontab 每轮不再为限速去枚举虚拟机网卡（一次 PowerShell）
	if !nlFeatureEnabled && !netLimiterAvailable() {
		return nil
	}
	if !nlSupported() {
		return nil
	}
	if !d.hvLimitDue() {
		return nil
	}
	cfgs := d.hvLimitedConfigs()
	if len(cfgs) == 0 {
		return nil
	}
	if err := netLimiterEnsure(d.hvDriverPath()); err != nil {
		return err
	}
	adapters, err := d.hvNetworkAdapters()
	if err != nil {
		return err
	}
	var firstErr error
	for _, cfg := range cfgs {
		up, down := hvLimitValues(cfg)
		macs := hvMACsOf(adapters, cfg)
		if len(macs) == 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("未匹配到虚拟机 %s 的网卡 MAC 地址", cfg.VmUUID)
			}
			continue
		}
		for _, mac := range macs {
			if err := netLimiterSetRule(mac, up, down); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
