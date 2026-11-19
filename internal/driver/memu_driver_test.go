package driver

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"io"
	"net"
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

// 颜色数超过 16 的瓦片必须回退成 Raw 子编码，
// 否则每个像素都要线性扫描整张颜色表，单帧编码会慢到几百毫秒。
func TestHextileEncodeManyColorsFallsBackToRaw(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			i := y*img.Stride + x*4
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(x*16), byte(y*16), 0, 255
		}
	}
	data := hextileEncode(img, defaultRFBPixelFormat)
	if data[0] != 0x01 {
		t.Fatalf("subencoding = %#x, want 0x01(Raw)", data[0])
	}
	if want := 1 + 16*16*4; len(data) != want {
		t.Errorf("Raw 瓦片长度 = %d, want %d", len(data), want)
	}
}

// 矩形编码必须与「按矩形裁剪后整块编码」逐字节一致，
// 否则增量更新发出去的局部区域会和整屏渲染错位。
func TestHextileEncodeRectMatchesCrop(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 48, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 48; x++ {
			i := y*full.Stride + x*4
			if x >= 16 && x < 32 {
				full.Pix[i], full.Pix[i+1], full.Pix[i+2] = 200, 30, 40
			} else {
				full.Pix[i], full.Pix[i+1], full.Pix[i+2] = 10, 20, 30
			}
			full.Pix[i+3] = 255
		}
	}

	crop := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		copy(crop.Pix[y*crop.Stride:y*crop.Stride+64],
			full.Pix[(y+8)*full.Stride+16*4:(y+8)*full.Stride+16*4+64])
	}

	got := hextileEncodeRect(full, 16, 8, 16, 16, defaultRFBPixelFormat)
	want := hextileEncode(crop, defaultRFBPixelFormat)
	if !bytes.Equal(got, want) {
		t.Errorf("矩形编码与裁剪编码不一致：\n got=% x\nwant=% x", got, want)
	}
}

func TestDecodeRawScreencapRGBA(t *testing.T) {
	const w, h = 20, 16
	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header[0:], w)
	binary.LittleEndian.PutUint32(header[4:], h)
	binary.LittleEndian.PutUint32(header[8:], 1) // RGBA_8888

	pix := make([]byte, w*h*4)
	// 左上角红色；右下角蓝色。alpha 故意留 0，解码后应被补成不透明。
	pix[0], pix[1], pix[2] = 255, 0, 0
	last := len(pix) - 4
	pix[last+2] = 255

	data := append([]byte("already connected to 127.0.0.1:21503\r\n"), header...)
	data = append(data, pix...)

	img, err := decodeRawScreencap(data)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if got := img.Bounds(); got.Dx() != w || got.Dy() != h {
		t.Fatalf("尺寸 = %v, want %dx%d", got, w, h)
	}
	if r, g, b, a := img.At(0, 0).RGBA(); r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Errorf("左上角像素 = (%d,%d,%d,%d)", r>>8, g>>8, b>>8, a>>8)
	}
	if _, _, b, a := img.At(w-1, h-1).RGBA(); b>>8 != 255 || a>>8 != 255 {
		t.Errorf("右下角像素蓝色 = %d, alpha = %d", b>>8, a>>8)
	}

	// 部分 Android 版本会多写 4 字节颜色空间
	header16 := make([]byte, 16)
	copy(header16, header)
	withSpace := append(header16, pix...)
	if _, err := decodeRawScreencap(withSpace); err != nil {
		t.Errorf("16 字节头部解码失败: %v", err)
	}
}

func TestDecodeRawScreencapFormats(t *testing.T) {
	const w, h = 16, 16
	newHeader := func(format uint32) []byte {
		header := make([]byte, 12)
		binary.LittleEndian.PutUint32(header[0:], w)
		binary.LittleEndian.PutUint32(header[4:], h)
		binary.LittleEndian.PutUint32(header[8:], format)
		return header
	}

	t.Run("RGB_565", func(t *testing.T) {
		pix := make([]byte, w*h*2)
		// 纯白：0xFFFF
		for i := 0; i < len(pix); i += 2 {
			pix[i], pix[i+1] = 0xff, 0xff
		}
		img, err := decodeRawScreencap(append(newHeader(4), pix...))
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		if r, g, b, a := img.At(3, 4).RGBA(); r>>8 != 255 || g>>8 != 255 || b>>8 != 255 || a>>8 != 255 {
			t.Errorf("白色像素 = (%d,%d,%d,%d)", r>>8, g>>8, b>>8, a>>8)
		}
	})

	t.Run("RGB_888", func(t *testing.T) {
		pix := make([]byte, w*h*3)
		for i := 0; i < len(pix); i += 3 {
			pix[i], pix[i+1], pix[i+2] = 0x11, 0x22, 0x33
		}
		img, err := decodeRawScreencap(append(newHeader(3), pix...))
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		if r, g, b, _ := img.At(0, 0).RGBA(); r>>8 != 0x11 || g>>8 != 0x22 || b>>8 != 0x33 {
			t.Errorf("像素 = (%#x,%#x,%#x)", r>>8, g>>8, b>>8)
		}
	})

	t.Run("BGRA_8888", func(t *testing.T) {
		pix := make([]byte, w*h*4)
		for i := 0; i < len(pix); i += 4 {
			pix[i], pix[i+1], pix[i+2] = 0x33, 0x22, 0x11 // B,G,R
		}
		img, err := decodeRawScreencap(append(newHeader(5), pix...))
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		if r, g, b, _ := img.At(0, 1).RGBA(); r>>8 != 0x11 || g>>8 != 0x22 || b>>8 != 0x33 {
			t.Errorf("像素 = (%#x,%#x,%#x)", r>>8, g>>8, b>>8)
		}
	})
}

func TestDecodeRawScreencapRejectsGarbage(t *testing.T) {
	if _, err := decodeRawScreencap([]byte("error: device offline")); err == nil {
		t.Error("非法数据不应解码成功")
	}
	if _, err := decodeRawScreencap(nil); err == nil {
		t.Error("空数据不应解码成功")
	}
}

func TestDiffRects(t *testing.T) {
	prev := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for i := 0; i < len(prev.Pix); i++ {
		prev.Pix[i] = 7
	}

	if rects := diffRects(prev, prev); len(rects) != 0 {
		t.Fatalf("完全相同的画面不应有脏矩形，实际 %v", rects)
	}

	cur := image.NewRGBA(image.Rect(0, 0, 64, 40))
	copy(cur.Pix, prev.Pix)
	// 只改 (20,20) 一个像素：应只命中所属的 16x16 瓦片
	cur.Pix[20*cur.Stride+20*4] = 9

	rects := diffRects(prev, cur)
	if len(rects) != 1 {
		t.Fatalf("脏矩形数量 = %d, want 1（%v）", len(rects), rects)
	}
	if want := [4]int{16, 16, 16, 16}; rects[0] != want {
		t.Errorf("脏矩形 = %v, want %v", rects[0], want)
	}

	// 没有上一帧时必须整屏重发
	if rects := diffRects(nil, cur); len(rects) != 1 || rects[0][2] != 64 || rects[0][3] != 40 {
		t.Errorf("首帧应整屏重发，实际 %v", rects)
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

// capture 跑一次发送函数并取回它写出的全部字节。
func captureVNCWrite(t *testing.T, fn func(cc *memuVNCConn) error) (*memuVNCConn, []byte) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()

	cc := &memuVNCConn{conn: server, pf: defaultRFBPixelFormat, hextile: true}
	go func() {
		defer server.Close()
		if err := fn(cc); err != nil {
			t.Errorf("发送失败: %v", err)
		}
	}()

	msg, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("读取报文失败: %v", err)
	}
	return cc, msg
}

// 增量更新必须只包含脏矩形，且矩形的坐标/尺寸与 diffRects 一致。
func TestVNCIncrementalUpdateFraming(t *testing.T) {
	prev := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(prev.Pix); i++ {
		prev.Pix[i] = 40
	}
	cur := image.NewRGBA(image.Rect(0, 0, 32, 32))
	copy(cur.Pix, prev.Pix)
	cur.Pix[20*cur.Stride+20*4] = 200

	cc, msg := captureVNCWrite(t, func(cc *memuVNCConn) error {
		cc.lastFrame, cc.lastW, cc.lastH = prev, 32, 32
		return cc.writeIncrementalUpdate(cur, 2, defaultRFBPixelFormat)
	})

	if len(msg) < 16 {
		t.Fatalf("报文过短: %d 字节", len(msg))
	}
	if n := binary.BigEndian.Uint16(msg[2:4]); n != 1 {
		t.Fatalf("矩形数量 = %d, want 1", n)
	}
	got := [5]int{
		int(binary.BigEndian.Uint16(msg[4:6])),
		int(binary.BigEndian.Uint16(msg[6:8])),
		int(binary.BigEndian.Uint16(msg[8:10])),
		int(binary.BigEndian.Uint16(msg[10:12])),
		int(int32(binary.BigEndian.Uint32(msg[12:16]))),
	}
	if want := [5]int{16, 16, 16, 16, 5}; got != want {
		t.Errorf("矩形 = %v, want %v（x,y,w,h,encoding）", got, want)
	}
	if cc.sentSeq != 2 || cc.lastFrame != cur {
		t.Error("发送后未记录已发送版本")
	}
}

// 尺寸变化时必须先发 DesktopSize 伪矩形，再发整屏像素。
func TestVNCFullUpdateFraming(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 9, 8, 7, 255
	}

	cc, msg := captureVNCWrite(t, func(cc *memuVNCConn) error {
		return cc.writeFullUpdate(img, 7, defaultRFBPixelFormat, true, false, true, true)
	})

	if len(msg) < 4+12+12 {
		t.Fatalf("报文过短: %d 字节", len(msg))
	}
	if n := binary.BigEndian.Uint16(msg[2:4]); n != 2 {
		t.Fatalf("矩形数量 = %d, want 2", n)
	}
	if enc := int32(binary.BigEndian.Uint32(msg[12:16])); enc != -223 {
		t.Errorf("第一个矩形编码 = %d, want -223(DesktopSize)", enc)
	}
	if enc := int32(binary.BigEndian.Uint32(msg[4+12+8 : 4+12+12])); enc != 5 {
		t.Errorf("第二个矩形编码 = %d, want 5(Hextile)", enc)
	}
	if cc.sentSeq != 7 {
		t.Errorf("sentSeq = %d, want 7", cc.sentSeq)
	}
}
