package driver

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// lxdSnapshot LXD REST 快照条目
type lxdSnapshot struct {
	Name         string `json:"name"`
	CreationDate string `json:"creation_date"`
	Stateful     bool   `json:"stateful"`
}

// lxdInstanceSummary `lxc list --format json` 的实例条目（仅取快照字段）
type lxdInstanceSummary struct {
	Snapshots []lxdSnapshot `json:"snapshots"`
}

// SupportsSnapshot LXD 容器提供原生快照
func (d *LXDDriver) SupportsSnapshot() bool { return true }

// ListSnapshots 读取 LXD 快照列表。
//
// 优先走 `lxc query /1.0/instances/{name}/snapshots`（信息完整），
// 旧版本不支持 query 时回退到 `lxc list <name> --format json`。
func (d *LXDDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	out, err := d.execLXC("query", "/1.0/instances/"+vmUUID+"/snapshots")
	if err != nil {
		if fallback, fallbackErr := d.execLXC("list", vmUUID, "--format", "json"); fallbackErr == nil {
			return parseLXDSnapshotListFallback(fallback), nil
		}
		return nil, fmt.Errorf("读取快照列表失败: %v (%s)", err, strings.TrimSpace(out))
	}

	body := strings.TrimSpace(out)
	var raw []lxdSnapshot
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		// LXD 的 /snapshots 集合端点既可能返回对象数组，也可能只返回
		// 快照 URL 数组（如 "/1.0/instances/xx/snapshots/s1"）。后者按
		// 字符串数组解析并逐条查询详情，否则会直接报 JSON 类型错误。
		var refs []string
		if refErr := json.Unmarshal([]byte(body), &refs); refErr != nil {
			return nil, fmt.Errorf("解析快照列表失败: %v", err)
		}
		for _, ref := range refs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			item := lxdSnapshot{Name: path.Base(ref)}
			if detail, detailErr := d.execLXC("query", ref); detailErr == nil {
				var parsed lxdSnapshot
				if json.Unmarshal([]byte(strings.TrimSpace(detail)), &parsed) == nil && parsed.Name != "" {
					item = parsed
				}
			}
			raw = append(raw, item)
		}
	}

	snapshots := make([]SnapshotInfo, 0, len(raw))
	for _, item := range raw {
		if item.Name == "" || IsBackupSnapshot(item.Name) {
			continue
		}
		snapshots = append(snapshots, SnapshotInfo{
			Name:      item.Name,
			CreatedAt: parseRFC3339Unix(item.CreationDate),
			State:     lxdSnapshotState(item.Stateful),
		})
	}
	return snapshots, nil
}

// parseLXDSnapshotListFallback 解析 lxc list 的 snapshots 字段
func parseLXDSnapshotListFallback(output string) []SnapshotInfo {
	var instances []lxdInstanceSummary
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &instances); err != nil {
		return []SnapshotInfo{}
	}
	snapshots := make([]SnapshotInfo, 0)
	for _, instance := range instances {
		for _, item := range instance.Snapshots {
			if item.Name == "" || IsBackupSnapshot(item.Name) {
				continue
			}
			snapshots = append(snapshots, SnapshotInfo{
				Name:      item.Name,
				CreatedAt: parseRFC3339Unix(item.CreationDate),
				State:     lxdSnapshotState(item.Stateful),
			})
		}
	}
	return snapshots
}

// lxdSnapshotState 状态化快照会包含运行内存
func lxdSnapshotState(stateful bool) string {
	if stateful {
		return "含内存状态"
	}
	return "仅磁盘"
}

// CreateSnapshot 创建 LXD 快照
func (d *LXDDriver) CreateSnapshot(vmUUID, name, description string) error {
	if out, err := d.execLXC("snapshot", vmUUID, name); err != nil {
		return fmt.Errorf("创建快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// RestoreSnapshot 恢复 LXD 快照
func (d *LXDDriver) RestoreSnapshot(vmUUID, name string) error {
	if out, err := d.execLXC("restore", vmUUID, name); err != nil {
		return fmt.Errorf("恢复快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// DeleteSnapshot 删除 LXD 快照
func (d *LXDDriver) DeleteSnapshot(vmUUID, name string) error {
	if out, err := d.execLXC("delete", vmUUID+"/"+name); err != nil {
		return fmt.Errorf("删除快照失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// parseRFC3339Unix 解析 RFC3339 时间戳
func parseRFC3339Unix(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.Unix()
	}
	return 0
}
