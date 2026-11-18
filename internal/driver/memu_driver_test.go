package driver

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestMemuAndroidCode(t *testing.T) {
	cases := map[string]string{
		"Android 7.1":       "71",
		"Android 7.1 (64位)": "76",
		"Android 9":         "90",
		"Android 12":        "120",
		"71":                "71",
		"76":                "76",
		"120":               "120",
		"android7.1":        "71",
		"android7.1_64":     "76",
		"  android 5.1  ":   "51",
		"unknown":           "",
		"":                  "",
	}
	for input, want := range cases {
		if got := memuAndroidCode(input); got != want {
			t.Errorf("memuAndroidCode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDecodeMemucOutputGBK(t *testing.T) {
	gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte("逍遥模拟器1"))
	if err != nil {
		t.Fatalf("构造 GBK 数据失败: %v", err)
	}
	got := decodeMemucOutput(string(gbk))
	if got != "逍遥模拟器1" {
		t.Errorf("decodeMemucOutput = %q, want 逍遥模拟器1", got)
	}
	// 纯 ASCII / 合法 UTF-8 不应被改写
	if got := decodeMemucOutput("SUCCESS: start vm finished."); got != "SUCCESS: start vm finished." {
		t.Errorf("ASCII 被改写: %q", got)
	}
	if got := decodeMemucOutput("运行中"); got != "运行中" {
		t.Errorf("UTF-8 被改写: %q", got)
	}
}

func TestMemuOutputError(t *testing.T) {
	okOutputs := []string{
		"SUCCESS: start vm finished.\r\n",
		"Running\r\n",
		"Not Running\r\n",
		"already connected to 127.0.0.1:21503\r\n\r\n",
	}
	for _, out := range okOutputs {
		if msg := memuOutputError(out); msg != "" {
			t.Errorf("误判为错误: %q -> %q", out, msg)
		}
	}
	if msg := memuOutputError("ERROR: The core process of emulator has ended.\r\n"); msg == "" {
		t.Error("未识别 start 的 ERROR 输出")
	}
	if msg := memuOutputError("already connected to 127.0.0.1:21503\r\nerror: device offline\r\n"); msg == "" {
		t.Error("未识别 adb 的 error 输出")
	}
}

func TestMemuArrowCursor(t *testing.T) {
	w, h, hotX, hotY, pixels, mask := memuArrowCursor()
	if w != 24 || h != 24 || hotX != 0 || hotY != 0 {
		t.Fatalf("光标尺寸/热点 = %dx%d (%d,%d)", w, h, hotX, hotY)
	}
	if len(pixels) != w*h*4 {
		t.Fatalf("像素长度 = %d, want %d", len(pixels), w*h*4)
	}
	if len(mask) != ((w+7)/8)*h {
		t.Fatalf("掩码长度 = %d, want %d", len(mask), ((w+7)/8)*h)
	}
	// 热点（箭头尖端）必须可见
	if mask[0]&0x80 == 0 {
		t.Error("箭头尖端未设置可见掩码")
	}
	// 右下角远离箭头，必须透明
	if mask[(h-1)*((w+7)/8)+(w-1)/8]&(0x80>>uint((w-1)%8)) != 0 {
		t.Error("右下角不应可见")
	}
}

func TestMemuIsRunningText(t *testing.T) {
	cases := map[string]bool{
		"Running\r\n":     true,
		"Not Running\r\n": false,
		"not running":     false,
		"":                false,
		"unknown":         false,
	}
	for input, want := range cases {
		if got := memuIsRunningText(input); got != want {
			t.Errorf("memuIsRunningText(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestParseMemucListvms(t *testing.T) {
	out := "0,逍遥模拟器,594628,1,50944\r\n" +
		"1,逍遥模拟器1,2688462,0,55424\r\n" +
		"\r\n"
	instances := parseMemuListvms(out)
	if len(instances) != 2 {
		t.Fatalf("解析数量 = %d, want 2", len(instances))
	}
	if instances[0].Index != "0" || instances[0].Title != "逍遥模拟器" || !instances[0].Running {
		t.Errorf("第 1 条解析错误: %+v", instances[0])
	}
	if instances[1].Index != "1" || instances[1].Running {
		t.Errorf("第 2 条解析错误: %+v", instances[1])
	}
	if instances[0].PID != "50944" {
		t.Errorf("PID = %q", instances[0].PID)
	}
}

func TestRFBPixelFormatCPixel(t *testing.T) {
	pf := defaultRFBPixelFormat
	if got := pf.cpixelSize(); got != 3 {
		t.Fatalf("cpixelSize = %d, want 3", got)
	}

	// 小端 32bpp：内存字节序为 B,G,R,0，CPIXEL 取前 3 字节
	buf := make([]byte, 4)
	n := pf.writeCPixel(buf, 0x11, 0x22, 0x33)
	if n != 3 {
		t.Fatalf("writeCPixel n = %d", n)
	}
	if buf[0] != 0x33 || buf[1] != 0x22 || buf[2] != 0x11 {
		t.Errorf("CPIXEL = % x, want 33 22 11", buf[:3])
	}

	be := pf
	be.bigEndian = 1
	if n := be.writeCPixel(buf, 0x11, 0x22, 0x33); n != 3 {
		t.Fatalf("BE writeCPixel n = %d", n)
	}
	if buf[0] != 0x11 || buf[1] != 0x22 || buf[2] != 0x33 {
		t.Errorf("BE CPIXEL = % x, want 11 22 33", buf[:3])
	}
}

func TestZRLEEncodeRoundTrip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = 255, 0, 0, 255
	img.Pix[4], img.Pix[5], img.Pix[6], img.Pix[7] = 0, 255, 0, 255
	img.Pix[8], img.Pix[9], img.Pix[10], img.Pix[11] = 0, 0, 255, 255
	img.Pix[12], img.Pix[13], img.Pix[14], img.Pix[15] = 255, 255, 255, 255

	pf := defaultRFBPixelFormat
	encoded := zrleEncode(img, pf)
	if len(encoded) < 5 {
		t.Fatalf("ZRLE 数据过短: %d", len(encoded))
	}
	length := binary.BigEndian.Uint32(encoded[:4])
	if int(length) != len(encoded)-4 {
		t.Fatalf("ZRLE 长度前缀 = %d, 实际 = %d", length, len(encoded)-4)
	}

	zr, err := zlib.NewReader(bytes.NewReader(encoded[4:]))
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	raw, err := io.ReadAll(zr)
	_ = zr.Close()
	if err != nil {
		t.Fatalf("读取解压数据失败: %v", err)
	}
	// 一个瓦片：1 字节子编码 + 4 像素 × 3 字节
	if len(raw) != 1+4*3 {
		t.Fatalf("解压长度 = %d, want %d", len(raw), 1+4*3)
	}
	if raw[0] != 0 {
		t.Errorf("子编码 = %d, want 0", raw[0])
	}
	want := []byte{0x00, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0x00, 0xFF, 0xFF, 0xFF}
	if !bytes.Equal(raw[1:], want) {
		t.Errorf("像素数据 = % x, want % x", raw[1:], want)
	}
}

func TestHextileEncodeSolidTile(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 10, 20, 30, 255
	}
	pf := defaultRFBPixelFormat
	data := hextileEncode(img, pf)
	if len(data) != 1+4 {
		t.Fatalf("纯色瓦片长度 = %d, want 5", len(data))
	}
	if data[0] != 0x02 {
		t.Errorf("subencoding = %#x, want 0x02", data[0])
	}
	// 小端 32bpp：B,G,R,0
	if data[1] != 30 || data[2] != 20 || data[3] != 10 {
		t.Errorf("背景色 = % x, want 1e 14 0a", data[1:4])
	}
}

func TestHextileEncodeSubrect(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0, 0, 0, 255
	}
	// 第 3 行第 2 列起 4 个红色像素
	for x := 2; x < 6; x++ {
		i := 3*img.Stride + x*4
		img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 255, 0, 0
	}
	data := hextileEncode(img, defaultRFBPixelFormat)
	if len(data) < 2 {
		t.Fatal("Hextile 数据过短")
	}
	if data[0] != 0x1A { // 0x02|0x08|0x10
		t.Fatalf("subencoding = %#x, want 0x1A", data[0])
	}
	if data[5] != 1 {
		t.Fatalf("子矩形数量 = %d, want 1", data[5])
	}
	// 子矩形：像素(4B) + x/y(1B) + w/h(1B)
	if data[10] != 0x23 { // x=2, y=3
		t.Errorf("子矩形坐标 = %#x, want 0x23", data[10])
	}
	if data[11] != 0x30 { // w=4 → (4-1)<<4 = 0x30
		t.Errorf("子矩形尺寸 = %#x, want 0x30", data[11])
	}
}

func TestAndroidInputText(t *testing.T) {
	if got := androidInputText("hello world"); got != "hello%sworld" {
		t.Errorf("空格转义 = %q", got)
	}
	if got := androidInputText("a;rm -rf /"); strings.Contains(got, ";") {
		t.Errorf("危险字符未过滤: %q", got)
	}
}
