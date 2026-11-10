package driver

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// proxmoxSnapshotTimeout 创建/回滚快照的等待上限（大磁盘可能较久）
const proxmoxSnapshotTimeout = 30 * time.Minute

// pveSnapshotItem PVE 快照列表原始结构。
//
// 字段用 interface{} 接收：不同 PVE 版本对 snaptime / parent / vmstate
// 的类型描述不完全一致，写死类型会导致整份列表解析失败。
type pveSnapshotItem struct {
	Name        string      `json:"name"`
	Description interface{} `json:"description"`
	Snaptime    interface{} `json:"snaptime"`
	Parent      interface{} `json:"parent"`
	Vmstate     interface{} `json:"vmstate"`
}

// SupportsSnapshot PVE 提供虚拟机快照
func (d *ProxmoxDriver) SupportsSnapshot() bool { return true }

// pveSnapshotPath 拼装虚拟机快照 API 路径
func (d *ProxmoxDriver) pveSnapshotPath(vmid int64, suffix string) string {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", d.nodeName(), vmid)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

// ListSnapshots 读取 PVE 快照列表。
//
// PVE 会在列表中额外返回一条 name=current 的伪条目，代表「当前状态」，
// 必须过滤掉，否则前端会出现一个无法操作的 current 快照。
func (d *ProxmoxDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	client, err := d.apiConn()
	if err != nil {
		return nil, err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	var raw []pveSnapshotItem
	if err := client.Get(ctx, d.pveSnapshotPath(vmid, ""), &raw); err != nil {
		return nil, fmt.Errorf("读取快照列表失败: %w", err)
	}

	snapshots := make([]SnapshotInfo, 0, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item.Name)
		if name == "" || strings.EqualFold(name, "current") {
			continue
		}
		// 备份功能创建的快照不进入快照页，避免误删导致备份记录失效
		if IsBackupSnapshot(name) {
			continue
		}
		snapshots = append(snapshots, SnapshotInfo{
			Name:        name,
			Description: pveFirewallString(item.Description),
			Parent:      pveFirewallString(item.Parent),
			CreatedAt:   int64(pveFirewallInt(item.Snaptime)),
			State:       pveSnapshotState(pveFirewallBool(item.Vmstate)),
		})
	}
	return snapshots, nil
}

// pveSnapshotState 把 vmstate 转成可读状态
func pveSnapshotState(vmstate bool) string {
	if vmstate {
		return "含内存状态"
	}
	return "仅磁盘"
}

// CreateSnapshot 创建 PVE 快照。
func (d *ProxmoxDriver) CreateSnapshot(vmUUID, name, description string) error {
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	body := map[string]interface{}{
		"snapname": name,
		"vmstate":  0,
	}
	if description = strings.TrimSpace(description); description != "" {
		body["description"] = description
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	var upid string
	if err := client.Post(ctx, d.pveSnapshotPath(vmid, ""), body, &upid); err != nil {
		return fmt.Errorf("创建快照失败: %w", err)
	}
	if err := d.waitPVETask(upid, proxmoxSnapshotTimeout); err != nil {
		return fmt.Errorf("创建快照失败: %w", err)
	}
	return nil
}

// RestoreSnapshot 回滚到指定 PVE 快照。
func (d *ProxmoxDriver) RestoreSnapshot(vmUUID, name string) error {
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	var upid string
	path := d.pveSnapshotPath(vmid, url.PathEscape(name)+"/rollback")
	if err := client.Post(ctx, path, nil, &upid); err != nil {
		return fmt.Errorf("回滚快照失败: %w", err)
	}
	if err := d.waitPVETask(upid, proxmoxSnapshotTimeout); err != nil {
		return fmt.Errorf("回滚快照失败: %w", err)
	}
	return nil
}

// DeleteSnapshot 删除指定 PVE 快照。
//
// 存在子快照时 PVE 会拒绝删除；把平台错误原样返回，
// 让用户先在平台上清理子快照，避免这里静默 force 删除整条链。
func (d *ProxmoxDriver) DeleteSnapshot(vmUUID, name string) error {
	client, err := d.apiConn()
	if err != nil {
		return err
	}
	vmid, err := d.getVMID(vmUUID)
	if err != nil {
		return err
	}

	ctx, cancel := d.pveFirewallContext(vmUUID)
	defer cancel()

	var upid string
	if err := client.Delete(ctx, d.pveSnapshotPath(vmid, url.PathEscape(name)), &upid); err != nil {
		return fmt.Errorf("删除快照失败: %w", err)
	}
	if err := d.waitPVETask(upid, proxmoxSnapshotTimeout); err != nil {
		return fmt.Errorf("删除快照失败: %w", err)
	}
	return nil
}
