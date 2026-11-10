package driver

import (
	"fmt"
	"strings"
	"time"
)

// SupportsSnapshot VirtualBox 提供原生快照
func (d *VirtualBoxDriver) SupportsSnapshot() bool { return true }

// ListSnapshots 读取 VirtualBox 快照列表。
//
// `VBoxManage snapshot <uuid> list --machinereadable` 输出平铺的 key=value，
// 每条快照以 SnapshotName 开始；CurrentSnapshotName 单独标记当前快照。
func (d *VirtualBoxDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	out, err := d.execVBox("snapshot", vmUUID, "list", "--machinereadable")
	if err != nil {
		// 没有快照时 VBoxManage 可能返回非零退出码
		if strings.Contains(strings.ToLower(out), "does not have any snapshots") {
			return []SnapshotInfo{}, nil
		}
		return nil, fmt.Errorf("读取快照列表失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return parseVBoxSnapshotList(out), nil
}

// parseVBoxSnapshotList 解析 VBoxManage 的 machinereadable 快照输出
func parseVBoxSnapshotList(output string) []SnapshotInfo {
	snapshots := make([]SnapshotInfo, 0)
	current := ""
	var pending *SnapshotInfo

	flush := func() {
		if pending != nil {
			pending.Current = pending.Name != "" && pending.Name == current
			if !IsBackupSnapshot(pending.Name) && pending.Name != "" {
				snapshots = append(snapshots, *pending)
			}
			pending = nil
		}
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := splitVBoxKeyValue(line)
		if !ok {
			continue
		}
		switch key {
		case "SnapshotName":
			flush()
			pending = &SnapshotInfo{Name: value}
		case "SnapshotDescription":
			if pending != nil {
				pending.Description = value
			}
		case "SnapshotTime":
			if pending != nil {
				pending.CreatedAt = parseVBoxTime(value)
			}
		case "CurrentSnapshotName":
			current = value
			if pending != nil {
				pending.Current = pending.Name == current
			}
		}
	}
	flush()
	return snapshots
}

// splitVBoxKeyValue 拆分 key="value"，并处理引号内转义
func splitVBoxKeyValue(line string) (key, value string, ok bool) {
	idx := strings.Index(line, "=")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	value = strings.Trim(value, `"`)
	value = strings.ReplaceAll(value, `\"`, `"`)
	value = strings.ReplaceAll(value, `\\`, `\`)
	return key, value, true
}

// parseVBoxTime 解析 VBox 的 SnapshotTime（无时区的本地时间）
func parseVBoxTime(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	layouts := []string{
		"2006-01-02T15:04:05.000000000",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t.Unix()
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.Unix()
	}
	return 0
}

// CreateSnapshot 创建 VirtualBox 快照
func (d *VirtualBoxDriver) CreateSnapshot(vmUUID, name, description string) error {
	args := []string{"snapshot", vmUUID, "take", name}
	if description = strings.TrimSpace(description); description != "" {
		args = append(args, "--description", description)
	}
	if out, err := d.execVBox(args...); err != nil {
		return fmt.Errorf("创建快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// RestoreSnapshot 恢复 VirtualBox 快照
func (d *VirtualBoxDriver) RestoreSnapshot(vmUUID, name string) error {
	if out, err := d.execVBox("snapshot", vmUUID, "restore", name); err != nil {
		return fmt.Errorf("恢复快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// DeleteSnapshot 删除 VirtualBox 快照
func (d *VirtualBoxDriver) DeleteSnapshot(vmUUID, name string) error {
	if out, err := d.execVBox("snapshot", vmUUID, "delete", name); err != nil {
		return fmt.Errorf("删除快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}
