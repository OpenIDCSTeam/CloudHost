package driver

import (
	"strings"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestQEMUBuildDomainXMLHardware 新建域必须带声卡与常驻空光驱。
//
// 此前 buildDomainXML 只有系统盘和网卡，客户机里既没有音频设备，
// 也没有可用的 CD/DVD 驱动器；挂载 ISO 只能靠 attach-disk 现场加设备。
func TestQEMUBuildDomainXMLHardware(t *testing.T) {
	cfg := model.NewHSConfig()
	cfg.NetworkNat = "virbr0"
	d := NewQEMUDriver(cfg)

	vm := model.NewVMConfig()
	vm.VmUUID = "qemu-1"
	vm.NicAll = map[string]model.NCConfig{
		"nat1": {NicName: "nat1", NicType: "nat", MacAddr: "52:54:00:11:22:33"},
	}

	xml := d.buildDomainXML(vm, "/var/lib/libvirt/images/qemu-1.qcow2", "")
	for _, want := range []string{
		"<controller type='ide' index='0'/>",
		"<controller type='usb' index='0' model='qemu-xhci'/>",
		"<sound model='ich9'/>",
		"device='cdrom'",
		"<target dev='hdc' bus='ide'/>",
	} {
		if !strings.Contains(xml, want) {
			t.Fatalf("域定义缺少 %s：\n%s", want, xml)
		}
	}

	// 空光驱不能带 source，否则 libvirt 会按已插入介质处理
	cdromAt := strings.Index(xml, "device='cdrom'")
	cdromBlock := xml[cdromAt:]
	if end := strings.Index(cdromBlock, "</disk>"); end > 0 {
		cdromBlock = cdromBlock[:end]
	}
	if strings.Contains(cdromBlock, "<source") {
		t.Fatalf("空光驱不应指定介质：\n%s", cdromBlock)
	}
}

// TestStripSoundCardXML 宿主机没有音频后端时的降级路径：
// 去掉声卡后仍要保留磁盘、网卡等其它设备。
func TestStripSoundCardXML(t *testing.T) {
	xml := "<devices>\n    <sound model='ich9'/>\n    <disk type='file' device='disk'/>\n  </devices>"
	got := stripSoundCardXML(xml)
	if strings.Contains(got, "<sound") {
		t.Fatalf("声卡未被移除：\n%s", got)
	}
	if !strings.Contains(got, "<disk type='file' device='disk'/>") {
		t.Fatalf("移除声卡时误删了其它设备：\n%s", got)
	}

	plain := "<devices>\n    <disk type='file' device='disk'/>\n  </devices>"
	if stripSoundCardXML(plain) != plain {
		t.Fatalf("没有声卡时应原样返回")
	}
}
