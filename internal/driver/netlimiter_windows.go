//go:build windows

package driver

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// 服务控制管理器访问掩码（x/sys/windows 未导出 SC_MANAGER_ALL_ACCESS）
const scManagerAllAccess = 0xF003F

// nlDev 已打开的驱动控制设备句柄
var (
	nlDev     windows.Handle
	nlDevOpen bool
)

// nlSupported 当前平台是否支持限速驱动。
func nlSupported() bool { return true }

// nlInstallDriver 释放驱动文件并创建 / 启动内核服务（幂等）。
//
// 启动类型固定为「手动（demand）」：规则表只存在于内核内存，主机重启后必然是空的，
// 设为自启并不能提前恢复限速，反而会让驱动在 agent 被移除后仍然常驻。
// 实际加载时机由 agent 决定：主机驱动初始化、首次下发规则或 Crontab 周期同步时调用。
func nlInstallDriver(driverPath string, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("内置限速驱动为空")
	}
	if err := writeFileIfChanged(driverPath, payload); err != nil {
		return fmt.Errorf("释放驱动文件 %s 失败: %w", driverPath, err)
	}

	name, err := windows.UTF16PtrFromString(nlServiceName)
	if err != nil {
		return err
	}
	binPath, err := windows.UTF16PtrFromString(driverPath)
	if err != nil {
		return err
	}

	scm, err := windows.OpenSCManager(nil, nil, scManagerAllAccess)
	if err != nil {
		return fmt.Errorf("打开服务管理器失败（需要管理员权限）: %w", err)
	}
	defer windows.CloseServiceHandle(scm)

	svc, err := windows.CreateService(scm, name, name, windows.SERVICE_ALL_ACCESS,
		windows.SERVICE_KERNEL_DRIVER, windows.SERVICE_DEMAND_START, windows.SERVICE_ERROR_NORMAL,
		binPath, nil, nil, nil, nil, nil)
	if err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_EXISTS) {
			return fmt.Errorf("创建内核服务失败: %w", err)
		}
		// 服务已存在（例如已由参考工具安装过）：校正驱动路径后继续启动
		svc, err = windows.OpenService(scm, name, windows.SERVICE_ALL_ACCESS)
		if err != nil {
			return fmt.Errorf("打开内核服务失败: %w", err)
		}
		_ = windows.ChangeServiceConfig(svc, windows.SERVICE_NO_CHANGE, windows.SERVICE_NO_CHANGE,
			windows.SERVICE_NO_CHANGE, binPath, nil, nil, nil, nil, nil, nil)
	}
	defer windows.CloseServiceHandle(svc)

	if err := windows.StartService(svc, 0, nil); err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) &&
			!errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return fmt.Errorf("启动内核服务失败: %w", err)
		}
	}
	return nil
}

// nlUninstallDriver 停止并删除内核服务，随后删除驱动文件（幂等）。
//
// 卸载顺序很关键：先停服务（释放内核模块）→ 删服务（避免开机/手动再次加载）→ 删文件。
// 服务删除后句柄关闭才真正生效，因此文件删除会做短暂重试；仍被占用时登记到重启后删除。
func nlUninstallDriver(driverPath string) error {
	name, err := windows.UTF16PtrFromString(nlServiceName)
	if err != nil {
		return err
	}
	scm, err := windows.OpenSCManager(nil, nil, scManagerAllAccess)
	if err != nil {
		return fmt.Errorf("打开服务管理器失败（需要管理员权限）: %w", err)
	}
	defer windows.CloseServiceHandle(scm)

	svc, err := windows.OpenService(scm, name, windows.SERVICE_ALL_ACCESS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			// 服务本来就不存在：只清理可能残留的驱动文件
			return removeDriverFile(driverPath)
		}
		return fmt.Errorf("打开内核服务失败: %w", err)
	}

	// 停止驱动（未运行 / 正在停止时不视为失败）
	var status windows.SERVICE_STATUS
	if err := windows.ControlService(svc, windows.SERVICE_CONTROL_STOP, &status); err != nil &&
		!errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) &&
		!errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
		windows.CloseServiceHandle(svc)
		return fmt.Errorf("停止内核服务失败: %w", err)
	}
	for i := 0; i < 30; i++ {
		st := windows.SERVICE_STATUS{}
		if err := windows.QueryServiceStatus(svc, &st); err != nil {
			break
		}
		if st.CurrentState == windows.SERVICE_STOPPED {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := windows.DeleteService(svc); err != nil &&
		!errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		windows.CloseServiceHandle(svc)
		return fmt.Errorf("删除内核服务失败: %w", err)
	}
	windows.CloseServiceHandle(svc)

	// DeleteService 在最后一个句柄关闭后才完成，等内核回收模块引用再删文件
	time.Sleep(300 * time.Millisecond)
	return removeDriverFile(driverPath)
}

// nlQueryDriver 查询内核服务的安装 / 运行状态。
func nlQueryDriver() (installed, running bool, err error) {
	name, err := windows.UTF16PtrFromString(nlServiceName)
	if err != nil {
		return false, false, err
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false, fmt.Errorf("打开服务管理器失败: %w", err)
	}
	defer windows.CloseServiceHandle(scm)

	svc, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("查询内核服务失败: %w", err)
	}
	defer windows.CloseServiceHandle(svc)

	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svc, &st); err != nil {
		return true, false, fmt.Errorf("查询内核服务状态失败: %w", err)
	}
	return true, st.CurrentState == windows.SERVICE_RUNNING, nil
}

// nlOpenDevice 打开驱动控制设备（加载后可能有一小段初始化延迟，做短重试）。
func nlOpenDevice() error {
	if nlDevOpen {
		return nil
	}
	name, err := windows.UTF16PtrFromString(nlDevicePath)
	if err != nil {
		return err
	}
	var lastErr error
	for i := 0; i < 10; i++ {
		h, err := windows.CreateFile(name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			nlDev = h
			nlDevOpen = true
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return lastErr
}

// nlCloseDevice 关闭设备句柄。
func nlCloseDevice() error {
	if !nlDevOpen {
		return nil
	}
	err := windows.CloseHandle(nlDev)
	nlDev = 0
	nlDevOpen = false
	return err
}

// nlIoctl 发送一次 DeviceIoControl。
// outSize > 0 时使用同一块缓冲做输入/输出（驱动为 METHOD_BUFFERED）。
func nlIoctl(code uint32, in []byte, outSize int) ([]byte, error) {
	if !nlDevOpen {
		return nil, fmt.Errorf("限速设备未打开")
	}
	var inPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	var outPtr *byte
	var outBuf []byte
	if outSize > 0 {
		if len(in) >= outSize {
			outBuf = in[:outSize]
		} else {
			outBuf = make([]byte, outSize)
		}
		outPtr = &outBuf[0]
	}
	var returned uint32
	if err := windows.DeviceIoControl(nlDev, code, inPtr, uint32(len(in)),
		outPtr, uint32(outSize), &returned, nil); err != nil {
		return nil, err
	}
	if outSize <= 0 {
		return nil, nil
	}
	n := int(returned)
	if n <= 0 || n > len(outBuf) {
		n = len(outBuf)
	}
	return outBuf[:n], nil
}

// writeFileIfChanged 仅在内容不一致时写文件，避免每次启动都覆盖驱动。
func writeFileIfChanged(path string, payload []byte) error {
	if cur, err := os.ReadFile(path); err == nil && len(cur) == len(payload) {
		if string(cur) == string(payload) {
			return nil
		}
	}
	return os.WriteFile(path, payload, 0o644)
}

// removeDriverFile 删除驱动文件；仍被内核占用时登记到重启后删除。
func removeDriverFile(path string) error {
	if path == "" {
		return nil
	}
	var lastErr error
	for i := 0; i < 10; i++ {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	if ptr, err := windows.UTF16PtrFromString(path); err == nil {
		if err := windows.MoveFileEx(ptr, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err == nil {
			return nil
		}
	}
	return fmt.Errorf("删除驱动文件失败（可能仍被系统占用，重启后可删除）: %w", lastErr)
}
