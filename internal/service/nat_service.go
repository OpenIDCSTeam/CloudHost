// Package service NAT端口映射 / 网络静态绑定下发服务（爱快路由器）
// 对应 Python 版 HostModule/NetsManager 的登录与 dnat/dhcp_static/arp 调用协议，
// 同时兼容爱快 3.x 与爱快 4.x（详见 ikuai_client.go）。
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/data"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// NATService NAT / DHCP / ARP 下发服务
type NATService struct {
	db       *data.DB
	resolver func(string) driver.HostDriver
	mu       sync.Mutex
	clients  map[string]*ikuaiClient
}

// NewNATService 创建 NAT 服务
func NewNATService(db *data.DB) *NATService {
	return &NATService{
		db:      db,
		clients: make(map[string]*ikuaiClient),
	}
}

// SetDriverResolver 注入驱动解析器（用于识别平台自有的下发通道）
func (s *NATService) SetDriverResolver(fn func(string) driver.HostDriver) {
	s.resolver = fn
}

// provider 返回主机驱动实现的平台自有端口映射通道（若支持）。
//
// 目前由容器类平台（Docker / LXD / SmolVM）通过宿主机 iptables 自持通道。
//
// 均未实现时回落到爱快路由器下发。
func (s *NATService) provider(hsName string) (driver.NatPortProvider, bool) {
	if s.resolver == nil {
		return nil, false
	}
	d := s.resolver(hsName)
	if d == nil {
		return nil, false
	}
	p, ok := d.(driver.NatPortProvider)
	return p, ok
}

// client 获取（并缓存）指定路由器的客户端。
// 缓存键使用归一化后的地址，避免大小写/末尾斜杠导致同一路由器被重复建连。
// token 为爱快 4.0 的个人 API 令牌，参与缓存键：改令牌后必须拿到新客户端。
func (s *NATService) client(routerAddr, user, pass, token string) *ikuaiClient {
	if strings.TrimSpace(routerAddr) == "" {
		return nil
	}
	base, _ := normalizeIKuaiAddr(routerAddr)
	key := base + "|" + user + "|" + pass + "|" + token
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[key]; ok {
		return c
	}
	c := newIKuaiClient(routerAddr, user, pass, token)
	s.clients[key] = c
	return c
}

// ============================================================
// 对外方法（写库 + 下发）
// ============================================================

// ApplyPortMap 添加端口映射
// 未指定外网端口时自动分配；平台自有通道优先，否则下发爱快路由器。
// nat 为指针，便于回填自动分配的端口。
//
// 端口分配同时避开两类占用：本地数据库中已分配的端口，以及爱快路由器上已存在的
// 端口映射（对齐 Python 版 BasicServer.PortsMap：先 get_port() 收集 wan_list 再分配）。
func (s *NATService) ApplyPortMap(hsName, vmUUID string, nat *model.PortData, userID int) error {
	if nat == nil {
		return fmt.Errorf("端口映射参数为空")
	}

	// 内网地址兜底：前端/自动流程常常只给外网与内网端口，
	// 爱快会直接以「参数错误: lan_addr」拒绝（整条规则下发失败）。
	// 这里用虚拟机第一块已分配 IP 的网卡补齐。
	if strings.TrimSpace(nat.LanAddr) == "" {
		nat.LanAddr = s.vmPrimaryIP(hsName, vmUUID)
	}
	if strings.TrimSpace(nat.LanAddr) == "" {
		return fmt.Errorf("缺少内网地址：虚拟机尚未分配 IP，请先为网卡分配 IP 后重试")
	}

	host, err := s.db.GetHost(hsName)
	if err != nil || host == nil {
		return fmt.Errorf("主机 %s 不存在", hsName)
	}

	provider, hasProvider := s.provider(hsName)

	// 路由器侧已占用的 WAN 端口：仅爱快通道需要（平台通道由云端自行管理端口）
	var ikuai *ikuaiClient
	routerPorts := map[int]bool{}
	if !hasProvider {
		if strings.TrimSpace(host.IKuaiAddr) == "" {
			return fmt.Errorf("主机 %s 未配置爱快路由器地址", hsName)
		}
		ikuai = s.client(host.IKuaiAddr, host.IKuaiUser, host.IKuaiPass, host.IKuaiToken)
		if ikuai == nil {
			return fmt.Errorf("爱快路由器配置无效")
		}
		// 读取失败按「路由器上没有占用」处理：与 Python 版 get_port() 失败后
		// wan_list 为空即继续下发一致，本地库占用仍会参与判断，不阻断业务。
		if ports, portsErr := ikuai.listWanPorts(); portsErr == nil {
			routerPorts = ports
		}
		if nat.WanPort != 0 && routerPorts[nat.WanPort] {
			return fmt.Errorf("外网端口 %d 已被路由器上的其它规则占用，请更换端口", nat.WanPort)
		}
	}

	// 平台自有下发通道
	if hasProvider {
		// 端口分配交给驱动：容器类（Docker/LXD/SmolVM）
		// 则需查询**宿主机 iptables 实际占用**才能选到真正可用的端口
		// （对齐 Python 版 BasicServer.PortsMap_TTY 的 allocate_port 语义）。
		// 指定了端口时驱动会自行校验宿主机占用。
		msg, err := provider.PortsMap(nat, true)
		if err != nil {
			return err
		}
		if msg != nil && !msg.Success {
			return fmt.Errorf("%s", msg.Message)
		}
		if nat.WanPort == 0 {
			return fmt.Errorf("平台通道未分配外网端口")
		}
		// 驱动分配后仍需防止覆盖其它虚拟机的端口归属
		if owner, found, err := s.db.NATPortOwner(hsName, nat.WanPort); err == nil && found && owner != vmUUID {
			return fmt.Errorf("外网端口 %d 已被虚拟机 %s 占用，请更换端口", nat.WanPort, owner)
		}
		return s.db.UpsertNATRule(hsName, vmUUID, userID, nat.WanPort, nat.LanPort, nat.LanAddr, nat.NatTips)
	}

	if nat.WanPort == 0 {
		port, allocErr := s.allocateWanPort(host, routerPorts)
		if allocErr != nil {
			return allocErr
		}
		nat.WanPort = port
	}

	// 端口占用检查：避免把已属于其它虚拟机的端口重新分配出去。
	// 数据库冲突分支是 UPSERT，若不做前置检查会静默改写归属。
	if owner, found, err := s.db.NATPortOwner(hsName, nat.WanPort); err == nil && found && owner != vmUUID {
		return fmt.Errorf("外网端口 %d 已被虚拟机 %s 占用，请更换端口", nat.WanPort, owner)
	}

	if err := ikuai.addPortMap(*nat); err != nil {
		return err
	}
	return s.db.UpsertNATRule(hsName, vmUUID, userID, nat.WanPort, nat.LanPort, nat.LanAddr, nat.NatTips)
}

// RevokePortMap 删除端口映射：下发删除后删库（下发失败不阻断，避免记录残留）
func (s *NATService) RevokePortMap(hsName, vmUUID string, nat model.PortData) error {
	if p, ok := s.provider(hsName); ok {
		_, _ = p.PortsMap(&nat, false)
		return s.db.DeleteNATRule(hsName, vmUUID, nat.WanPort)
	}

	if host, err := s.db.GetHost(hsName); err == nil && host != nil && host.IKuaiAddr != "" {
		if c := s.client(host.IKuaiAddr, host.IKuaiUser, host.IKuaiPass, host.IKuaiToken); c != nil {
			_ = c.deletePortMap(nat.LanPort, nat.LanAddr)
		}
	}
	return s.db.DeleteNATRule(hsName, vmUUID, nat.WanPort)
}

// allocateWanPort 在主机端口范围内分配未占用的外网端口。
//
// reserved 为路由器侧已被占用的端口（无爱快通道时传 nil），
// 与本地数据库中已分配的端口一起参与去重。
//
// 原实现在随机尝试失败后直接返回 low，即使该端口已被占用：
// 端口池接近耗尽时会必然返回冲突端口。现在改为返回明确错误。
//
// 注意：该实现仍不是原子预留，并发请求可能同时选中同一端口；
// 调用方需依赖 NATPortOwner 冲突检查与数据库唯一约束兜底。
func (s *NATService) allocateWanPort(host *data.HSConfigRow, reserved map[int]bool) (int, error) {
	low, high := portRange(host)

	localUsed := make(map[int]bool)
	if vms, err := s.db.GetVMSaving(host.HsName); err == nil {
		for _, vmJSON := range vms {
			vm, decodeErr := DecodeVMConfig(vmJSON)
			if decodeErr != nil || vm == nil {
				continue
			}
			for _, n := range vm.NatAll {
				localUsed[n.WanPort] = true
			}
		}
	}

	// 先做确定性扫描，保证端口耗尽时一定能给出准确结论
	if port, ok := pickWanPort(low, high, localUsed, reserved); ok {
		return port, nil
	}
	return 0, fmt.Errorf("主机 %s 的端口范围 %d-%d 已全部占用，无法自动分配", host.HsName, low, high)
}

// portRange 返回主机的端口分配范围（未配置时缺省 10000-60000）
func portRange(host *data.HSConfigRow) (int, int) {
	low := host.PortsStart
	if low == 0 {
		low = 10000
	}
	high := host.PortsClose
	if high == 0 {
		high = 60000
	}
	if high <= low {
		high = low + 1
	}
	return low, high
}

// pickWanPort 返回 [low, high] 内第一个既未被本地库分配、
// 也未被路由器占用的端口。两组来源都必须避开：
// localUsed 来自数据库中的虚拟机配置，reserved 来自路由器上现有映射
// （可能包含管理员手工添加、本地库并不知晓的规则）。
func pickWanPort(low, high int, localUsed, reserved map[int]bool) (int, bool) {
	for p := low; p <= high; p++ {
		if !localUsed[p] && !reserved[p] {
			return p, true
		}
	}
	return 0, false
}

// ============================================================
// 网络静态绑定（爱快：DHCP 静态地址 + ARP 绑定）
// 对应 Python 版 BasicServer.NetiKuai / IPBinder_ROS
// ============================================================

// 静态绑定缺省 DNS（对应 Python 版 add_dhcp 的默认值）
const (
	ikuaiDefaultDNS1 = "119.29.29.29"
	ikuaiDefaultDNS2 = "223.5.5.5"
)

// ikuaiClientFor 获取主机对应的爱快客户端。
//
// 返回 (nil, host, nil) 表示该主机未配置爱快路由器：静态绑定属于可选能力，
// 未配置时静默跳过（与 Python 版 NetiKuai 一致），不阻断虚拟机流程。
func (s *NATService) ikuaiClientFor(hsName string) (*ikuaiClient, *data.HSConfigRow, error) {
	host, err := s.db.GetHost(hsName)
	if err != nil {
		return nil, nil, fmt.Errorf("读取主机 %s 配置失败: %w", hsName, err)
	}
	if host == nil {
		return nil, nil, fmt.Errorf("主机 %s 不存在", hsName)
	}
	if strings.TrimSpace(host.IKuaiAddr) == "" {
		return nil, host, nil
	}
	c := s.client(host.IKuaiAddr, host.IKuaiUser, host.IKuaiPass, host.IKuaiToken)
	if c == nil {
		return nil, host, fmt.Errorf("爱快路由器配置无效")
	}
	return c, host, nil
}

// ikuaiStaticDNS 返回静态绑定使用的 DNS，优先取主机 ipaddr_ddns 配置。
func ikuaiStaticDNS(host *data.HSConfigRow) (string, string) {
	d1, d2 := ikuaiDefaultDNS1, ikuaiDefaultDNS2
	if host == nil || strings.TrimSpace(host.IPAddrDDNS) == "" {
		return d1, d2
	}
	var dns []string
	if err := json.Unmarshal([]byte(host.IPAddrDDNS), &dns); err != nil {
		return d1, d2
	}
	if len(dns) > 0 && strings.TrimSpace(dns[0]) != "" {
		d1 = strings.TrimSpace(dns[0])
	}
	if len(dns) > 1 && strings.TrimSpace(dns[1]) != "" {
		d2 = strings.TrimSpace(dns[1])
	}
	return d1, d2
}

// BindStatic 为单个 IP/MAC 下发静态绑定。
// vmUUID 作为备注写入，便于在爱快上定位归属；dns1/dns2 为空时取主机配置。
func (s *NATService) BindStatic(hsName, vmUUID, ip, mac, dns1, dns2 string) error {
	ip = strings.TrimSpace(ip)
	mac = strings.TrimSpace(mac)
	if ip == "" || mac == "" {
		return fmt.Errorf("静态绑定参数不完整: ip=%q mac=%q", ip, mac)
	}

	c, host, err := s.ikuaiClientFor(hsName)
	if err != nil || c == nil {
		return err
	}
	defDNS1, defDNS2 := ikuaiStaticDNS(host)
	if strings.TrimSpace(dns1) == "" {
		dns1 = defDNS1
	}
	if strings.TrimSpace(dns2) == "" {
		dns2 = defDNS2
	}

	// DHCP 静态地址：客户端每次都能拿到固定 IP
	if err := c.addDHCPStatic(ip, mac, vmUUID, dns1, dns2); err != nil {
		return err
	}
	// ARP 绑定：防止地址被其他机器抢占
	if err := c.addARP(ip, mac, vmUUID); err != nil {
		return err
	}
	return nil
}

// UnbindStatic 解除单个 IP/MAC 的静态绑定（DHCP 静态地址 + ARP）。
// 记录不存在时视为已解绑，保证重复调用幂等。
func (s *NATService) UnbindStatic(hsName, ip, mac string) error {
	ip = strings.TrimSpace(ip)
	mac = strings.TrimSpace(mac)
	if ip == "" && mac == "" {
		return fmt.Errorf("解绑静态绑定需要 ip 或 mac")
	}

	c, _, err := s.ikuaiClientFor(hsName)
	if err != nil || c == nil {
		return err
	}

	var errs []string
	if err := c.deleteDHCPStatic(ip, mac); err != nil && !errors.Is(err, errIKuaiNotFound) {
		errs = append(errs, err.Error())
	}
	if err := c.deleteARP(ip, mac); err != nil && !errors.Is(err, errIKuaiNotFound) {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// StaticBindResult 描述一次静态绑定的实际执行情况。
//
// 必须区分 Bound 与 Skipped：网卡缺少 IP/MAC 时绑定无处可下，
// 若这种情况也返回「成功」，调用方会把「什么都没做」记成「绑定成功」，
// 让真正的配置缺失在日志里彻底消失。
type StaticBindResult struct {
	// Bound 实际下发成功的 IPv4 绑定数
	Bound int
	// BoundV6 实际下发成功的 IPv6 静态分配数
	BoundV6 int
	// Skipped 因缺少 IP 或 MAC 而跳过的网卡描述
	Skipped []string
	// NoNIC 表示虚拟机没有任何网卡配置
	NoNIC bool
	// NotConfigured 表示主机未配置爱快路由器。
	// 静态绑定属于可选能力（部分主机走平台自有通道），此时应静默跳过，
	// 不能按「失败」或「跳过」上报，否则每台虚拟机的创建日志都会带噪音。
	NotConfigured bool
}

// Total 返回实际下发的绑定条数（IPv4 + IPv6）
func (r StaticBindResult) Total() int { return r.Bound + r.BoundV6 }

// Summary 返回便于打日志的一句话摘要
func (r StaticBindResult) Summary() string {
	switch {
	case r.NotConfigured:
		return "主机未配置爱快路由器"
	case r.NoNIC:
		return "虚拟机没有网卡配置"
	case r.Total() == 0 && len(r.Skipped) == 0:
		return "没有需要绑定的网卡"
	default:
		return fmt.Sprintf("成功 IPv4 %d 条 / IPv6 %d 条，跳过 %d 张（%s）",
			r.Bound, r.BoundV6, len(r.Skipped), strings.Join(r.Skipped, ", "))
	}
}

// StaticBindConfigured 返回主机是否配置了爱快路由器。
// 供 HTTP 接口在「未配置」时明确报错，而不是回一个假的成功。
func (s *NATService) StaticBindConfigured(hsName string) bool {
	c, _, err := s.ikuaiClientFor(hsName)
	return err == nil && c != nil
}

// BindStaticForVM 为虚拟机所有已配置 IP/MAC 的网卡下发静态绑定。
// 单张网卡失败不影响其余网卡，返回的错误里带上失败明细。
func (s *NATService) BindStaticForVM(hsName, vmUUID string, vm *model.VMConfig) (StaticBindResult, error) {
	var res StaticBindResult
	if vm == nil || len(vm.NicAll) == 0 {
		res.NoNIC = true
		return res, nil
	}

	// 先判定主机是否配置了爱快：未配置属于可选能力缺失，静默跳过
	if c, _, err := s.ikuaiClientFor(hsName); err != nil {
		return res, err
	} else if c == nil {
		res.NotConfigured = true
		return res, nil
	}

	var errs []string
	for _, item := range sortedNics(vm.NicAll) {
		nic := item.Nic
		mac := strings.TrimSpace(nic.MacAddr)
		ip4 := strings.TrimSpace(nic.IP4Addr)
		ip6 := strings.TrimSpace(nic.IP6Addr)

		// 没有 IP 或没有 MAC 时无法定位终端，两张表都下发不了
		if mac == "" || (ip4 == "" && ip6 == "") {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s(ip4=%q ip6=%q mac=%q)",
				item.Name, nic.IP4Addr, nic.IP6Addr, nic.MacAddr))
			continue
		}

		if ip4 != "" {
			dns1, dns2 := firstDNS(nic.DNSAddr)
			if err := s.BindStatic(hsName, vmUUID, ip4, mac, dns1, dns2); err != nil {
				errs = append(errs, fmt.Sprintf("%s(%s): %v", ip4, mac, err))
			} else {
				res.Bound++
			}
		}

		// IPv6 走 DHCPv6 静态分配：MAC + 接口标识后缀，
		// 与 IPv4 的 DHCP 静态地址是两张表，必须分别下发。
		if ip6 != "" {
			if err := s.BindStaticV6(hsName, vmUUID, ip6, mac); err != nil {
				errs = append(errs, fmt.Sprintf("%s(%s): %v", ip6, mac, err))
			} else {
				res.BoundV6++
			}
		}
	}
	if len(errs) > 0 {
		return res, errors.New(strings.Join(errs, "; "))
	}
	return res, nil
}

// UnbindStaticForVM 解除虚拟机所有网卡 IP 的静态绑定（删除虚拟机时调用）
func (s *NATService) UnbindStaticForVM(hsName string, vm *model.VMConfig) error {
	if vm == nil || len(vm.NicAll) == 0 {
		return nil
	}
	var errs []string
	for _, item := range sortedNics(vm.NicAll) {
		nic := item.Nic
		ip4 := strings.TrimSpace(nic.IP4Addr)
		ip6 := strings.TrimSpace(nic.IP6Addr)

		if ip4 != "" {
			if err := s.UnbindStatic(hsName, ip4, nic.MacAddr); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", ip4, err))
			}
		}
		if ip6 != "" {
			if err := s.UnbindStaticV6(hsName, ip6, nic.MacAddr); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", ip6, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// BindStaticV6 为单个 IPv6/MAC 下发 DHCPv6 静态分配。
//
// 下发的是「MAC + 后缀」而不是完整地址：运营商前缀会变，
// 只有接口标识稳定。suffix 由 NCConfig.IPv6Suffix 从实际地址推导，
// 保证路由器绑定的后缀与网卡真实地址一致。
func (s *NATService) BindStaticV6(hsName, vmUUID, ip6, mac string) error {
	ip6 = strings.TrimSpace(ip6)
	mac = strings.TrimSpace(mac)
	if ip6 == "" || mac == "" {
		return fmt.Errorf("IPv6 静态绑定参数不完整: ip6=%q mac=%q", ip6, mac)
	}

	nic := model.NCConfig{IP6Addr: ip6, MacAddr: mac}
	suffix := nic.IPv6Suffix()
	if suffix == "" {
		return fmt.Errorf("无法从 IPv6 地址 %q 解析出接口标识后缀", ip6)
	}

	c, _, err := s.ikuaiClientFor(hsName)
	if err != nil || c == nil {
		return err
	}
	return c.addDHCPv6Static(mac, suffix, vmUUID)
}

// UnbindStaticV6 解除单个 IPv6/MAC 的静态分配；不存在时视为已解绑。
func (s *NATService) UnbindStaticV6(hsName, ip6, mac string) error {
	ip6 = strings.TrimSpace(ip6)
	mac = strings.TrimSpace(mac)
	if ip6 == "" && mac == "" {
		return fmt.Errorf("解绑 IPv6 静态分配需要 ip6 或 mac")
	}

	suffix := ""
	if ip6 != "" {
		nic := model.NCConfig{IP6Addr: ip6}
		suffix = nic.IPv6Suffix()
	}

	c, _, err := s.ikuaiClientFor(hsName)
	if err != nil || c == nil {
		return err
	}
	if err := c.deleteDHCPv6Static(mac, suffix); err != nil && !errors.Is(err, errIKuaiNotFound) {
		return err
	}
	return nil
}

// vmPrimaryIP 返回虚拟机第一块已分配地址的网卡 IP。
func (s *NATService) vmPrimaryIP(hsName, vmUUID string) string {
	raw, err := s.db.GetVMConfig(hsName, vmUUID)
	if err != nil || raw == "" {
		return ""
	}
	vm, err := DecodeVMConfig(raw)
	if err != nil || vm == nil {
		return ""
	}
	for _, item := range sortedNics(vm.NicAll) {
		if ip := strings.TrimSpace(item.Nic.IP4Addr); ip != "" {
			return ip
		}
	}
	return ""
}

// namedNic 网卡名与其配置（map 的键即网卡名，可能未写入 NCConfig.NicName）
type namedNic struct {
	Name string
	Nic  model.NCConfig
}

// sortedNics 按网卡名排序返回网卡配置：map 遍历顺序随机，
// 固定顺序可让下发日志与失败明细保持稳定、可对比。
func sortedNics(nics map[string]model.NCConfig) []namedNic {
	names := make([]string, 0, len(nics))
	for name := range nics {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]namedNic, 0, len(nics))
	for _, name := range names {
		out = append(out, namedNic{Name: name, Nic: nics[name]})
	}
	return out
}

// firstDNS 取网卡自身配置的 DNS，缺省返回空串（由主机配置兜底）
func firstDNS(list []string) (string, string) {
	d1, d2 := "", ""
	if len(list) > 0 {
		d1 = strings.TrimSpace(list[0])
	}
	if len(list) > 1 {
		d2 = strings.TrimSpace(list[1])
	}
	return d1, d2
}

// ============================================================
// 公网 IPv4 分配（爱快地址转换 / 1:1 NAT）
// ============================================================

// publicAddrs 解析主机 public_addr 配置（JSON 字符串数组）。
func publicAddrs(host *data.HSConfigRow) []string {
	if host == nil || strings.TrimSpace(host.PublicAddr) == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(host.PublicAddr), &list); err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// PublicIPPool 返回主机配置的可用公网地址列表。
func (s *NATService) PublicIPPool(hsName string) ([]string, error) {
	host, err := s.db.GetHost(hsName)
	if err != nil {
		return nil, fmt.Errorf("读取主机配置失败: %w", err)
	}
	if host == nil {
		return nil, fmt.Errorf("主机 %s 不存在", hsName)
	}
	return publicAddrs(host), nil
}

// ListPublicIPs 返回主机下全部公网地址绑定关系。
func (s *NATService) ListPublicIPs(hsName string) ([]model.PublicIPBinding, error) {
	rules, err := s.db.ListPublicIPRules(hsName)
	if err != nil {
		return nil, fmt.Errorf("读取公网地址记录失败: %w", err)
	}
	return toPublicIPBindings(rules), nil
}

// ListVMPublicIPs 返回指定虚拟机的公网地址绑定关系。
func (s *NATService) ListVMPublicIPs(hsName, vmUUID string) ([]model.PublicIPBinding, error) {
	rules, err := s.db.ListVMPublicIPRules(hsName, vmUUID)
	if err != nil {
		return nil, fmt.Errorf("读取公网地址记录失败: %w", err)
	}
	return toPublicIPBindings(rules), nil
}

func toPublicIPBindings(rules []data.PublicIPRule) []model.PublicIPBinding {
	out := make([]model.PublicIPBinding, 0, len(rules))
	for _, r := range rules {
		out = append(out, model.PublicIPBinding{
			VMUUID:     r.VMUUID,
			PublicAddr: r.PublicAddr,
			LanAddr:    r.LanAddr,
			MacAddr:    r.MacAddr,
			Comment:    r.Comment,
			CreatedAt:  r.CreatedAt,
		})
	}
	return out
}

// AllocatePublicIP 为虚拟机分配一个公网 IPv4，并在爱快上下发地址转换规则。
//
// 参数：
//   - publicAddr 为空时从主机 public_addr 池中挑第一个未占用的地址；
//   - lanAddr 为空时取虚拟机第一块已分配 IPv4 的网卡。
//
// 虚拟机的网卡地址仍然保持内网 IP：公网地址由爱快做 1:1 转换，
// 直接把公网地址配到网卡上会和路由器的 WAN 网段冲突。
func (s *NATService) AllocatePublicIP(hsName, vmUUID, publicAddr, lanAddr, comment string) (*model.PublicIPBinding, error) {
	host, err := s.db.GetHost(hsName)
	if err != nil {
		return nil, fmt.Errorf("读取主机配置失败: %w", err)
	}
	if host == nil {
		return nil, fmt.Errorf("主机 %s 不存在", hsName)
	}
	if strings.TrimSpace(host.IKuaiAddr) == "" {
		return nil, fmt.Errorf("主机 %s 未配置爱快路由器：公网地址分配依赖爱快的地址转换功能", hsName)
	}
	if _, ok := s.provider(hsName); ok {
		return nil, fmt.Errorf("主机 %s 使用平台自有网络通道，不支持爱快地址转换", hsName)
	}

	pool := publicAddrs(host)
	if len(pool) == 0 {
		return nil, fmt.Errorf("主机 %s 未配置公网地址池（public_addr）", hsName)
	}

	lanAddr = strings.TrimSpace(lanAddr)
	if lanAddr == "" {
		lanAddr = s.vmPrimaryIP(hsName, vmUUID)
	}
	if lanAddr == "" {
		return nil, fmt.Errorf("缺少内网地址：请先为虚拟机网卡分配 IPv4 地址")
	}
	if ip := net.ParseIP(lanAddr); ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("内网地址 %q 不是合法的 IPv4 地址", lanAddr)
	}

	existing, err := s.db.ListPublicIPRules(hsName)
	if err != nil {
		return nil, fmt.Errorf("读取公网地址占用失败: %w", err)
	}
	// used: 公网地址 → 归属虚拟机；lanOwner: 内网地址 → 已绑定的公网地址
	used := make(map[string]string, len(existing))
	lanOwner := make(map[string]string, len(existing))
	for _, r := range existing {
		used[r.PublicAddr] = r.VMUUID
		lanOwner[r.LanAddr] = r.PublicAddr
	}

	publicAddr = strings.TrimSpace(publicAddr)
	if publicAddr == "" {
		for _, addr := range pool {
			if _, taken := used[addr]; !taken {
				publicAddr = addr
				break
			}
		}
		if publicAddr == "" {
			return nil, fmt.Errorf("主机 %s 的公网地址池已全部占用", hsName)
		}
	} else {
		if !containsString(pool, publicAddr) {
			return nil, fmt.Errorf("公网地址 %s 不在主机 %s 的地址池中", publicAddr, hsName)
		}
		if owner, taken := used[publicAddr]; taken && owner != vmUUID {
			return nil, fmt.Errorf("公网地址 %s 已被虚拟机 %s 占用", publicAddr, owner)
		}
	}

	// 1:1 转换要求内网地址唯一：同一个内网 IP 只能对应一个公网 IP。
	// 同一对「公网 ↔ 内网」重复提交属于幂等更新，允许通过。
	if bound, taken := lanOwner[lanAddr]; taken && bound != publicAddr {
		return nil, fmt.Errorf("内网地址 %s 已绑定公网地址 %s，1:1 转换不允许一个内网地址对应多个公网地址", lanAddr, bound)
	}

	// 已经完全相同的绑定直接返回，避免在路由器上重复插入同名规则
	for _, r := range existing {
		if r.PublicAddr == publicAddr && r.LanAddr == lanAddr && r.VMUUID == vmUUID {
			return &model.PublicIPBinding{
				VMUUID:     r.VMUUID,
				PublicAddr: r.PublicAddr,
				LanAddr:    r.LanAddr,
				MacAddr:    r.MacAddr,
				Comment:    r.Comment,
				CreatedAt:  r.CreatedAt,
			}, nil
		}
	}

	mac := s.vmNicMACByIP(hsName, vmUUID, lanAddr)
	if strings.TrimSpace(comment) == "" {
		comment = vmUUID
	}

	c := s.client(host.IKuaiAddr, host.IKuaiUser, host.IKuaiPass, host.IKuaiToken)
	if c == nil {
		return nil, fmt.Errorf("爱快路由器配置无效")
	}
	if err := c.addAddrTrans(publicAddr, lanAddr, comment); err != nil {
		return nil, err
	}

	if err := s.db.UpsertPublicIPRule(hsName, vmUUID, publicAddr, lanAddr, mac, comment); err != nil {
		// 落库失败必须把刚下发的规则撤掉，否则路由器上会留下本地无从追踪的孤儿规则
		_ = c.deleteAddrTrans(publicAddr, lanAddr)
		return nil, fmt.Errorf("保存公网地址记录失败: %w", err)
	}

	binding := &model.PublicIPBinding{
		VMUUID:     vmUUID,
		PublicAddr: publicAddr,
		LanAddr:    lanAddr,
		MacAddr:    mac,
		Comment:    comment,
	}
	if rules, err := s.db.ListVMPublicIPRules(hsName, vmUUID); err == nil {
		for _, r := range rules {
			if r.PublicAddr == publicAddr {
				binding.CreatedAt = r.CreatedAt
				break
			}
		}
	}
	return binding, nil
}

// ReleasePublicIP 释放公网地址：先撤销爱快地址转换，再删除本地记录。
//
// publicAddr 为空时删除该虚拟机的全部公网地址。
// 路由器上找不到规则视为已释放（幂等），不会阻断本地清理。
func (s *NATService) ReleasePublicIP(hsName, vmUUID, publicAddr string) error {
	publicAddr = strings.TrimSpace(publicAddr)

	var targets []data.PublicIPRule
	rules, err := s.db.ListPublicIPRules(hsName)
	if err != nil {
		return fmt.Errorf("读取公网地址记录失败: %w", err)
	}
	for _, r := range rules {
		if publicAddr != "" && r.PublicAddr != publicAddr {
			continue
		}
		if vmUUID != "" && r.VMUUID != vmUUID {
			continue
		}
		targets = append(targets, r)
	}

	var c *ikuaiClient
	if host, err := s.db.GetHost(hsName); err == nil && host != nil && strings.TrimSpace(host.IKuaiAddr) != "" {
		c = s.client(host.IKuaiAddr, host.IKuaiUser, host.IKuaiPass, host.IKuaiToken)
	}

	var errs []string
	for _, r := range targets {
		if c != nil {
			if delErr := c.deleteAddrTrans(r.PublicAddr, r.LanAddr); delErr != nil && !errors.Is(delErr, errIKuaiNotFound) {
				errs = append(errs, fmt.Sprintf("%s: %v", r.PublicAddr, delErr))
				// 下发失败时不删本地记录，保留下次重试的依据
				continue
			}
		}
		if delErr := s.db.DeletePublicIPRule(hsName, r.PublicAddr); delErr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.PublicAddr, delErr))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// ReleasePublicIPsForVM 释放虚拟机全部公网地址（删除虚拟机时调用）。
func (s *NATService) ReleasePublicIPsForVM(hsName, vmUUID string) error {
	rules, err := s.db.ListVMPublicIPRules(hsName, vmUUID)
	if err != nil {
		return fmt.Errorf("读取公网地址记录失败: %w", err)
	}
	if len(rules) == 0 {
		return nil
	}
	return s.ReleasePublicIP(hsName, vmUUID, "")
}

// vmNicMACByIP 返回虚拟机上使用指定 IPv4 的网卡 MAC（找不到时为空）。
func (s *NATService) vmNicMACByIP(hsName, vmUUID, ip string) string {
	raw, err := s.db.GetVMConfig(hsName, vmUUID)
	if err != nil || raw == "" {
		return ""
	}
	vm, err := DecodeVMConfig(raw)
	if err != nil || vm == nil {
		return ""
	}
	for _, item := range sortedNics(vm.NicAll) {
		if strings.TrimSpace(item.Nic.IP4Addr) == ip {
			return strings.TrimSpace(item.Nic.MacAddr)
		}
	}
	return ""
}

// containsString 判断字符串切片是否包含目标值（忽略首尾空白）
func containsString(list []string, want string) bool {
	for _, item := range list {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}
