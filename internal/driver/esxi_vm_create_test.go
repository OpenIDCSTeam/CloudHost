package driver

import (
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

// TestESXiSystemDiskDevices 创建虚拟机时必须带上磁盘控制器与系统盘。
//
// 历史问题：CreateVM 只下发 CPU/内存，ESXi 上得到的是一台没有任何硬盘的虚拟机，
// vSphere「编辑设置」里只剩 CPU/内存/显卡，客户根本装不了系统。
func TestESXiSystemDiskDevices(t *testing.T) {
	dsRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}
	devices, err := esxiSystemDiskDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 40960)
	if err != nil {
		t.Fatalf("构建系统盘设备失败: %v", err)
	}

	controller, err := devices.FindDiskController("")
	if err != nil {
		t.Fatalf("未生成磁盘控制器: %v", err)
	}
	controllerKey := controller.GetVirtualController().Key

	var disk *types.VirtualDisk
	for _, dev := range devices {
		if d, ok := dev.(*types.VirtualDisk); ok {
			if disk != nil {
				t.Fatalf("生成了多块系统盘")
			}
			disk = d
		}
	}
	if disk == nil {
		t.Fatal("系统盘缺失：创建出的虚拟机不会有硬盘")
	}
	if disk.CapacityInKB != int64(40960)*1024 {
		t.Fatalf("系统盘容量 = %d KB, want %d KB", disk.CapacityInKB, int64(40960)*1024)
	}
	if disk.ControllerKey != controllerKey {
		t.Fatalf("系统盘未挂到磁盘控制器: ControllerKey=%d, want %d", disk.ControllerKey, controllerKey)
	}

	backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	if !ok {
		t.Fatalf("系统盘 backing 类型异常: %T", disk.Backing)
	}
	if backing.FileName != "[datastore1] vm-1/vm-1.vmdk" {
		t.Fatalf("系统盘路径 = %q", backing.FileName)
	}
	if backing.Datastore == nil || backing.Datastore.Value != dsRef.Value {
		t.Fatalf("系统盘未指定数据存储: %+v", backing.Datastore)
	}
}

// TestESXiSystemDiskDefaultSize 未配置磁盘大小时按 8GB 创建，避免 0 容量盘。
func TestESXiSystemDiskDefaultSize(t *testing.T) {
	dsRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}
	devices, err := esxiSystemDiskDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 0)
	if err != nil {
		t.Fatalf("构建系统盘设备失败: %v", err)
	}

	for _, dev := range devices {
		if disk, ok := dev.(*types.VirtualDisk); ok {
			if disk.CapacityInKB != int64(8192)*1024 {
				t.Fatalf("默认容量 = %d KB, want %d KB", disk.CapacityInKB, int64(8192)*1024)
			}
			return
		}
	}
	t.Fatal("系统盘缺失")
}

// TestESXiSystemDiskConfigSpec 创建规范里系统盘必须声明「创建文件」，
// 否则 vSphere 只登记设备而不落 VMDK。
func TestESXiSystemDiskConfigSpec(t *testing.T) {
	dsRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}
	devices, err := esxiSystemDiskDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 10240)
	if err != nil {
		t.Fatalf("构建系统盘设备失败: %v", err)
	}
	specs, err := devices.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	if err != nil {
		t.Fatalf("生成设备变更规范失败: %v", err)
	}

	var found bool
	for _, spec := range specs {
		s := spec.GetVirtualDeviceConfigSpec()
		if _, ok := s.Device.(*types.VirtualDisk); !ok {
			continue
		}
		found = true
		if s.Operation != types.VirtualDeviceConfigSpecOperationAdd {
			t.Fatalf("系统盘操作 = %v, want add", s.Operation)
		}
		if s.FileOperation != types.VirtualDeviceConfigSpecFileOperationCreate {
			t.Fatalf("系统盘文件操作 = %v, want create", s.FileOperation)
		}
	}
	if !found {
		t.Fatal("创建规范里没有系统盘")
	}
}
