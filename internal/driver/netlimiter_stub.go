//go:build !windows

package driver

// nlSupported 当前平台是否支持限速驱动。
func nlSupported() bool { return false }

// nlInstallDriver 非 Windows 平台无内核驱动可加载。
func nlInstallDriver(driverPath string, payload []byte) error {
	return errNetLimiterUnsupported
}

// nlUninstallDriver 非 Windows 平台无内核服务可卸载。
func nlUninstallDriver(driverPath string) error {
	return errNetLimiterUnsupported
}

// nlQueryDriver 非 Windows 平台恒为「未安装」。
func nlQueryDriver() (installed, running bool, err error) {
	return false, false, nil
}

// nlOpenDevice 非 Windows 平台没有控制设备。
func nlOpenDevice() error { return errNetLimiterUnsupported }

// nlCloseDevice 无设备可关闭。
func nlCloseDevice() error { return nil }

// nlIoctl 非 Windows 平台无 IOCTL 通道。
func nlIoctl(code uint32, in []byte, outSize int) ([]byte, error) {
	return nil, errNetLimiterUnsupported
}
