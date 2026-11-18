package model

import (
	"fmt"
	"strings"
)

// NetUsage 网卡与地址的使用量。
//
// 地址按「网卡类型 + 地址族」分别统计，与套餐里的四个上限一一对应：
// 公网V4(ip4_pub) / 内网V4(ip4_pri) / 公网V6(ip6_pub) / 内网V6(ip6_pri)。
type NetUsage struct {
	Total int // 网卡总数
	PubV4 int // 公网 IPv4 数量
	PubV6 int // 公网 IPv6 数量
	PriV4 int // 内网 IPv4 数量
	PriV6 int // 内网 IPv6 数量
}

// NetUsage 统计当前 nic_all 的网卡数与各类型地址数。
func (c *VMConfig) NetUsage() NetUsage {
	usage := NetUsage{}
	if c == nil {
		return usage
	}
	for _, nic := range c.NicAll {
		usage.Total++
		isPub := nic.EffectiveNicType() == "pub"
		hasV4 := strings.TrimSpace(nic.IP4Addr) != ""
		hasV6 := strings.TrimSpace(nic.IP6Addr) != ""
		switch {
		case isPub:
			if hasV4 {
				usage.PubV4++
			}
			if hasV6 {
				usage.PubV6++
			}
		default:
			if hasV4 {
				usage.PriV4++
			}
			if hasV6 {
				usage.PriV6++
			}
		}
	}
	return usage
}

// NetLimitV4 / NetLimitV6 返回指定网卡类型（pub / 其它视为内网）的地址上限。
// 返回 nil 表示未配置上限，不限制。
func (c *VMConfig) NetLimitV4(nicType string) *int {
	if c == nil {
		return nil
	}
	if nicType == "pub" {
		return c.Ip4Pub
	}
	return c.Ip4Pri
}

func (c *VMConfig) NetLimitV6(nicType string) *int {
	if c == nil {
		return nil
	}
	if nicType == "pub" {
		return c.Ip6Pub
	}
	return c.Ip6Pri
}

// CheckNetQuota 校验当前网卡配置没有超出套餐的网络配额。
//
// 两条约束：
//  1. 网卡总数不能超过最大网卡数量（nic_num）；
//  2. 公网/内网各自的 IPv4、IPv6 地址总数不能超过对应上限。
//
// 未配置上限（nil）时不限制，历史数据因此不受影响。
func (c *VMConfig) CheckNetQuota() error {
	if c == nil {
		return nil
	}
	usage := c.NetUsage()
	if c.NicNum != nil && usage.Total > *c.NicNum {
		return fmt.Errorf("网卡数量 %d 超过上限 %d", usage.Total, *c.NicNum)
	}
	checks := []struct {
		name  string
		used  int
		limit *int
	}{
		{"公网 IPv4", usage.PubV4, c.Ip4Pub},
		{"内网 IPv4", usage.PriV4, c.Ip4Pri},
		{"公网 IPv6", usage.PubV6, c.Ip6Pub},
		{"内网 IPv6", usage.PriV6, c.Ip6Pri},
	}
	for _, item := range checks {
		if item.limit != nil && item.used > *item.limit {
			return fmt.Errorf("%s 地址数量 %d 超过上限 %d", item.name, item.used, *item.limit)
		}
	}
	return nil
}

// CopyNetQuotaFrom 把网络配额从套餐复制到虚拟机配置。
// 指针在新旧配置间不共享，避免后续修改套餐时影响已创建的虚拟机。
func (c *VMConfig) CopyNetQuotaFrom(src *VMConfig) {
	if c == nil || src == nil {
		return
	}
	c.NicNum = copyIntPtr(src.NicNum)
	c.Ip4Pub = copyIntPtr(src.Ip4Pub)
	c.Ip4Pri = copyIntPtr(src.Ip4Pri)
	c.Ip6Pub = copyIntPtr(src.Ip6Pub)
	c.Ip6Pri = copyIntPtr(src.Ip6Pri)
}

func copyIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
