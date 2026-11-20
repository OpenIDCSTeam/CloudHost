// Package driver VirtualBox 固件类型（BIOS / UEFI）自适应。
//
// VirtualBox 创建虚拟机时 ostype 只决定默认固件：Windows10_64 / Linux26_64
// 默认给 BIOS，Windows11_64 默认给 EFI。而系统模板（尤其是 Windows 云镜像）
// 常常是 GPT 分区表、只带 UEFI 引导项，BIOS 固件下开机只会停在
//
//	No bootable medium found! Please insert a bootable medium and reboot.
//
// 因此不能只看 ostype：克隆出系统盘后直接读分区表，按磁盘实际类型选择固件。
package driver

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
)

// 虚拟机固件类型，取值与 VBoxManage modifyvm --firmware 一致。
const (
	vboxFirmwareBIOS = "bios"
	vboxFirmwareEFI  = "efi"
)

// VDI 1.1 文件头中用到的字段（相对文件起始的绝对偏移）。
//
// 布局见 VirtualBox VDI 格式：0x40 起为文件头，0x154/0x158 分别是
// 块映射表与数据区偏移，0x178 是块大小（默认 1MB），0x180 是块总数。
const (
	vdiHeaderOffset    = 0x40
	vdiSignature       = 0xbeda107f
	vdiOffBlocksField  = 0x154
	vdiOffDataField    = 0x158
	vdiBlockSizeField  = 0x178
	vdiBlockCountField = 0x180

	// 块映射表项：全零块与未分配块（固定盘/动态盘通用）。
	vdiZeroBlock = 0xfffffffe
)

// gptEFISystemGUID 是 GPT 分区项里的「EFI 系统分区」类型 GUID
// （C12A7328-F81F-11D2-BA4B-00A0C93EC93B）的磁盘字节序。
var gptEFISystemGUID = []byte{
	0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11,
	0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b,
}

// vdiBootFirmware 读取 VDI 系统盘的分区表，返回该盘可引导所需的固件类型。
//
// 返回 vboxFirmwareEFI 表示 GPT 磁盘且存在 EFI 系统分区（UEFI 引导）；
// vboxFirmwareBIOS 表示传统 MBR 引导；无法判断（非 VDI、空盘、读取失败）
// 时返回空串，调用方应保持 VirtualBox 的默认固件，不要盲目改成 EFI。
func vdiBootFirmware(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	header := make([]byte, 0x200)
	if _, err := f.ReadAt(header, vdiHeaderOffset); err != nil {
		return ""
	}
	if binary.LittleEndian.Uint32(header) != vdiSignature {
		return ""
	}
	offBlocks := int64(binary.LittleEndian.Uint32(header[vdiOffBlocksField-vdiHeaderOffset:]))
	offData := int64(binary.LittleEndian.Uint32(header[vdiOffDataField-vdiHeaderOffset:]))
	blockSize := int64(binary.LittleEndian.Uint32(header[vdiBlockSizeField-vdiHeaderOffset:]))
	blockCount := int64(binary.LittleEndian.Uint32(header[vdiBlockCountField-vdiHeaderOffset:]))
	if offBlocks <= 0 || offData <= 0 || blockSize <= 0 || blockCount <= 0 {
		return ""
	}

	// 第 0 个逻辑块的块号：0 之外都说明首扇区不在文件起始处的数据区。
	entry := make([]byte, 4)
	if _, err := f.ReadAt(entry, offBlocks); err != nil {
		return ""
	}
	block := int64(binary.LittleEndian.Uint32(entry))
	if block == vdiZeroBlock || block < 0 || block >= blockCount {
		return ""
	}

	// MBR、GPT 头与 GPT 分区表都在第一个逻辑块内，读 64KB 足够。
	window := blockSize
	if window > 64*1024 {
		window = 64 * 1024
	}
	if window < 1024 {
		return ""
	}
	diskHead := make([]byte, window)
	if _, err := f.ReadAt(diskHead, offData+block*blockSize); err != nil {
		return ""
	}
	return bootFirmwareFromSectors(diskHead)
}

// bootFirmwareFromSectors 根据磁盘起始扇区判断引导方式。
func bootFirmwareFromSectors(head []byte) string {
	if len(head) < 512 {
		return ""
	}
	mbr := head[:512]
	// 没有 0x55AA 结束标记：空盘或未分区，任何固件都引导不了。
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return ""
	}
	// 保护性 MBR（分区类型 0xEE）+ GPT 头：GPT 磁盘。
	// VirtualBox 的 BIOS 不支持从 GPT 启动，这类盘必须用 EFI。
	if hasProtectiveMBR(mbr) && len(head) >= 1024 && string(head[512:520]) == "EFI PART" {
		if gptHasESPPartition(head) {
			return vboxFirmwareEFI
		}
		return ""
	}
	if hasLegacyMBR(mbr) {
		return vboxFirmwareBIOS
	}
	return ""
}

// hasProtectiveMBR 判断 MBR 中是否存在保护性分区项（GPT 磁盘的标志）。
func hasProtectiveMBR(mbr []byte) bool {
	for i := 0; i < 4; i++ {
		if mbr[0x1be+i*16+4] == 0xEE {
			return true
		}
	}
	return false
}

// hasLegacyMBR 判断是否为可被 BIOS 引导的传统 MBR：
// 引导代码非空，或存在状态为 0x80 的活动分区。
func hasLegacyMBR(mbr []byte) bool {
	for _, b := range mbr[:440] {
		if b != 0 {
			return true
		}
	}
	for i := 0; i < 4; i++ {
		e := mbr[0x1be+i*16:]
		if e[0] == 0x80 && e[4] != 0 {
			return true
		}
	}
	return false
}

// gptHasESPPartition 检查 GPT 分区表中是否存在 EFI 系统分区。
//
// 只有带 ESP 的 GPT 盘才能被 UEFI 引导；没有 ESP 的 GPT 盘（例如只带
// bios_grub 分区）改成 EFI 同样开不了机，此时返回 false 让调用方保持原状。
func gptHasESPPartition(head []byte) bool {
	if len(head) < 1024 {
		return false
	}
	gpt := head[512:1024]
	entryLBA := int64(binary.LittleEndian.Uint64(gpt[72:80]))
	count := int64(binary.LittleEndian.Uint32(gpt[80:84]))
	size := int64(binary.LittleEndian.Uint32(gpt[84:88]))
	if entryLBA <= 0 || count <= 0 || size < 128 {
		return false
	}
	start := entryLBA * 512
	end := start + count*size
	// 分区表必须落在已读到的磁盘首部窗口内，否则无法判断。
	if start < 1024 || end > int64(len(head)) {
		return false
	}
	for i := int64(0); i < count; i++ {
		entry := head[start+i*size:]
		if len(entry) < 16 {
			return false
		}
		if bytes.Equal(entry[:16], gptEFISystemGUID) {
			return true
		}
	}
	return false
}

// vboxAlignFirmware 在关机状态下让虚拟机固件与系统盘分区表一致。
//
// 老版本创建虚拟机时一律使用 BIOS，UEFI 系统盘会永远停在
// "No bootable medium found!"。开机前自查一次即可修复已存在的虚拟机；
// 已经是 EFI、磁盘无法判断（非 VDI/读取失败）时不做任何改动。
func (d *VirtualBoxDriver) vboxAlignFirmware(vmName string) {
	out, err := d.execVBox("showvminfo", vmName, "--machinereadable")
	if err != nil {
		return
	}
	firmware, disk := "", ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "firmware="):
			firmware = strings.ToLower(strings.Trim(strings.TrimPrefix(line, "firmware="), `"`))
		case strings.HasPrefix(line, `"SATA-0-0"=`):
			disk = strings.Trim(strings.TrimPrefix(line, `"SATA-0-0"=`), `"`)
		}
	}
	if firmware != vboxFirmwareBIOS || disk == "" {
		return
	}
	if !strings.EqualFold(filepath.Ext(disk), ".vdi") {
		return
	}
	if vdiBootFirmware(disk) != vboxFirmwareEFI {
		return
	}
	_, _ = d.execVBox("modifyvm", vmName, "--firmware", vboxFirmwareEFI)
}
