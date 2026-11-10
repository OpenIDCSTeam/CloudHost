package driver

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hypervSnapshotItem Get-VMSnapshot 的 JSON 输出结构
type hypervSnapshotItem struct {
	Name               string      `json:"Name"`
	Description        string      `json:"Description"`
	ParentSnapshotName interface{} `json:"ParentSnapshotName"`
	SnapshotType       string      `json:"SnapshotType"`
	CreatedAt          interface{} `json:"CreatedAt"`
}

// SupportsSnapshot Hyper-V 检查点即快照
func (d *HyperVDriver) SupportsSnapshot() bool { return true }

// ListSnapshots 读取 Hyper-V 检查点列表。
//
// vmUUID 是 Hyper-V 的 VM GUID，Get-VM 系列 cmdlet 没有 -VMId 参数，
// 统一用 `Get-VM -Id <guid> | ...` 走对象管道。
func (d *HyperVDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	script := fmt.Sprintf(
		"$vm = Get-VM -Id '%s' -ErrorAction Stop; "+
			"@($vm | Get-VMSnapshot | Select-Object Name,Description,ParentSnapshotName,SnapshotType,"+
			"@{n='CreatedAt';e={[int64]([DateTimeOffset]$_.CreationTime).ToUnixTimeSeconds()}}) | ConvertTo-Json -Compress",
		psEscape(vmUUID))

	out, err := d.execPS(script)
	if err != nil {
		return nil, fmt.Errorf("读取检查点列表失败: %v (%s)", err, strings.TrimSpace(out))
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "[]" || out == "null" {
		return []SnapshotInfo{}, nil
	}

	var items []hypervSnapshotItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		// 只有一条快照时 ConvertTo-Json 输出对象而不是数组
		var single hypervSnapshotItem
		if err2 := json.Unmarshal([]byte(out), &single); err2 != nil {
			return nil, fmt.Errorf("解析检查点列表失败: %v", err)
		}
		items = []hypervSnapshotItem{single}
	}

	snapshots := make([]SnapshotInfo, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" || IsBackupSnapshot(name) {
			continue
		}
		snapshots = append(snapshots, SnapshotInfo{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			Parent:      strings.TrimSpace(pveFirewallString(item.ParentSnapshotName)),
			CreatedAt:   int64(pveFirewallInt(item.CreatedAt)),
			State:       strings.TrimSpace(item.SnapshotType),
		})
	}
	return snapshots, nil
}

// CreateSnapshot 创建 Hyper-V 检查点。
func (d *HyperVDriver) CreateSnapshot(vmUUID, name, description string) error {
	// Hyper-V 检查点没有描述字段，描述仅用于前端提示，无法写入平台
	script := fmt.Sprintf(
		"Get-VM -Id '%s' -ErrorAction Stop | Checkpoint-VM -SnapshotName '%s' -ErrorAction Stop",
		psEscape(vmUUID), psEscape(name))
	if out, err := d.execPS(script); err != nil {
		return fmt.Errorf("创建检查点失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// RestoreSnapshot 应用 Hyper-V 检查点。
func (d *HyperVDriver) RestoreSnapshot(vmUUID, name string) error {
	script := fmt.Sprintf(
		"Get-VM -Id '%s' -ErrorAction Stop | Get-VMSnapshot -Name '%s' -ErrorAction Stop | "+
			"Restore-VMSnapshot -Confirm:$false -ErrorAction Stop",
		psEscape(vmUUID), psEscape(name))
	if out, err := d.execPS(script); err != nil {
		return fmt.Errorf("应用检查点失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// DeleteSnapshot 删除 Hyper-V 检查点。
func (d *HyperVDriver) DeleteSnapshot(vmUUID, name string) error {
	script := fmt.Sprintf(
		"Get-VM -Id '%s' -ErrorAction Stop | Get-VMSnapshot -Name '%s' -ErrorAction Stop | "+
			"Remove-VMSnapshot -Confirm:$false -ErrorAction Stop",
		psEscape(vmUUID), psEscape(name))
	if out, err := d.execPS(script); err != nil {
		return fmt.Errorf("删除检查点失败: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}
