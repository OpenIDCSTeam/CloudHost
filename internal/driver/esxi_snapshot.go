package driver

import (
	"fmt"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// SupportsSnapshot ESXi 提供原生快照
func (d *ESXiDriver) SupportsSnapshot() bool { return true }

// ListSnapshots 读取 ESXi 快照树并展开为扁平列表。
//
// vSphere 的快照是树结构，这里保留 parent 字段，前端以扁平列表展示，
// 用户仍能看出层级关系。
func (d *ESXiDriver) ListSnapshots(vmUUID string) ([]SnapshotInfo, error) {
	vm, err := d.findVM(vmUUID)
	if err != nil {
		return nil, err
	}

	ctx := d.Ctx(vmUUID)
	var props mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"snapshot"}, &props); err != nil {
		return nil, fmt.Errorf("读取快照信息失败: %w", err)
	}
	if props.Snapshot == nil {
		return []SnapshotInfo{}, nil
	}

	currentRef := ""
	if props.Snapshot.CurrentSnapshot != nil {
		currentRef = props.Snapshot.CurrentSnapshot.Value
	}

	snapshots := make([]SnapshotInfo, 0)
	var walk func(nodes []types.VirtualMachineSnapshotTree, parent string)
	walk = func(nodes []types.VirtualMachineSnapshotTree, parent string) {
		for _, node := range nodes {
			if IsBackupSnapshot(node.Name) {
				// 备份快照仍然遍历子节点，避免把用户快照从树上截断
				walk(node.ChildSnapshotList, parent)
				continue
			}
			snapshots = append(snapshots, SnapshotInfo{
				Name:        node.Name,
				Description: node.Description,
				Parent:      parent,
				CreatedAt:   node.CreateTime.Unix(),
				State:       string(node.State),
				Current:     node.Snapshot.Value == currentRef,
			})
			walk(node.ChildSnapshotList, node.Name)
		}
	}
	walk(props.Snapshot.RootSnapshotList, "")
	return snapshots, nil
}

// CreateSnapshot 创建 ESXi 快照（不含内存状态，避免长时间停顿）
func (d *ESXiDriver) CreateSnapshot(vmUUID, name, description string) error {
	vm, err := d.findVM(vmUUID)
	if err != nil {
		return err
	}
	ctx := d.Ctx(vmUUID)
	task, err := vm.CreateSnapshot(ctx, name, description, false, false)
	if err != nil {
		return fmt.Errorf("创建快照失败: %w", err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("等待快照创建失败: %w", err)
	}
	return nil
}

// RestoreSnapshot 回滚到 ESXi 快照
func (d *ESXiDriver) RestoreSnapshot(vmUUID, name string) error {
	vm, err := d.findVM(vmUUID)
	if err != nil {
		return err
	}
	ctx := d.Ctx(vmUUID)
	task, err := vm.RevertToSnapshot(ctx, name, false)
	if err != nil {
		return fmt.Errorf("回滚快照失败: %w", err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("等待快照回滚失败: %w", err)
	}
	return nil
}

// DeleteSnapshot 删除 ESXi 快照。
//
// removeChildren=false：只删除指定快照，子快照保留并由 vSphere 合并，
// 避免用户删一个中间快照时把后续快照一并清空。
func (d *ESXiDriver) DeleteSnapshot(vmUUID, name string) error {
	vm, err := d.findVM(vmUUID)
	if err != nil {
		return err
	}
	ctx := d.Ctx(vmUUID)
	task, err := vm.RemoveSnapshot(ctx, name, false, nil)
	if err != nil {
		return fmt.Errorf("删除快照失败: %w", err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("等待快照删除失败: %w", err)
	}
	return nil
}
