// Package driver MEmu 运行环境自检与修复。
//
// 逍遥模拟器在 Hyper-V 模式下有两类环境依赖，缺失时表现为
// 「创建成功但一启动就退出」，memuhv.log 里分别是 H0 / H8：
//
//	H0  Windows 防火墙服务（MpsSvc）未运行
//	    → HNS 无法为新网络添加防火墙规则（0x800706D9）→ HCN 建网失败
//	H8  Hyper-V 虚拟机账户（NT VIRTUAL MACHINE\Virtual Machines，
//	    S-1-5-83-0）没有基础镜像 / 实例磁盘的访问权限（0x80070005）
//
// 这里把原 Fix-MEmuHyperV.ps1 的逻辑内置到 HostAgent：
// 主机启用时可由运维页面一键执行，创建/启动虚拟机时也会自动修复并重试。
package driver

import (
	"fmt"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// MemuRepairItem 单个磁盘目录的 ACL 检查结果
type MemuRepairItem struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Rights string `json:"rights"`
	Status string `json:"status"` // ok / fixed / warn / fail
	Detail string `json:"detail,omitempty"`
}

// MemuRepairReport 修复报告
type MemuRepairReport struct {
	Root     string           `json:"root"`
	Firewall string           `json:"firewall"`
	Items    []MemuRepairItem `json:"items"`
	Fixed    int              `json:"fixed"`
	Warnings []string         `json:"warnings,omitempty"`
}

// PlatformRepairer 平台运行环境自检/修复能力。
//
// 供 API 层识别：实现了该接口的驱动才展示「修复运行环境」入口。
type PlatformRepairer interface {
	RepairPlatform() (*model.ZMessage, error)
}

// RepairPlatform 执行一次完整自检并修复（防火墙服务 + 磁盘 ACL）
func (d *MemuDriver) RepairPlatform() (*model.ZMessage, error) {
	report := d.repairRuntime(true)
	if report == nil {
		return &model.ZMessage{Success: false, Action: "Repair",
			Message: "当前平台不支持自动修复"}, nil
	}

	msg := "运行环境检查完成，未发现需要修复的项目"
	if report.Fixed > 0 {
		msg = fmt.Sprintf("运行环境已修复 %d 项", report.Fixed)
	}
	if len(report.Warnings) > 0 {
		msg += fmt.Sprintf("，%d 项需要人工处理", len(report.Warnings))
	}
	return &model.ZMessage{
		Success: true,
		Action:  "Repair",
		Message: msg,
		Results: map[string]interface{}{
			"firewall": report.Firewall,
			"items":    report.Items,
			"fixed":    report.Fixed,
			"warnings": report.Warnings,
			"root":     report.Root,
		},
	}, nil
}
