package driver

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"
)

// Proxmox 的 qm screendump 与 QEMU 的 virsh screenshot 都输出 P6 PPM，
// 转换结果必须是不带 data: 前缀的纯 base64（前端统一拼接前缀）。
func TestPPMBytesToBase64PNG(t *testing.T) {
	// 2x1 像素：红、绿
	ppm := append([]byte("P6\n2 1\n255\n"),
		0xFF, 0x00, 0x00,
		0x00, 0xFF, 0x00)

	b64, err := ppmBytesToBase64PNG(ppm)
	if err != nil {
		t.Fatalf("PPM 转换失败: %v", err)
	}
	if strings.HasPrefix(b64, "data:") {
		t.Fatalf("应返回纯 base64，实际带 data: 前缀: %q", b64)
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("输出不是合法 base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("输出不是合法 PNG: %v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 1 {
		t.Errorf("尺寸应为 2x1，实际 %v", img.Bounds().Size())
	}
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 != 0xFF || g>>8 != 0 || b>>8 != 0 {
		t.Errorf("第一个像素应为红色，实际 (%d,%d,%d)", r>>8, g>>8, b>>8)
	}
}

// 回归：大尺寸 PPM（超过 bufio 默认 4KB 缓冲）必须完整解析。
// 原实现用 bufio.Reader.Buffered() 推算头部长度，会把 3MB 的画面截成 4080 字节。
func TestPPMBytesToBase64PNGLargeImage(t *testing.T) {
	const w, h = 64, 64
	header := []byte("P6\n64 64\n255\n")
	pixels := make([]byte, w*h*3)
	for i := range pixels {
		pixels[i] = byte(i % 251)
	}
	raw := append(header, pixels...)

	b64, err := ppmBytesToBase64PNG(raw)
	if err != nil {
		t.Fatalf("大尺寸 PPM 转换失败: %v", err)
	}
	bin, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("输出不是合法 base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(bin))
	if err != nil {
		t.Fatalf("输出不是合法 PNG: %v", err)
	}
	if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Errorf("尺寸应为 %dx%d，实际 %v", w, h, img.Bounds().Size())
	}
}

func TestPPMBytesToBase64PNGRejectsInvalid(t *testing.T) {
	cases := map[string][]byte{
		"非P6":   []byte("P3\n1 1\n255\n"),
		"像素不足":  []byte("P6\n2 2\n255\n\x00\x00\x00"),
		"非法宽度":  []byte("P6\n-1 1\n255\n"),
		"不支持灰度": []byte("P6\n1 1\n65535\n"),
	}
	for name, in := range cases {
		if _, err := ppmBytesToBase64PNG(in); err == nil {
			t.Errorf("%s: 应返回错误", name)
		}
	}
}
