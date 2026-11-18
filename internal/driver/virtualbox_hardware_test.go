package driver

import (
	"strings"
	"testing"
)

// TestVBoxAudioCommands 声卡参数必须同时兼容 VirtualBox 7.x 与 6.x。
//
// 7.x 改名为 --audio-driver/--audio-enabled，6.x 仍是 --audio/--audioenabled；
// 只下发一套会让另一版本 modifyvm 直接报未知参数。
func TestVBoxAudioCommands(t *testing.T) {
	cmds := vboxAudioCommands("vm-1")
	if len(cmds) < 2 {
		t.Fatalf("应提供 7.x 与 6.x 两套声卡参数，实际 %d 套", len(cmds))
	}

	v7 := strings.Join(cmds[0], " ")
	for _, want := range []string{"--audio-driver default", "--audio-controller hda", "--audio-enabled on"} {
		if !strings.Contains(v7, want) {
			t.Fatalf("7.x 声卡参数缺少 %s：%s", want, v7)
		}
	}

	v6 := strings.Join(cmds[1], " ")
	for _, want := range []string{"--audio default", "--audiocontroller hda", "--audioenabled on"} {
		if !strings.Contains(v6, want) {
			t.Fatalf("6.x 声卡参数缺少 %s：%s", want, v6)
		}
	}
}

// TestVBoxUSBControllerArgs USB 直通（usbfilter）前必须启用 USB 控制器。
func TestVBoxUSBControllerArgs(t *testing.T) {
	args := strings.Join(vboxUSBControllerArgs("vm-1"), " ")
	for _, want := range []string{"--usb on", "--usb-xhci on"} {
		if !strings.Contains(args, want) {
			t.Fatalf("USB 控制器参数缺少 %s：%s", want, args)
		}
	}
}
