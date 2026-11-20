package driver

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeProbeVDI 生成一个最小可用 VDI：块映射表在第 0 项指向 blockIndex，
// 数据区由 fill 写入。块大小取 64KB，与解析窗口一致。
func writeProbeVDI(t *testing.T, path string, blockIndex uint32, fill func([]byte)) {
	t.Helper()

	const (
		offBlocks = 0x1000
		offData   = 0x2000
		blockSize = 0x10000
	)
	buf := make([]byte, offData+blockSize)
	binary.LittleEndian.PutUint32(buf[0x40:], vdiSignature)
	binary.LittleEndian.PutUint32(buf[0x44:], 0x00010001)
	binary.LittleEndian.PutUint32(buf[0x48:], 0x190)
	binary.LittleEndian.PutUint32(buf[0x4c:], 1)
	binary.LittleEndian.PutUint32(buf[vdiOffBlocksField:], offBlocks)
	binary.LittleEndian.PutUint32(buf[vdiOffDataField:], offData)
	binary.LittleEndian.PutUint32(buf[vdiBlockSizeField:], blockSize)
	binary.LittleEndian.PutUint32(buf[vdiBlockCountField:], 2)
	binary.LittleEndian.PutUint32(buf[offBlocks:], blockIndex)
	if fill != nil {
		fill(buf[offData : offData+blockSize])
	}
	if err := os.WriteFile(path, buf, 0600); err != nil {
		t.Fatal(err)
	}
}

// fillGPT 构造保护性 MBR + GPT 头；withESP 决定是否存在 EFI 系统分区。
func fillGPT(withESP bool) func([]byte) {
	return func(b []byte) {
		b[510], b[511] = 0x55, 0xAA
		b[0x1be+4] = 0xEE
		copy(b[512:], "EFI PART")
		binary.LittleEndian.PutUint64(b[512+72:], 2)
		binary.LittleEndian.PutUint32(b[512+80:], 128)
		binary.LittleEndian.PutUint32(b[512+84:], 128)
		if withESP {
			copy(b[1024:1040], gptEFISystemGUID)
		} else {
			b[1024+4] = 0x07
		}
	}
}

// fillLegacyMBR 构造传统 MBR：有引导代码与活动分区。
func fillLegacyMBR(b []byte) {
	copy(b[:2], []byte{0x33, 0xC0})
	b[510], b[511] = 0x55, 0xAA
	b[0x1be] = 0x80
	b[0x1be+4] = 0x07
}

// TestVDIBootFirmware 系统盘固件识别。
//
// 漏判 UEFI 会让虚拟机永远停在 "No bootable medium found!"（BIOS 不能引导 GPT），
// 误判 EFI 则会让传统 MBR 系统盘开不了机，因此三种结果都必须准确。
func TestVDIBootFirmware(t *testing.T) {
	cases := []struct {
		name  string
		block uint32
		fill  func([]byte)
		want  string
	}{
		{"GPT+ESP 需要 EFI", 0, fillGPT(true), vboxFirmwareEFI},
		{"GPT 无 ESP 不判断", 0, fillGPT(false), ""},
		{"传统 MBR 走 BIOS", 0, fillLegacyMBR, vboxFirmwareBIOS},
		{"空盘首扇区", 0, nil, ""},
		{"未分配块", 0xffffffff, fillGPT(true), ""},
		{"全零块", vdiZeroBlock, fillGPT(true), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "disk.vdi")
			writeProbeVDI(t, path, tc.block, tc.fill)
			if got := vdiBootFirmware(path); got != tc.want {
				t.Fatalf("vdiBootFirmware = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVDIBootFirmwareInvalidFile 非 VDI / 不存在的文件必须放弃判断，
// 调用方据此保持 VirtualBox 默认固件，不能因为读不懂就改成 EFI。
func TestVDIBootFirmwareInvalidFile(t *testing.T) {
	dir := t.TempDir()
	if got := vdiBootFirmware(filepath.Join(dir, "missing.vdi")); got != "" {
		t.Fatalf("不存在的文件返回 %q", got)
	}

	raw := filepath.Join(dir, "raw.img")
	if err := os.WriteFile(raw, make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	if got := vdiBootFirmware(raw); got != "" {
		t.Fatalf("非 VDI 文件返回 %q", got)
	}
}
