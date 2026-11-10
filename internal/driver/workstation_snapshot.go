package driver

import (
	"fmt"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// SupportsSnapshot VMware Workstation 提供原生快照（vmrun）
func (d *WorkstationDriver) SupportsSnapshot() bool { return true }

// runVmrun 执行 vmrun 命令（-T ws）
func (d *WorkstationDriver) runVmrun(args ...string) (string, error) {
	full := append([]string{d.vmrunBin(), "-T", "ws"}, args...)
	out, _, err := security.SafeExecWithTimeout(full, 2*time.Minute)
	return out, err
}

// ListSnapshots 读取 Workstation 快照列表。
//
// vmrun listSnapshots 只输出名称（首行是 Total snapshots: N），
// 没有时间与描述，这两个字段留空由前端显示为 "-"。
func (d *WorkstationDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	out, err := d.runVmrun("listSnapshots", d.vmxPath(vmUUID))
	if err != nil {
		return nil, fmt.Errorf("读取快照列表失败: %v (%s)", err, strings.TrimSpace(out))
	}

	snapshots := make([]SnapshotInfo, 0)
	for _, line := range splitNonEmptyLines(out) {
		if strings.HasPrefix(strings.ToLower(line), "total snapshots") {
			continue
		}
		if IsBackupSnapshot(line) {
			continue
		}
		snapshots = append(snapshots, SnapshotInfo{Name: line})
	}
	return snapshots, nil
}

// CreateSnapshot 创建 Workstation 快照
func (d *WorkstationDriver) CreateSnapshot(vmUUID, name, description string) error {
	if out, err := d.runVmrun("snapshot", d.vmxPath(vmUUID), name); err != nil {
		return fmt.Errorf("创建快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// RestoreSnapshot 恢复到 Workstation 快照
func (d *WorkstationDriver) RestoreSnapshot(vmUUID, name string) error {
	if out, err := d.runVmrun("revertToSnapshot", d.vmxPath(vmUUID), name); err != nil {
		return fmt.Errorf("恢复快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// DeleteSnapshot 删除 Workstation 快照
func (d *WorkstationDriver) DeleteSnapshot(vmUUID, name string) error {
	if out, err := d.runVmrun("deleteSnapshot", d.vmxPath(vmUUID), name); err != nil {
		return fmt.Errorf("删除快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}
