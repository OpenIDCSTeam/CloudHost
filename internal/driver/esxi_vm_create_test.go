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
	devices, err := esxiCreateDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 40960)
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
	devices, err := esxiCreateDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 0)
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
	devices, err := esxiCreateDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 10240)
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

// TestESXiCreateDevicesHardware 创建时必须带 IDE 控制器、空光驱、虚拟声卡与 USB 控制器。
//
// vSphere 的 CD/DVD 驱动器只能挂在 IDE/SATA 控制器上，声卡是独立 PCI 设备，
// USB 直通设备（VirtualUSB）必须有 USB 控制器承载；缺任何一个都会少一项硬件。
func TestESXiCreateDevicesHardware(t *testing.T) {
	dsRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}
	devices, err := esxiCreateDevices(dsRef, "[datastore1] vm-1/vm-1.vmdk", 8192)
	if err != nil {
		t.Fatalf("构建虚拟硬件失败: %v", err)
	}

	var ide *types.VirtualIDEController
	var cdrom *types.VirtualCdrom
	var sound *types.VirtualHdAudioCard
	var usb *types.VirtualUSBXHCIController
	for _, dev := range devices {
		switch d := dev.(type) {
		case *types.VirtualIDEController:
			ide = d
		case *types.VirtualCdrom:
			cdrom = d
		case *types.VirtualHdAudioCard:
			sound = d
		case *types.VirtualUSBXHCIController:
			usb = d
		}
	}

	if ide == nil {
		t.Fatal("缺少 IDE 控制器：光驱没有可挂载的控制器")
	}
	if cdrom == nil {
		t.Fatal("缺少虚拟光驱：无法挂载 ISO")
	}
	if cdrom.ControllerKey != ide.Key {
		t.Fatalf("光驱未挂到 IDE 控制器: ControllerKey=%d, want %d", cdrom.ControllerKey, ide.Key)
	}
	if _, ok := cdrom.Backing.(*types.VirtualCdromAtapiBackingInfo); !ok {
		t.Fatalf("光驱 backing 类型异常: %T", cdrom.Backing)
	}
	if sound == nil {
		t.Fatal("缺少虚拟声卡")
	}
	if sound.Key >= 0 {
		t.Fatalf("声卡设备 key 必须为负数（由 vSphere 分配），实际 %d", sound.Key)
	}
	if usb == nil {
		t.Fatal("缺少 USB 控制器：USB 直通设备无法挂载")
	}
	if usb.Key >= 0 {
		t.Fatalf("USB 控制器 key 必须为负数（由 vSphere 分配），实际 %d", usb.Key)
	}
	if usb.AutoConnectDevices == nil || !*usb.AutoConnectDevices {
		t.Fatal("USB 控制器未开启热插拔（AutoConnectDevices）")
	}
}
