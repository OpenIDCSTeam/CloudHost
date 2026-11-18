//go:build !windows

package driver

// 非 Windows 平台没有 Hyper-V 磁盘授权与 MpsSvc 依赖，
// 保留同名方法以保持驱动代码可编译。

func (d *MemuDriver) repairRuntime(fix bool) *MemuRepairReport {
	return nil
}

func (d *MemuDriver) ensureVMDiskACL(index string) *MemuRepairReport {
	return nil
}
