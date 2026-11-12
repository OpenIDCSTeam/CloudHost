// Package driver NetLimiter 内核限速驱动用户态客户端。
//
// Hyper-V 自带的带宽管理（Set-VMNetworkAdapter -MaximumBandwidth）只能限制
// 虚拟机网卡的双向合计带宽，无法分别限制上行/下行。随附的 NetLimiter.sys
// 是基于 WFP 的限速驱动：它在虚拟交换机 MAC 帧层
// （FWPS_LAYER_INBOUND/OUTBOUND_MAC_FRAME_ETHERNET）按虚拟机网卡 MAC 匹配规则，
// 用「每秒字节计数 + 每秒定时清零」的方式分别限制入方向（下行）与出方向（上行）。
//
// 驱动对外接口（METHOD_BUFFERED + FILE_ANY_ACCESS，DeviceType=0x22）：
//
//	0x00222000  写入 / 更新一条限速规则（输入 64 字节，见 nlRule）
//	0x00222004  全局开关（输入 4 字节，1=启用）
//	0x00222008  删除某 MAC 的运行时统计节点（输入 6 字节 MAC）
//	0x0022200C  查询某 MAC 的运行时统计（输入 64 字节，前 4 字节未用，随后为 MAC）
//	0x00222010  枚举规则表（输入输出同缓冲：u32 条数 + u32 缓冲长度 + 64 字节规则…）
//
// 规则以 MAC 为键，特殊 MAC FF-FF-FF-FF-FF-FF 表示「所有虚拟机」的全局规则。
// 六个 u64 字段中，速率单位为字节/秒，流量配额单位为字节，-1 表示不限。
package driver

import (
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// errNetLimiterUnsupported 非 Windows 平台不支持 NetLimiter 限速驱动。
var errNetLimiterUnsupported = errors.New("当前系统不支持 NetLimiter 限速驱动（仅 Windows Hyper-V 宿主可用）")

// errNetLimiterDisabled 管理员显式卸载驱动后，后台自动安装被抑制。
// 显式安装（EnsureBandwidthDriver / 运维接口）可解除该状态。
var errNetLimiterDisabled = errors.New("限速驱动已被手动卸载，需重新安装后才能限速")

// nlFeatureEnabled 默认部署是否启用内核限速驱动。
//
// 现状：NetLimiter.sys 在部分宿主上加载后会导致虚拟交换机网络中断，
// 因此默认关闭。关闭后默认流程：
//   - 不释放驱动文件、不创建内核服务、不启动服务
//   - 不主动卸载：宿主上已有的服务不会被默认流程改动
//   - 不下发任何限速规则（创建、改配、Crontab 同步全部跳过）
//
// 代码与其运维接口保留：管理员显式调用 InstallBandwidthDriver（运维接口）
// 仍可解除限制并安装，装好之后限速逻辑恢复正常。驱动字节仍内嵌在二进制里。
const nlFeatureEnabled = false

// errNetLimiterRemoved 默认流程已停用自动安装限速驱动。
// 仅在非显式（force=false）的安装入口返回，提示调用方改走运维接口。
var errNetLimiterRemoved = errors.New("带宽限速驱动默认不安装（加载后会导致虚拟交换机断网），如需启用请显式调用安装接口")

// netLimiterDriverBytes 内置的 NetLimiter.sys（WFP 限速驱动）。
// 运行时释放到 %SystemRoot%\System32\drivers\ 并以内核服务方式加载。
//
// 文件签名：CN=Microsoft Windows Hardware Compatibility Publisher（WHQL 验证签名），
// 时间戳有效，可在启用安全启动的 Windows 上加载。
// 校验值：MD5 9115155CC3AE6AAD6E86B9DCAC254274
//
//	SHA256 CC857CCE8E8DFC9BE1672D5301E8D8384CFBE37A90858A23DF57237F786667BA
//
//go:embed assets/NetLimiter.sys
var netLimiterDriverBytes []byte

const (
	// nlDevicePath 驱动控制设备（DriverEntry 中 IoCreateDevice 的 \Device\TransMonitor）
	nlDevicePath = `\\.\TransMonitor`
	// nlServiceName 内核服务名（与参考工具保持一致，避免重复安装同一驱动）
	nlServiceName = "NetLimiter.sys"
	// nlDriverFileName 驱动文件名
	nlDriverFileName = "NetLimiter.sys"

	nlIoctlSetRule uint32 = 0x00222000
	nlIoctlEnable  uint32 = 0x00222004
	nlIoctlDelete  uint32 = 0x00222008
	nlIoctlQuery   uint32 = 0x0022200C
	nlIoctlEnum    uint32 = 0x00222010

	// nlRuleSize 规则结构体长度（驱动侧按 0x40 定长拷贝）
	nlRuleSize = 64
	// nlRuleCapacity 驱动规则表容量（驱动内 table[1000] + 全局槽）
	nlRuleCapacity = 1000
	// nlRuleTag 规则首字段固定值（与参考实现一致，驱动不解析）
	nlRuleTag uint32 = 0xFFFFFFFF
	// nlUnlimited 不限速 / 不限额标记
	nlUnlimited uint64 = ^uint64(0)
)

// nlRule 驱动规则结构（64 字节，字段偏移与驱动内部结构一一对应）。
type nlRule struct {
	Tag        uint32  // 0x00 固定 0xFFFFFFFF
	MAC        [6]byte // 0x04 虚拟机网卡 MAC
	_          [2]byte // 0x0A 对齐填充
	InUse      uint32  // 0x0C 1=有效
	RateTotal  uint64  // 0x10 双向合计速率上限（字节/秒）
	RateUp     uint64  // 0x18 上行（虚拟机发出）速率上限（字节/秒）
	QuotaDown  uint64  // 0x20 下行累计流量上限（字节）
	QuotaTotal uint64  // 0x28 双向累计流量上限（字节）
	QuotaUp    uint64  // 0x30 上行累计流量上限（字节）
	RateDown   uint64  // 0x38 下行（虚拟机接收）速率上限（字节/秒）
}

// marshal 序列化为驱动要求的 64 字节小端结构。
func (r *nlRule) marshal() []byte {
	b := make([]byte, nlRuleSize)
	binary.LittleEndian.PutUint32(b[0:], r.Tag)
	copy(b[4:], r.MAC[:])
	binary.LittleEndian.PutUint32(b[12:], r.InUse)
	binary.LittleEndian.PutUint64(b[16:], r.RateTotal)
	binary.LittleEndian.PutUint64(b[24:], r.RateUp)
	binary.LittleEndian.PutUint64(b[32:], r.QuotaDown)
	binary.LittleEndian.PutUint64(b[40:], r.QuotaTotal)
	binary.LittleEndian.PutUint64(b[48:], r.QuotaUp)
	binary.LittleEndian.PutUint64(b[56:], r.RateDown)
	return b
}

// unmarshalNLRule 解析驱动回传的单条规则。
func unmarshalNLRule(b []byte) (nlRule, bool) {
	var r nlRule
	if len(b) < nlRuleSize {
		return r, false
	}
	r.Tag = binary.LittleEndian.Uint32(b[0:])
	copy(r.MAC[:], b[4:10])
	r.InUse = binary.LittleEndian.Uint32(b[12:])
	r.RateTotal = binary.LittleEndian.Uint64(b[16:])
	r.RateUp = binary.LittleEndian.Uint64(b[24:])
	r.QuotaDown = binary.LittleEndian.Uint64(b[32:])
	r.QuotaTotal = binary.LittleEndian.Uint64(b[40:])
	r.QuotaUp = binary.LittleEndian.Uint64(b[48:])
	r.RateDown = binary.LittleEndian.Uint64(b[56:])
	return r, true
}

var (
	// nlMu 保护驱动句柄与状态（设备全局唯一，多主机实例共用）
	nlMu sync.Mutex
	// nlReady 驱动已加载、设备已打开且全局开关已启用
	nlReady bool
	// nlDisabled 管理员显式卸载后置位，抑制后台自动安装（机器级状态，多主机实例共用）
	nlDisabled bool
	// nlDriverPath 最近一次使用的驱动文件路径
	nlDriverPath string
)

// mbpsToBytesPerSec 把配置里的 Mbps 换算成驱动使用的字节/秒。
//
// 参考实现按 1 Mbps = 2^20 bit/s 换算，这里保持一致，
// 以便与同一驱动的既有规则表现相同；<=0 视为不限速。
func mbpsToBytesPerSec(mbps int) uint64 {
	if mbps <= 0 {
		return nlUnlimited
	}
	return uint64(mbps) * 1024 * 1024 / 8
}

// defaultNetLimiterPath 返回驱动文件落地路径。
// 可通过主机扩展配置 extend_data.net_limit_driver 覆盖（便于离线/自定义部署）。
func defaultNetLimiterPath(cfg *model.HSConfig) string {
	if cfg != nil && cfg.ExtendData != nil {
		if v, ok := cfg.ExtendData["net_limit_driver"].(string); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	root := strings.TrimRight(os.Getenv("SystemRoot"), `\/`)
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", nlDriverFileName)
}

// netLimiterAvailable 报告限速驱动是否已就绪。
// 默认流程从不安装驱动，因此未显式安装时恒为 false。
func netLimiterAvailable() bool {
	nlMu.Lock()
	defer nlMu.Unlock()
	return nlReady
}

// netLimiterStatus 限速驱动的运行状态快照（用于运维自检）。
type netLimiterStatus struct {
	Enabled    bool   // 功能是否启用（false = 代码层面已停用，不会加载驱动）
	Supported  bool   // 当前平台是否支持（仅 Windows）
	Installed  bool   // 内核服务是否已注册
	Running    bool   // 内核服务是否在运行
	DeviceOpen bool   // 控制设备是否已打开
	Disabled   bool   // 已被管理员显式卸载（后台不再自动安装）
	Rules      int    // 驱动内规则条数（无法读取时为 -1）
	DriverPath string // 驱动文件路径
}

// netLimiterReport 查询驱动状态；驱动未就绪时不会触发安装。
func netLimiterReport(driverPath string) netLimiterStatus {
	st := netLimiterStatus{Enabled: nlFeatureEnabled, Supported: nlSupported(), Rules: -1}
	if st.Supported {
		installed, running, err := nlQueryDriver()
		if err == nil {
			st.Installed = installed
			st.Running = running
		}
	}
	nlMu.Lock()
	if driverPath == "" {
		driverPath = nlDriverPath
	}
	if driverPath == "" {
		driverPath = defaultNetLimiterPath(nil)
	}
	st.DriverPath = driverPath
	st.DeviceOpen = nlReady
	st.Disabled = nlDisabled
	nlMu.Unlock()

	if st.DeviceOpen {
		if rules, err := netLimiterRules(); err == nil {
			st.Rules = len(rules)
		}
	}
	return st
}

// netLimiterUninstall 停止并删除内核服务与驱动文件，用于「移除此主机 / 彻底关闭限速」。
// 规则表随驱动卸载一并消失，虚拟机会立刻恢复不限速。
func netLimiterUninstall(driverPath string) error {
	nlMu.Lock()
	defer nlMu.Unlock()
	// 先置位再卸载：即使下面的停服务/删文件失败，也不会被后台流程立刻装回去
	nlDisabled = true
	_ = nlCloseDevice()
	nlReady = false
	if !nlSupported() {
		return errNetLimiterUnsupported
	}
	if driverPath == "" {
		driverPath = nlDriverPath
	}
	if driverPath == "" {
		driverPath = defaultNetLimiterPath(nil)
	}
	if err := nlUninstallDriver(driverPath); err != nil {
		return err
	}
	nlDriverPath = ""
	return nil
}

// netLimiterEnsure 幂等地安装并启动驱动、打开设备并启用全局开关。
//
// 默认路径（主机加载 / 创建虚拟机 / 改配 / Crontab）一律走这里：
// 默认部署不安装驱动，直接返回 errNetLimiterRemoved；
// 只有显式安装入口 netLimiterEnsureForce 才能绕过该限制。
//
// 管理员显式卸载过驱动时返回 errNetLimiterDisabled。
func netLimiterEnsure(driverPath string) error {
	nlMu.Lock()
	defer nlMu.Unlock()
	// 默认部署不安装驱动（nlReady 恒为 false）；
	// 管理员显式安装过之后 nlReady=true，这里恢复正常的幂等语义。
	if !nlFeatureEnabled && !nlReady {
		return errNetLimiterRemoved
	}
	return nlEnsureLocked(driverPath)
}

// netLimiterEnsureForce 显式安装入口：解除「手动卸载」抑制后安装。
func netLimiterEnsureForce(driverPath string) error {
	nlMu.Lock()
	defer nlMu.Unlock()
	nlDisabled = false
	return nlEnsureLocked(driverPath)
}

// nlEnsureLocked 调用方需持有 nlMu。
func nlEnsureLocked(driverPath string) error {
	if nlReady {
		return nil
	}
	if nlDisabled {
		return errNetLimiterDisabled
	}
	if strings.TrimSpace(driverPath) != "" {
		nlDriverPath = driverPath
	}
	if nlDriverPath == "" {
		nlDriverPath = defaultNetLimiterPath(nil)
	}
	if err := nlInstallDriver(nlDriverPath, netLimiterDriverBytes); err != nil {
		return fmt.Errorf("加载限速驱动失败: %w", err)
	}
	if err := nlOpenDevice(); err != nil {
		return fmt.Errorf("打开限速设备 %s 失败: %w", nlDevicePath, err)
	}
	enable := make([]byte, 4)
	binary.LittleEndian.PutUint32(enable, 1)
	if _, err := nlIoctl(nlIoctlEnable, enable, 0); err != nil {
		_ = nlCloseDevice()
		return fmt.Errorf("启用限速驱动失败: %w", err)
	}
	nlReady = true
	return nil
}

// nlResetLocked 设备异常时复位，等待下一轮重新加载。
func nlResetLocked() {
	_ = nlCloseDevice()
	nlReady = false
}

// netLimiterSetRule 写入 / 更新一条按 MAC 的限速规则。
//
// 只设置「上行速率」「下行速率」两个字段，其余速率/流量上限统一写 -1（不限），
// 避免误伤业务语义（流量配额仍由主控侧统计，不在驱动里硬切流量）。
func netLimiterSetRule(mac [6]byte, upBytes, downBytes uint64) error {
	nlMu.Lock()
	defer nlMu.Unlock()
	if err := nlEnsureLocked(""); err != nil {
		return err
	}
	rule := nlRule{
		Tag:        nlRuleTag,
		MAC:        mac,
		InUse:      1,
		RateTotal:  nlUnlimited,
		RateUp:     upBytes,
		QuotaDown:  nlUnlimited,
		QuotaTotal: nlUnlimited,
		QuotaUp:    nlUnlimited,
		RateDown:   downBytes,
	}
	if _, err := nlIoctl(nlIoctlSetRule, rule.marshal(), 0); err != nil {
		nlResetLocked()
		return fmt.Errorf("下发限速规则失败(%s): %w", formatMAC(mac), err)
	}
	return nil
}

// netLimiterClearRule 取消某块网卡的限速（规则改为全部不限）。
func netLimiterClearRule(mac [6]byte) error {
	return netLimiterSetRule(mac, nlUnlimited, nlUnlimited)
}

// netLimiterRules 枚举驱动当前生效的规则表（用于自检 / 排障）。
func netLimiterRules() ([]nlRule, error) {
	nlMu.Lock()
	defer nlMu.Unlock()
	if err := nlEnsureLocked(""); err != nil {
		return nil, err
	}
	bufSize := 8 + nlRuleCapacity*nlRuleSize
	buf := make([]byte, bufSize)
	binary.LittleEndian.PutUint32(buf[0:], 0)               // 输出：有效规则条数
	binary.LittleEndian.PutUint32(buf[4:], uint32(bufSize)) // 输入：缓冲区长度
	out, err := nlIoctl(nlIoctlEnum, buf, bufSize)
	if err != nil {
		nlResetLocked()
		return nil, fmt.Errorf("枚举限速规则失败: %w", err)
	}
	if len(out) < 8 {
		return nil, fmt.Errorf("枚举限速规则失败: 返回数据过短")
	}
	count := int(binary.LittleEndian.Uint32(out[0:]))
	if count <= 0 {
		return nil, nil
	}
	limit := (len(out) - 8) / nlRuleSize
	if count > limit {
		count = limit
	}
	rules := make([]nlRule, 0, count)
	for i := 0; i < count; i++ {
		start := 8 + i*nlRuleSize
		if r, ok := unmarshalNLRule(out[start : start+nlRuleSize]); ok {
			rules = append(rules, r)
		}
	}
	return rules, nil
}

// parseMAC 解析 "00-15-5D-0A-1B-2C" / "00155D0A1B2C" 等形式的 MAC。
func parseMAC(s string) ([6]byte, error) {
	var mac [6]byte
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ':', '-', '.', '{', '}', ' ':
			return -1
		}
		return r
	}, s)
	if len(clean) != 12 {
		return mac, fmt.Errorf("MAC 地址格式非法: %q", s)
	}
	if _, err := hex.Decode(mac[:], []byte(clean)); err != nil {
		return mac, fmt.Errorf("MAC 地址格式非法: %q", s)
	}
	if mac == ([6]byte{}) {
		return mac, fmt.Errorf("MAC 地址为空")
	}
	return mac, nil
}

// formatMAC 把 MAC 格式化为 00-15-5D-0A-1B-2C。
func formatMAC(mac [6]byte) string {
	return fmt.Sprintf("%02X-%02X-%02X-%02X-%02X-%02X",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}
