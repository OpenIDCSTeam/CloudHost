package driver

import (
	"fmt"
	"strings"
	"time"
)

// SupportsSnapshot QEMU/KVM（libvirt）提供原生快照
func (d *QEMUDriver) SupportsSnapshot() bool { return true }

// ListSnapshots 读取 libvirt 快照列表。
//
// `virsh snapshot-list --name` 只输出名称、不受宿主语言环境影响；
// 时间 / 父快照 / 当前标记再逐个用 snapshot-info 补全，
// 解析不到时留空，不影响列表可用性。
func (d *QEMUDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	out, err := d.execVirsh("snapshot-list", vmUUID, "--name")
	if err != nil {
		return nil, fmt.Errorf("读取快照列表失败: %v (%s)", err, strings.TrimSpace(out))
	}

	snapshots := make([]SnapshotInfo, 0)
	for _, name := range splitNonEmptyLines(out) {
		if IsBackupSnapshot(name) {
			continue
		}
		info := SnapshotInfo{Name: name}
		if detail, infoErr := d.execVirsh("snapshot-info", vmUUID, name); infoErr == nil {
			fields := parseLibvirtColonFields(detail)
			info.Parent = cleanLibvirtValue(fields["Parent"])
			info.State = strings.TrimSpace(fields["State"])
			info.Current = strings.EqualFold(strings.TrimSpace(fields["Current"]), "yes")
			info.CreatedAt = parseLibvirtTime(fields["Creation Time"])
			if desc, ok := fields["Description"]; ok {
				info.Description = cleanLibvirtValue(desc)
			}
		}
		snapshots = append(snapshots, info)
	}
	return snapshots, nil
}

// CreateSnapshot 创建 libvirt 快照。
//
// 不使用备份链路的 --disk-only：外部快照只冻结磁盘，回滚后内存状态无法恢复。
// 普通快照由 libvirt 按磁盘格式决定内部/外部实现，失败时把平台错误原样返回。
func (d *QEMUDriver) CreateSnapshot(vmUUID, name, description string) error {
	args := []string{"snapshot-create-as", vmUUID, name}
	if description = strings.TrimSpace(description); description != "" {
		args = append(args, description)
	}
	args = append(args, "--atomic")
	if out, err := d.execVirsh(args...); err != nil {
		return fmt.Errorf("创建快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// RestoreSnapshot 回滚到 libvirt 快照
func (d *QEMUDriver) RestoreSnapshot(vmUUID, name string) error {
	if out, err := d.execVirsh("snapshot-revert", vmUUID, name); err != nil {
		return fmt.Errorf("回滚快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// DeleteSnapshot 删除 libvirt 快照
func (d *QEMUDriver) DeleteSnapshot(vmUUID, name string) error {
	if out, err := d.execVirsh("snapshot-delete", vmUUID, name); err != nil {
		return fmt.Errorf("删除快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// ============================================================
// libvirt 输出解析
// ============================================================

// parseLibvirtColonFields 解析 `virsh snapshot-info` 的 "Key: value" 输出。
//
// libvirt 输出随宿主语言变化，这里只按英文键取值，取不到就留空，
// 不抛错误——列表能显示名称与快照操作仍然可用。
func parseLibvirtColonFields(output string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if key != "" {
			fields[key] = value
		}
	}
	return fields
}

// cleanLibvirtValue 把 libvirt 的 (null) 占位转换为空字符串
func cleanLibvirtValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "(null)") || value == "-" {
		return ""
	}
	return value
}

// parseLibvirtTime 解析 snapshot-info 的 Creation Time
func parseLibvirtTime(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	layouts := []string{
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// splitNonEmptyLines 按行拆分并去掉空行
func splitNonEmptyLines(output string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
