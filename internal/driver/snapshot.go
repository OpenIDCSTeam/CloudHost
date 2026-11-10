package driver

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrSnapshotUnsupported 平台不具备虚拟机快照能力。
//
// 与防火墙一样是可选能力：BaseDriver 提供默认实现（false / 直接报错），
// 只有真正能调用平台快照 API 的驱动覆盖这些方法并返回 true。
// 前端通过 capabilities.snapshot 决定是否展示「快照」页签。
//
// 注意：备份（CreateBackup）与快照是两套语义。
// 部分平台的备份本身就是用快照实现的（ESXi / Hyper-V / LXD / QEMU），
// 因此这些平台由备份创建的快照也会出现在平台快照列表里；
// 快照列表统一过滤 backup- / backup_ 前缀，避免用户误删备份链路依赖的快照。
var ErrSnapshotUnsupported = errors.New("当前虚拟化平台不支持虚拟机快照")

// SnapshotInfo 平台无关的虚拟机快照信息。
type SnapshotInfo struct {
	// Name 快照名称（平台内唯一）
	Name string `json:"name"`
	// Description 快照描述，平台不支持时为空
	Description string `json:"description"`
	// Parent 父快照名称，顶层快照为空
	Parent string `json:"parent"`
	// CreatedAt 创建时间（Unix 秒），平台无法提供时为 0
	CreatedAt int64 `json:"created_at"`
	// State 平台附加快照状态（如 libvirt 的 running / shutoff、VMware 的内存状态）
	State string `json:"state"`
	// Current 是否为当前快照
	Current bool `json:"current"`
}

// SnapshotDriver 可选接口：平台提供虚拟机快照。
//
// 调用方必须先判断 SupportsSnapshot，再调用其余方法。
type SnapshotDriver interface {
	// SupportsSnapshot 平台是否支持虚拟机快照
	SupportsSnapshot() bool
	// ListSnapshots 列出快照（不含备份功能创建的快照）
	ListSnapshots(vmUUID string) ([]SnapshotInfo, error)
	// CreateSnapshot 创建快照
	CreateSnapshot(vmUUID, name, description string) error
	// RestoreSnapshot 回滚到指定快照
	RestoreSnapshot(vmUUID, name string) error
	// DeleteSnapshot 删除指定快照
	DeleteSnapshot(vmUUID, name string) error
}

// SupportsSnapshotPlatform 静态判断某种主机类型是否提供虚拟机快照。
//
// 这是「平台是否支持快照」的唯一事实来源，能力下发与前端页签可见性都据此判断。
// 不包含的平台及原因：
//   - Docker：docker commit 产出镜像，没有可回滚的运行态快照
//   - MEmu：Android 模拟器无快照概念（备份走 OVA 导出）
//   - SmolVM：Firecracker 快照 API 要求暂停且与 cloud-hypervisor 模式不通用，暂不纳入
func SupportsSnapshotPlatform(serverType string) bool {
	switch serverType {
	case "PromoxSetup", // /nodes/{node}/qemu/{vmid}/snapshot
		"vSphereESXi",   // CreateSnapshot / RevertToSnapshot / RemoveSnapshot
		"HyperVSetup",   // Checkpoint-VM / Restore-VMSnapshot
		"QEMUServer",    // virsh snapshot-*
		"LxContainer",   // lxc snapshot / restore / delete
		"VirtualBoxs",   // VBoxManage snapshot
		"VMWareSetup":   // vmrun snapshot
		return true
	default:
		return false
	}
}

// snapshotNamePattern 快照名称规则。
//
// 取各平台限制的交集：LXD 不允许下划线，PVE 要求首字符为字母，
// 因此统一限制为「字母或数字开头 + 字母/数字/连字符」，长度不超过 40。
// 中文名称在 PVE / LXD 上会被拒绝，这里直接拦下并给出明确提示。
var snapshotNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,39}$`)

// SnapshotDefaultName 生成全平台安全的默认快照名
func SnapshotDefaultName() string {
	return "snap-" + time.Now().Format("20060102-150405")
}

// NormalizeSnapshotName 校验并归一化快照名称。
func NormalizeSnapshotName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = SnapshotDefaultName()
	}
	if !snapshotNamePattern.MatchString(name) {
		return "", fmt.Errorf("快照名称只能包含字母、数字与连字符，且以字母或数字开头，最长 40 个字符")
	}
	return name, nil
}

// IsBackupSnapshot 判断快照是否由备份功能创建。
//
// ESXi 用 backup_<unix>，Hyper-V / LXD / QEMU 用 backup-<time>。
func IsBackupSnapshot(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(lower, "backup-") || strings.HasPrefix(lower, "backup_")
}

// ============================================================
// 基类默认实现：不具备快照能力的平台统一走这里
// ============================================================

// SupportsSnapshot 平台默认不具备虚拟机快照能力
func (b *BaseDriver) SupportsSnapshot() bool { return false }

// ListSnapshots 平台不支持时返回错误
func (b *BaseDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	return nil, ErrSnapshotUnsupported
}

// CreateSnapshot 平台不支持时返回错误
func (b *BaseDriver) CreateSnapshot(vmUUID, name, description string) error {
	return ErrSnapshotUnsupported
}

// RestoreSnapshot 平台不支持时返回错误
func (b *BaseDriver) RestoreSnapshot(vmUUID, name string) error {
	return ErrSnapshotUnsupported
}

// DeleteSnapshot 平台不支持时返回错误
func (b *BaseDriver) DeleteSnapshot(vmUUID, name string) error {
	return ErrSnapshotUnsupported
}
