package api

import (
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// 历史虚拟机没有 efi_all 时，应生成「系统盘 → 数据盘 → 光驱 → 网卡」的默认列表，
// 否则前端启动顺序页只能显示空白/占位。
func TestDefaultBootOptions(t *testing.T) {
	vm := model.NewVMConfig()
	vm.VmUUID = "pve-test"
	vm.HddAll["data-1"] = model.SDConfig{HddFlag: 1}
	vm.HddAll["data-2"] = model.SDConfig{HddFlag: 0} // 未启用，不应出现
	vm.IsoAll["iso-1"] = model.IMConfig{IsoFile: "a.iso"}
	vm.NicAll["nic0"] = model.NCConfig{MacAddr: "10:f6:0a:02:14:64"}

	list := defaultBootOptions(vm)
	if len(list) != 4 {
		t.Fatalf("应有 4 项（系统盘/数据盘/光驱/网卡），实际 %d: %+v", len(list), list)
	}
	if list[0].EfiName != "pve-test" || list[0].EfiType {
		t.Errorf("首项应为系统盘，实际 %+v", list[0])
	}
	if list[1].EfiName != "pve-test-data-1" {
		t.Errorf("第二项应为启用的数据盘，实际 %+v", list[1])
	}
	if !list[2].EfiType || list[2].EfiName != "iso-1" {
		t.Errorf("第三项应为光驱，实际 %+v", list[2])
	}
	if list[3].EfiName != "网卡(PXE)" {
		t.Errorf("末项应为网卡，实际 %+v", list[3])
	}
}

// 没有任何外设时应至少返回系统盘一项。
func TestDefaultBootOptionsMinimal(t *testing.T) {
	vm := model.NewVMConfig()
	vm.VmUUID = "pve-min"
	list := defaultBootOptions(vm)
	if len(list) != 1 || list[0].EfiName != "pve-min" {
		t.Fatalf("应只有系统盘一项，实际 %+v", list)
	}
}
