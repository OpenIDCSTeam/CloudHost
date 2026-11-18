// Package driver VirtualBox 备份（克隆机方案）。
//
// VirtualBox 没有 PVE 那种「备份文件」概念，最接近完整备份的原语是
// `VBoxManage clonevm <vm> --name <新机> --register`：克隆出一台完整的
// 独立虚拟机（含全部磁盘）。本文件以克隆机作为备份介质：
//
//	创建 —— 运行中的虚拟机先打临时快照，再从快照克隆，克隆完删除临时快照；
//	还原 —— 关机 → 卸载并删除当前系统盘 → 从克隆机磁盘克隆回系统盘 → 挂载 → 恢复原电源状态；
//	删除 —— unregistervm --delete 回收克隆机与磁盘；
//	扫描 —— 按克隆机命名前缀重建备份列表（重启后可恢复）。
//
// 备份克隆机的名字带 "bak-" 前缀与 "-backup-" 标记，扫描纳管时会被跳过
// （见 ScanVMs / IsVBoxBackupName），不会作为业务虚拟机出现在列表里。
package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

const (
	vboxBackupPrefix = "bak-"
	vboxBackupMark   = "-backup-"
)

// IsVBoxBackupName 判断虚拟机名是否为备份克隆机。
func IsVBoxBackupName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, vboxBackupPrefix) || strings.Contains(n, vboxBackupMark)
}

// vboxBackupName 生成备份克隆机名：bak-<vm>-backup-<时间戳>。
func vboxBackupName(vmUUID string) string {
	return fmt.Sprintf("%s%s%s%s", vboxBackupPrefix, vmUUID, vboxBackupMark,
		time.Now().Format("20060102-150405"))
}

// CreateBackup 创建备份（克隆整台虚拟机）。
func (d *VirtualBoxDriver) CreateBackup(vmUUID string, tips string) (*model.ZMessage, error) {
	if d.GetPower(vmUUID) == "未知" {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: "虚拟机不存在"}, nil
	}

	name := vboxBackupName(vmUUID)

	// 运行中的虚拟机无法直接 clonevm：先打临时快照，从快照克隆后再删掉快照，
	// 既保证磁盘一致性，也不污染用户的快照列表。
	snapshot := ""
	if d.GetPower(vmUUID) == "运行中" {
		snapshot = fmt.Sprintf("oidcs-backup-%d", time.Now().UnixNano())
		if out, err := d.execVBox("snapshot", vmUUID, "take", snapshot,
			"--description", "自动创建的备份临时快照"); err != nil {
			return &model.ZMessage{Success: false, Action: "VMBackup",
				Message: fmt.Sprintf("创建备份快照失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
		defer func() {
			_, _ = d.execVBox("snapshot", vmUUID, "delete", snapshot)
		}()
	}

	args := []string{"clonevm", vmUUID, "--name", name, "--register"}
	if snapshot != "" {
		args = append(args, "--snapshot", snapshot)
	}
	if out, err := d.execVBox(args...); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: fmt.Sprintf("创建备份失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	// 克隆自「运行中快照」的备份机会带着保存的运行态，立即丢弃运行态，
	// 保证备份机始终处于可读盘的关机状态（还原时不必再处理）。
	if d.GetPower(name) != "已关机" {
		_, _ = d.execVBox("discardstate", name)
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.Backups = append(cfg.Backups, model.VMBackup{
			BackupName: name, BackupTime: time.Now(), BackupTips: tips,
		})
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMBackup", Message: "备份创建成功",
		Results: map[string]interface{}{"backup_name": name}}, nil
}

// RestoreBackup 从备份克隆机还原系统盘。
//
// 与基类的目录归档实现不同，VirtualBox 的备份是「注册的克隆机」，
// 因此还原方式是：把备份机的系统盘克隆回原虚拟机的系统盘路径。
func (d *VirtualBoxDriver) RestoreBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	if !IsVBoxBackupName(backupName) || vboxBackupOwner(backupName) != vmUUID {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: "备份名称无效或不属于该虚拟机: " + backupName}, nil
	}
	if d.GetPower(backupName) == "未知" {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: "备份不存在: " + backupName}, nil
	}
	if d.GetPower(vmUUID) == "未知" {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: "虚拟机不存在: " + vmUUID}, nil
	}

	// 备份机不能处于运行状态（读取其磁盘做克隆会失败）。
	// 从运行中虚拟机的快照克隆出来的备份机带有「保存的运行态」（saved），
	// 既不能用 controlvm poweroff（没在运行），也不能直接读盘，
	// 需要先 discardstate 丢弃运行态——磁盘内容仍是快照时刻的一致状态。
	if d.GetPower(backupName) != "已关机" {
		_, _ = d.PowerControl(backupName, model.VMPowerHClose)
		if !d.vboxWaitPowerOff(backupName, 15*time.Second) {
			if out, err := d.execVBox("discardstate", backupName); err != nil {
				return &model.ZMessage{Success: false, Action: "Restores",
					Message: fmt.Sprintf("备份虚拟机无法停止，不能还原: %v (%s)", err, strings.TrimSpace(out))}, nil
			}
			_ = d.vboxWaitPowerOff(backupName, 15*time.Second)
		}
	}

	backupDisk := d.vboxSystemDisk(backupName)
	if backupDisk == "" {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: "备份中没有可用的系统盘"}, nil
	}

	wasRunning := d.GetPower(vmUUID) == "运行中"
	if wasRunning {
		_, _ = d.PowerControl(vmUUID, model.VMPowerHClose)
		if !d.vboxWaitPowerOff(vmUUID, 30*time.Second) {
			return &model.ZMessage{Success: false, Action: "Restores",
				Message: "虚拟机仍在运行，无法还原（原数据未改动）"}, nil
		}
	}

	diskPath := filepath.Join(d.vboxDiskDir(), vmUUID+".vdi")
	if current := d.vboxSystemDisk(vmUUID); current != "" {
		diskPath = current
	}

	// 1) 卸载并删除现有系统盘
	_, _ = d.execVBox("storageattach", vmUUID, "--storagectl", "SATA",
		"--port", "0", "--device", "0", "--type", "hdd", "--medium", "none")
	if out, err := d.execVBox("closemedium", "disk", diskPath, "--delete"); err != nil {
		if _, statErr := os.Stat(diskPath); statErr == nil {
			if wasRunning {
				_, _ = d.PowerControl(vmUUID, model.VMPowerSStart)
			}
			return &model.ZMessage{Success: false, Action: "Restores",
				Message: fmt.Sprintf("删除当前系统盘失败（原数据未改动）: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	// 2) 从备份盘克隆回系统盘
	if out, err := d.execVBox("clonemedium", backupDisk, diskPath, "--format", "VDI"); err != nil {
		if wasRunning {
			_, _ = d.PowerControl(vmUUID, model.VMPowerSStart)
		}
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("从备份克隆磁盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}

	// 3) 挂载并恢复原电源状态
	if out, err := d.execVBox("storageattach", vmUUID, "--storagectl", "SATA",
		"--port", "0", "--device", "0", "--type", "hdd", "--medium", diskPath); err != nil {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("挂载还原后的磁盘失败: %v (%s)", err, strings.TrimSpace(out))}, nil
	}
	if wasRunning {
		_, _ = d.PowerControl(vmUUID, model.VMPowerSStart)
	}

	return &model.ZMessage{Success: true, Action: "Restores",
		Message: "还原成功: " + backupName}, nil
}

// DeleteBackup 删除备份（回收克隆机与其磁盘）。
func (d *VirtualBoxDriver) DeleteBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	if !IsVBoxBackupName(backupName) || vboxBackupOwner(backupName) != vmUUID {
		return &model.ZMessage{Success: false, Action: "RMBackup",
			Message: "备份名称无效或不属于该虚拟机: " + backupName}, nil
	}

	if d.GetPower(backupName) == "运行中" {
		_, _ = d.PowerControl(backupName, model.VMPowerHClose)
		_ = d.vboxWaitPowerOff(backupName, 30*time.Second)
	}
	// 备份机可能不存在（已手动清理）：unregister 报错时按「已删除」处理
	if out, err := d.execVBox("unregistervm", backupName, "--delete"); err != nil {
		if d.GetPower(backupName) != "未知" {
			return &model.ZMessage{Success: false, Action: "RMBackup",
				Message: fmt.Sprintf("删除备份失败: %v (%s)", err, strings.TrimSpace(out))}, nil
		}
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		kept := make([]model.VMBackup, 0, len(cfg.Backups))
		for _, b := range cfg.Backups {
			if b.BackupName != backupName {
				kept = append(kept, b)
			}
		}
		cfg.Backups = kept
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "RMBackup", Message: "备份删除成功"}, nil
}

// ScanBackups 扫描备份克隆机，重建各虚拟机的备份列表。
func (d *VirtualBoxDriver) ScanBackups() (*model.ZMessage, error) {
	out, err := d.execVBox("list", "vms")
	if err != nil {
		return &model.ZMessage{Success: false, Action: "LDBackup",
			Message: "读取虚拟机列表失败: " + err.Error()}, nil
	}

	type found struct {
		name string
		when time.Time
	}
	byVM := map[string][]found{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "{") {
			continue
		}
		name := strings.Trim(strings.SplitN(line, " ", 2)[0], "\"")
		if !IsVBoxBackupName(name) {
			continue
		}
		owner := vboxBackupOwner(name)
		if owner == "" {
			continue
		}
		bt := time.Now()
		if info, statErr := os.Stat(filepath.Join(d.vboxDiskDir(), owner+".vdi")); statErr == nil {
			bt = info.ModTime()
		}
		byVM[owner] = append(byVM[owner], found{name: name, when: bt})
	}

	d.mu.Lock()
	for vmUUID, cfg := range d.VmSaving {
		cfg.Backups = []model.VMBackup{}
		for _, f := range byVM[vmUUID] {
			cfg.Backups = append(cfg.Backups, model.VMBackup{
				BackupName: f.name,
				BackupTime: f.when,
			})
		}
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "LDBackup",
		Message: fmt.Sprintf("扫描完成，共发现 %d 个备份", len(byVM))}, nil
}

// vboxBackupOwner 从备份克隆机名反解出虚拟机名（bak-<vm>-backup-<ts>）。
func vboxBackupOwner(name string) string {
	n := strings.TrimSpace(name)
	if !strings.HasPrefix(strings.ToLower(n), vboxBackupPrefix) {
		return ""
	}
	n = n[len(vboxBackupPrefix):]
	idx := strings.LastIndex(n, vboxBackupMark)
	if idx <= 0 {
		return ""
	}
	return n[:idx]
}
