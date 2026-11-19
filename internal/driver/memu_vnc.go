// Package driver MEmu 图形控制台（内置 RFB/VNC 服务）。
//
// 逍遥模拟器本身只提供 ADB，没有 VNC 服务；HostAgent 在这里启动一个
// 仅监听 127.0.0.1 的 RFB 3.8 服务：
//
//   - 画面：常驻抓帧协程用 `memuc adb exec-out screencap`（不带 -p，直接取
//     原始帧缓冲）取帧。设备不做 PNG 压缩，单帧从约 1 秒降到 100ms 量级；
//     画面没有变化时不推送，有变化时只编码发生变化的 16x16 瓦片。
//   - 输入：按下/拖动/抬起转成 Android 触摸（设备支持 `input motionevent`
//     时注入连续的 DOWN/MOVE/UP，否则退化为分段 `input swipe`），
//     键盘按键转成 `input keyevent`，可打印字符合并后走 `input text`。
//
// 前端 noVNC 仍然连接 6080 的 websockify 代理，由代理转发到这里的动态端口，
// 因此对浏览器/前端完全透明，不需要额外插件。
package driver

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// ============================================================
// RFB 像素格式与编码
// ============================================================

// rfbPixelFormat RFB 像素格式（RFC 6143 §7.4）
type rfbPixelFormat struct {
	bpp        uint8
	depth      uint8
	bigEndian  uint8
	trueColor  uint8
	redMax     uint16
	greenMax   uint16
	blueMax    uint16
	redShift   uint8
	greenShift uint8
	blueShift  uint8
}

// defaultRFBPixelFormat 与 noVNC 默认格式一致，避免客户端 SetPixelFormat 后错位
var defaultRFBPixelFormat = rfbPixelFormat{
	bpp: 32, depth: 24, bigEndian: 0, trueColor: 1,
	redMax: 255, greenMax: 255, blueMax: 255,
	redShift: 16, greenShift: 8, blueShift: 0,
}

func parseRFBPixelFormat(b []byte) rfbPixelFormat {
	if len(b) < 16 {
		return defaultRFBPixelFormat
	}
	return rfbPixelFormat{
		bpp:        b[0],
		depth:      b[1],
		bigEndian:  b[2],
		trueColor:  b[3],
		redMax:     binary.BigEndian.Uint16(b[4:6]),
		greenMax:   binary.BigEndian.Uint16(b[6:8]),
		blueMax:    binary.BigEndian.Uint16(b[8:10]),
		redShift:   b[10],
		greenShift: b[11],
		blueShift:  b[12],
	}
}

func (pf rfbPixelFormat) bppBytes() int {
	switch pf.bpp {
	case 8:
		return 1
	case 16:
		return 2
	default:
		return 4
	}
}

// word 按客户端格式把 RGB 组成像素值
func (pf rfbPixelFormat) word(r, g, b uint8) uint32 {
	rv := uint32(r) * uint32(pf.redMax) / 255
	gv := uint32(g) * uint32(pf.greenMax) / 255
	bv := uint32(b) * uint32(pf.blueMax) / 255
	return rv<<pf.redShift | gv<<pf.greenShift | bv<<pf.blueShift
}

// writePixel 按客户端字节序写入完整像素
func (pf rfbPixelFormat) writePixel(dst []byte, r, g, b uint8) int {
	v := pf.word(r, g, b)
	switch pf.bpp {
	case 8:
		if len(dst) < 1 {
			return 0
		}
		dst[0] = byte(v)
		return 1
	case 16:
		if len(dst) < 2 {
			return 0
		}
		if pf.bigEndian != 0 {
			dst[0], dst[1] = byte(v>>8), byte(v)
		} else {
			dst[0], dst[1] = byte(v), byte(v>>8)
		}
		return 2
	default:
		if len(dst) < 4 {
			return 0
		}
		if pf.bigEndian != 0 {
			dst[0], dst[1], dst[2], dst[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
		} else {
			dst[0], dst[1], dst[2], dst[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
		}
		return 4
	}
}

// cpixelSize ZRLE 的压缩像素尺寸（RFC 6143 §7.7.6）：
// 32bpp 且深度不超过 24 位、颜色位全部落在同一侧 3 字节时按 3 字节传输。
func (pf rfbPixelFormat) cpixelSize() int {
	if pf.bpp == 32 && pf.depth <= 24 {
		max := uint32(pf.redMax)<<pf.redShift |
			uint32(pf.greenMax)<<pf.greenShift |
			uint32(pf.blueMax)<<pf.blueShift
		if max <= 0xFFFFFF {
			return 3
		}
	}
	return pf.bppBytes()
}

func (pf rfbPixelFormat) writeCPixel(dst []byte, r, g, b uint8) int {
	if pf.cpixelSize() == 3 {
		var tmp [4]byte
		pf.writePixel(tmp[:], r, g, b)
		if pf.bigEndian != 0 {
			copy(dst, tmp[1:4])
		} else {
			copy(dst, tmp[0:3])
		}
		return 3
	}
	return pf.writePixel(dst, r, g, b)
}

// ============================================================
// 服务生命周期
// ============================================================

const (
	// memuVNCFrameInterval 两次抓帧的最小间隔（约 12 fps 上限）。
	// 实际帧率由 adb screencap 的耗时决定，这里只防止无谓的高频抓帧。
	memuVNCFrameInterval = 80 * time.Millisecond
	// memuVNCIdleInterval 控制台关闭（没有客户端）时的抓帧间隔。
	memuVNCIdleInterval = 400 * time.Millisecond
	// memuVNCUpdateWait 增量请求等待“下一帧”的最长时间。
	// 画面静止时不能立刻回空更新，否则 noVNC 会以网络往返速度空转。
	memuVNCUpdateWait = 250 * time.Millisecond
	// memuVNCMaxRects 单次更新最多发送的矩形数，碎片过多时整屏重发更划算。
	memuVNCMaxRects = 96
	// memuVNCTile 差异比较与 Hextile 编码的瓦片边长。
	memuVNCTile = 16
)

var (
	memuVNCMu      sync.Mutex
	memuVNCServers = map[string]*memuVNCServer{}
)

// memuVNCServer 单个实例的 RFB 服务
type memuVNCServer struct {
	d    *MemuDriver
	idx  string
	ln   net.Listener
	port int

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool

	stopCh   chan struct{}
	stopOnce sync.Once

	// 最新画面。frameSeq 只在画面内容真正变化时递增；
	// updated 在每次更新时关闭并替换，用于广播唤醒等待新画面的连接。
	frameMu  sync.Mutex
	frame    *image.RGBA
	frameSeq uint64
	updated  chan struct{}

	// retryAt 抓帧失败后的退避时间，避免对已关机的实例高频拉起 adb
	retryAt time.Time
	lastErr string

	// rawFail/rawOff：原始截屏连续失败后固定退回 PNG 抓帧
	rawFail int
	rawOff  bool

	// motionMode input motionevent 支持情况：0 未知，1 支持，-1 不支持
	motionMode int32

	// lastRequest 最近一次画面请求的时间（unix nano），用于在
	// 浏览器切到后台后降低抓帧频率
	lastRequest int64
}

func (d *MemuDriver) vncKey(idx string) string {
	host := ""
	if d.Config != nil {
		host = d.Config.ServerName
	}
	return host + "\x00" + idx
}

// ensureVNCServer 按需启动实例的 VNC 服务
func (d *MemuDriver) ensureVNCServer(idx string) (*memuVNCServer, error) {
	key := d.vncKey(idx)

	memuVNCMu.Lock()
	defer memuVNCMu.Unlock()

	if s, ok := memuVNCServers[key]; ok && !s.closed {
		return s, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &memuVNCServer{
		d:       d,
		idx:     idx,
		ln:      ln,
		port:    ln.Addr().(*net.TCPAddr).Port,
		conns:   map[net.Conn]struct{}{},
		stopCh:  make(chan struct{}),
		updated: make(chan struct{}),
	}
	memuVNCServers[key] = s
	go s.serve()
	go s.captureLoop()
	return s, nil
}

// Port 返回监听端口
func (s *memuVNCServer) Port() int { return s.port }

// closeVNCServer 关闭指定实例的 VNC 服务
func (d *MemuDriver) closeVNCServer(idx string) {
	key := d.vncKey(idx)
	memuVNCMu.Lock()
	s, ok := memuVNCServers[key]
	if ok {
		delete(memuVNCServers, key)
	}
	memuVNCMu.Unlock()
	if ok {
		s.Stop()
	}
}

// closeAllVNCServers 关闭本驱动创建的所有 VNC 服务
func (d *MemuDriver) closeAllVNCServers() {
	memuVNCMu.Lock()
	var list []*memuVNCServer
	for key, s := range memuVNCServers {
		if s.d == d {
			delete(memuVNCServers, key)
			list = append(list, s)
		}
	}
	memuVNCMu.Unlock()
	for _, s := range list {
		s.Stop()
	}
}

// Stop 关闭监听与所有连接
func (s *memuVNCServer) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	s.stopOnce.Do(func() { close(s.stopCh) })
	_ = s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *memuVNCServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *memuVNCServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		go func(conn net.Conn) {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			s.handleConn(conn)
		}(c)
	}
}

// ============================================================
// 帧来源：常驻抓帧协程
// ============================================================

// captureLoop 持续抓帧，与客户端请求解耦。
//
// 旧实现每收到一次 FramebufferUpdateRequest 才同步抓一帧，
// 抓帧（PNG 约 1 秒）与编码串在连接读循环里，既决定了帧率上限，
// 也让鼠标/键盘事件排队等一帧，表现就是「画面一顿一顿、滑动没反应」。
func (s *memuVNCServer) captureLoop() {
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		if s.connCount() == 0 {
			if !sleepOrStop(s.stopCh, memuVNCIdleInterval) {
				return
			}
			continue
		}

		// 浏览器切到后台后 noVNC 不再请求更新，这时没必要按 12fps 抓帧
		interval := memuVNCFrameInterval
		if time.Since(time.Unix(0, atomic.LoadInt64(&s.lastRequest))) > 2*time.Second {
			interval = memuVNCIdleInterval
		}

		start := time.Now()
		s.captureOnce()
		if wait := interval - time.Since(start); wait > 0 {
			if !sleepOrStop(s.stopCh, wait) {
				return
			}
		}
	}
}

func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func (s *memuVNCServer) captureOnce() {
	s.frameMu.Lock()
	retryAt := s.retryAt
	s.frameMu.Unlock()
	if !retryAt.IsZero() && time.Now().Before(retryAt) {
		return
	}

	img, err := s.grabFrame()
	if err != nil {
		msg := err.Error()
		s.frameMu.Lock()
		changed := msg != s.lastErr
		s.lastErr = msg
		s.retryAt = time.Now().Add(2 * time.Second)
		s.frameMu.Unlock()
		// 只在错误内容变化时打日志：控制台黑屏时这里能说明原因
		if changed {
			log.Printf("[memu-vnc] 实例 #%s 抓帧失败（控制台将停留在上一帧）：%s", s.idx, msg)
		}
		return
	}

	s.frameMu.Lock()
	s.retryAt = time.Time{}
	s.lastErr = ""
	prev := s.frame
	// 画面没变化就不递增版本：客户端只会收到空更新，不会重传整屏
	if prev != nil && prev.Bounds() == img.Bounds() && bytes.Equal(prev.Pix, img.Pix) {
		s.frameMu.Unlock()
		return
	}
	s.frame = img
	s.frameSeq++
	close(s.updated)
	s.updated = make(chan struct{})
	s.frameMu.Unlock()
}

// grabFrame 抓取一帧：优先原始帧缓冲，失败后回退 PNG。
func (s *memuVNCServer) grabFrame() (*image.RGBA, error) {
	s.frameMu.Lock()
	rawOff := s.rawOff
	s.frameMu.Unlock()

	var rawErr error
	if !rawOff {
		raw, err := s.d.captureMemuRawFrame(s.idx)
		if err == nil {
			img, decErr := decodeRawScreencap(raw)
			if decErr == nil {
				s.frameMu.Lock()
				s.rawFail = 0
				s.frameMu.Unlock()
				return img, nil
			}
			err = decErr
		}
		rawErr = err
		s.noteRawFailure(err)
	}

	data, err := s.d.captureMemuFrame(s.idx)
	if err != nil {
		if rawErr != nil {
			return nil, fmt.Errorf("原始截屏失败（%v），PNG 截图也失败：%w", rawErr, err)
		}
		return nil, err
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return toRGBAImage(decoded), nil
}

// noteRawFailure 累计原始截屏失败次数，连续失败后固定使用 PNG。
func (s *memuVNCServer) noteRawFailure(err error) {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	s.rawFail++
	if s.rawFail >= 3 && !s.rawOff {
		s.rawOff = true
		log.Printf("[memu-vnc] 实例 #%s 原始截屏不可用，改用 PNG 抓帧：%v", s.idx, err)
	}
}

func (s *memuVNCServer) snapshot() (*image.RGBA, uint64) {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	return s.frame, s.frameSeq
}

// currentFrame 返回最新画面；握手阶段还没有画面时同步抓一帧。
func (s *memuVNCServer) currentFrame() *image.RGBA {
	if img, _ := s.snapshot(); img != nil {
		return img
	}
	if img, err := s.grabFrame(); err == nil {
		s.frameMu.Lock()
		if s.frame == nil {
			s.frame = img
			s.frameSeq++
			close(s.updated)
			s.updated = make(chan struct{})
		}
		img = s.frame
		s.frameMu.Unlock()
		return img
	}
	return placeholderFrame(1280, 720)
}

// waitFrame 等待比 after 更新的画面，超时返回当前画面。
func (s *memuVNCServer) waitFrame(after uint64, timeout time.Duration) (*image.RGBA, uint64) {
	deadline := time.Now().Add(timeout)
	for {
		s.frameMu.Lock()
		img, seq, ch := s.frame, s.frameSeq, s.updated
		s.frameMu.Unlock()
		if img != nil && seq != after {
			return img, seq
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return img, seq
		}
		t := time.NewTimer(remain)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
			return img, seq
		case <-s.stopCh:
			t.Stop()
			return img, seq
		}
	}
}

// placeholderFrame 抓帧失败时的占位画面（客户端仍能正常握手）
func placeholderFrame(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 18, 20, 26, 255
	}
	return img
}

// toRGBAImage 把任意图像转成 RGBA，便于按像素编码
func toRGBAImage(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok {
		return rgba
	}
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	return rgba
}

// captureMemuRawFrame 抓取一帧原始帧缓冲。
//
// `screencap -p` 要在设备上把整屏压成 PNG（1280x720 约需 1 秒），
// 去掉 -p 后设备只做一次读帧缓冲，宿主侧解析也只是一次拷贝，
// 单帧耗时降到 100ms 量级，是控制台能跑到 8~12fps 的关键。
func (d *MemuDriver) captureMemuRawFrame(vmIndex string) ([]byte, error) {
	args := append([]string{d.memucBin()}, "adb", "-i", vmIndex, "--", "exec-out", "screencap")
	out, err := security.SafeExecBytesWithTimeout(args, 20*time.Second)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// decodeRawScreencap 解析 `screencap`（不带 -p）输出的原始帧缓冲。
//
// 头部是 3 个小端 uint32：宽、高、像素格式（android::PixelFormat：
// 1=RGBA_8888，2=RGBX_8888，3=RGB_888，4=RGB_565，5=BGRA_8888），
// 后面是逐行紧排的像素数据（设备已完成 stride 裁剪）。
// 部分 Android 版本会多写 4 字节颜色空间，因此 12/16 字节头部都要试。
// `memuc adb` 可能在二进制前打印 "already connected to ..."，
// 因此在开头范围内滑动搜索与剩余长度自洽的头部，而不是假定从 0 开始。
// 尾部允许少量多余字节（个别机型会多写换行/填充），但像素数据必须完整。
func decodeRawScreencap(data []byte) (*image.RGBA, error) {
	const maxPrefix = 4096
	limit := min(len(data), maxPrefix)

	// 先按精确长度匹配，找不到再容忍 16 字节尾部冗余：
	// 宽松匹配可能把 12 字节头部误当成 16 字节头部（反之亦然），
	// 那样整帧像素会错开 4 字节。
	for _, exact := range []bool{true, false} {
		fit := func(rest, hdr, need int) bool {
			if exact {
				return rest == hdr+need
			}
			return rest >= hdr+need && rest-(hdr+need) <= 16
		}
		for off := 0; off+12 <= limit; off++ {
			w := int(binary.LittleEndian.Uint32(data[off:]))
			h := int(binary.LittleEndian.Uint32(data[off+4:]))
			format := binary.LittleEndian.Uint32(data[off+8:])
			if w < 16 || h < 16 || w > 8192 || h > 8192 {
				continue
			}

			var bpp int
			var conv func(pix []byte, w, h int) *image.RGBA
			switch format {
			case 1, 2: // RGBA_8888 / RGBX_8888
				bpp, conv = 4, rgbaFromRaw
			case 3: // RGB_888
				bpp, conv = 3, rgbaFromRGB888
			case 4: // RGB_565
				bpp, conv = 2, rgbaFromRGB565
			case 5: // BGRA_8888
				bpp, conv = 4, rgbaFromBGRA
			default: // 含 RGBA_1010102 / RGBA_F16：不支持，交给 PNG 兜底
				continue
			}

			rest := len(data) - off
			need := w * h * bpp
			for _, hdr := range []int{16, 12} {
				if fit(rest, hdr, need) {
					return conv(data[off+hdr:off+hdr+need], w, h), nil
				}
			}
		}
	}
	return nil, errors.New("原始截图数据无法解析")
}

func rgbaFromRaw(pix []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	row := w * 4
	for y := 0; y < h; y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+row], pix[y*row:(y+1)*row])
	}
	// RGBA_8888 的 alpha 不一定可靠（部分机型恒为 0），统一按不透明处理
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

// rgbaFromRGB888 转换 3 字节紧排的 RGB_888
func rgbaFromRGB888(pix []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	pos := 0
	for y := 0; y < h; y++ {
		base := y * img.Stride
		for x := 0; x < w; x++ {
			i := base + x*4
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = pix[pos], pix[pos+1], pix[pos+2]
			img.Pix[i+3] = 255
			pos += 3
		}
	}
	return img
}

// rgbaFromBGRA 转换 4 字节紧排的 BGRA_8888
func rgbaFromBGRA(pix []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	pos := 0
	for y := 0; y < h; y++ {
		base := y * img.Stride
		for x := 0; x < w; x++ {
			i := base + x*4
			img.Pix[i] = pix[pos+2]
			img.Pix[i+1] = pix[pos+1]
			img.Pix[i+2] = pix[pos]
			img.Pix[i+3] = 255
			pos += 4
		}
	}
	return img
}

func rgbaFromRGB565(pix []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	pos := 0
	for y := 0; y < h; y++ {
		base := y * img.Stride
		for x := 0; x < w; x++ {
			v := uint16(pix[pos]) | uint16(pix[pos+1])<<8
			pos += 2
			r := uint8(v>>11) & 0x1f
			g := uint8(v>>5) & 0x3f
			b := uint8(v) & 0x1f
			i := base + x*4
			img.Pix[i] = r<<3 | r>>2
			img.Pix[i+1] = g<<2 | g>>4
			img.Pix[i+2] = b<<3 | b>>2
			img.Pix[i+3] = 255
		}
	}
	return img
}

// ============================================================
// RFB 连接
// ============================================================

// memuVNCConn 单个客户端会话
type memuVNCConn struct {
	srv  *memuVNCServer
	conn net.Conn

	// fmtMu 保护协商后的像素格式与编码开关：
	// 读循环负责写入，发送协程负责读取。
	fmtMu       sync.Mutex
	pf          rfbPixelFormat
	hextile     bool
	zrle        bool
	desktopSize bool

	cursor     bool
	cursorSent bool

	// 已发送画面的尺寸与版本
	lastFrame    *image.RGBA
	lastW, lastH int
	sentSeq      uint64

	// 更新请求合并：读循环只置位 + 唤醒，编码与发送放在 sendLoop
	fbMu       sync.Mutex
	fbNeedFull bool
	fbSignal   chan struct{}
	writeMu    sync.Mutex

	// 输入队列：连续拖动的 move 事件只保留最新一条
	inMu    sync.Mutex
	inCond  *sync.Cond
	inQueue []string
	inStop  bool

	// 以下输入状态只在输入协程中读写
	motionEvents       bool
	inPressAt          time.Time
	inPressX, inPressY int
	inLastX, inLastY   int
	inDragged          bool

	// 指针按键状态，仅读循环使用
	pressed bool

	done    chan struct{}
	textBuf textBuffer
}

func (s *memuVNCServer) handleConn(conn net.Conn) {
	cc := &memuVNCConn{
		srv:      s,
		conn:     conn,
		pf:       defaultRFBPixelFormat,
		fbSignal: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	cc.inCond = sync.NewCond(&cc.inMu)

	defer func() {
		close(cc.done)
		cc.stopInput()
		cc.textBuf.close()
	}()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return
	}

	ver := make([]byte, 12)
	if _, err := io.ReadFull(conn, ver); err != nil {
		return
	}
	minor := 8
	if len(ver) >= 11 {
		if n, err := parseSmallInt(ver[8:11]); err == nil {
			minor = n
		}
	}

	if minor >= 7 {
		// 仅支持 None 认证
		if _, err := conn.Write([]byte{1, 1}); err != nil {
			return
		}
		choice := make([]byte, 1)
		if _, err := io.ReadFull(conn, choice); err != nil || choice[0] != 1 {
			return
		}
		if minor >= 8 {
			if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
				return
			}
		}
	} else {
		// RFB 3.3：服务端直接指定安全类型
		if _, err := conn.Write([]byte{0, 0, 0, 1}); err != nil {
			return
		}
	}

	shared := make([]byte, 1)
	if _, err := io.ReadFull(conn, shared); err != nil {
		return
	}

	img := s.currentFrame()
	cc.lastW, cc.lastH = img.Bounds().Dx(), img.Bounds().Dy()
	if err := cc.writeServerInit(cc.lastW, cc.lastH); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	// 探测触摸注入方式放在握手之后：它会额外跑一条 adb，不能拖慢首屏。
	// 输入协程必须先于读循环启动，读循环收到的指针事件才能立刻排队执行。
	cc.motionEvents = s.motionEventSupport()
	go cc.inputWorker()
	go cc.sendLoop()
	cc.readLoop()
}

// writeServerInit 发送 ServerInit（含像素格式与桌面名称）
func (cc *memuVNCConn) writeServerInit(w, h int) error {
	name := "MEmu Android"
	if cc.srv.d.Config != nil {
		name = "MEmu " + cc.srv.d.Config.ServerName + " #" + cc.srv.idx
	}

	pf := cc.pixelFormat()
	buf := make([]byte, 0, 24+len(name))
	buf = appendU16(buf, uint16(w))
	buf = appendU16(buf, uint16(h))
	buf = append(buf,
		pf.bpp, pf.depth, pf.bigEndian, pf.trueColor,
	)
	buf = appendU16(buf, pf.redMax)
	buf = appendU16(buf, pf.greenMax)
	buf = appendU16(buf, pf.blueMax)
	buf = append(buf, pf.redShift, pf.greenShift, pf.blueShift)
	buf = append(buf, 0, 0, 0) // padding
	buf = appendU32(buf, uint32(len(name)))
	buf = append(buf, []byte(name)...)
	return cc.writeAll(buf)
}

func (cc *memuVNCConn) pixelFormat() rfbPixelFormat {
	cc.fmtMu.Lock()
	defer cc.fmtMu.Unlock()
	return cc.pf
}

func (cc *memuVNCConn) clientFormat() (rfbPixelFormat, bool, bool, bool) {
	cc.fmtMu.Lock()
	defer cc.fmtMu.Unlock()
	return cc.pf, cc.hextile, cc.zrle, cc.desktopSize
}

// readLoop 处理客户端消息
func (cc *memuVNCConn) readLoop() {
	for {
		var typ [1]byte
		if _, err := io.ReadFull(cc.conn, typ[:]); err != nil {
			return
		}
		switch typ[0] {
		case 0: // SetPixelFormat
			b := make([]byte, 3+16)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
			cc.fmtMu.Lock()
			cc.pf = parseRFBPixelFormat(b[3:])
			cc.fmtMu.Unlock()
		case 2: // SetEncodings
			hdr := make([]byte, 3)
			if _, err := io.ReadFull(cc.conn, hdr); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(hdr[1:3]))
			data := make([]byte, n*4)
			if _, err := io.ReadFull(cc.conn, data); err != nil {
				return
			}
			cc.fmtMu.Lock()
			for i := 0; i < n; i++ {
				enc := int32(binary.BigEndian.Uint32(data[i*4 : i*4+4]))
				switch enc {
				case 5: // Hextile（首选：兼容性好，不依赖 zlib 流状态）
					cc.hextile = true
				case 16: // ZRLE
					cc.zrle = true
				case -223: // DesktopSize
					cc.desktopSize = true
				case -239: // Cursor
					cc.cursor = true
				}
			}
			sendCursor := cc.cursor && !cc.cursorSent
			if sendCursor {
				cc.cursorSent = true
			}
			cc.fmtMu.Unlock()

			// 客户端声明支持光标伪编码后立即下发一次箭头光标。
			// 不发送光标时 noVNC 会一直使用 RFB.cursors.none，
			// 表现就是「控制台里看不到鼠标」。
			if sendCursor {
				if err := cc.sendCursor(); err != nil {
					return
				}
			}
		case 3: // FramebufferUpdateRequest
			b := make([]byte, 9)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
			cc.requestUpdate(b[0] == 0)
		case 4: // KeyEvent
			b := make([]byte, 7)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
			down := b[0] != 0
			sym := binary.BigEndian.Uint32(b[3:7])
			cc.handleKey(down, sym)
		case 5: // PointerEvent
			b := make([]byte, 5)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
			mask := b[0]
			x := int(binary.BigEndian.Uint16(b[1:3]))
			y := int(binary.BigEndian.Uint16(b[3:5]))
			cc.handlePointer(mask, x, y)
		case 6: // ClientCutText
			hdr := make([]byte, 7)
			if _, err := io.ReadFull(cc.conn, hdr); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(hdr[3:7])
			if n > 1<<20 {
				return
			}
			if _, err := io.CopyN(io.Discard, cc.conn, int64(n)); err != nil {
				return
			}
		case 150: // EnableContinuousUpdates
			b := make([]byte, 9)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
		case 248: // ClientFence
			b := make([]byte, 8)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(b[4:8])
			if n > 1<<20 {
				return
			}
			if _, err := io.CopyN(io.Discard, cc.conn, int64(n)); err != nil {
				return
			}
		default:
			// 未知消息无法确定后续长度，直接断开避免协议错位
			return
		}
	}
}

// requestUpdate 登记一次更新请求（读循环不阻塞，避免输入事件排队）。
func (cc *memuVNCConn) requestUpdate(full bool) {
	atomic.StoreInt64(&cc.srv.lastRequest, time.Now().UnixNano())
	if full {
		cc.fbMu.Lock()
		cc.fbNeedFull = true
		cc.fbMu.Unlock()
	}
	select {
	case cc.fbSignal <- struct{}{}:
	default:
	}
}

// sendLoop 串行编码并发送画面更新。
func (cc *memuVNCConn) sendLoop() {
	for {
		select {
		case <-cc.done:
			return
		case <-cc.fbSignal:
			cc.fbMu.Lock()
			full := cc.fbNeedFull
			cc.fbNeedFull = false
			cc.fbMu.Unlock()
			if err := cc.sendUpdate(full); err != nil {
				_ = cc.conn.Close()
				return
			}
		}
	}
}

func (cc *memuVNCConn) sendUpdate(full bool) error {
	pf, hextile, zrle, desktopSize := cc.clientFormat()

	img, seq := cc.srv.snapshot()
	if img == nil {
		return nil
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()

	// 画面没有更新时等一小会儿，避免用「空更新」把客户端带进空转
	if !full && cc.lastFrame != nil && seq == cc.sentSeq {
		next, nextSeq := cc.srv.waitFrame(seq, memuVNCUpdateWait)
		if next != nil && nextSeq != seq {
			img, seq = next, nextSeq
			w, h = img.Bounds().Dx(), img.Bounds().Dy()
		} else {
			return cc.writeEmptyUpdate()
		}
	}

	sizeChanged := w != cc.lastW || h != cc.lastH
	if full || cc.lastFrame == nil || sizeChanged || !(hextile && pf.bpp == 32) {
		return cc.writeFullUpdate(img, seq, pf, hextile, zrle, desktopSize, sizeChanged)
	}
	return cc.writeIncrementalUpdate(img, seq, pf)
}

func (cc *memuVNCConn) writeFullUpdate(img *image.RGBA, seq uint64, pf rfbPixelFormat, hextile, zrle, desktopSize, sizeChanged bool) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	numRects := uint16(1)
	if sizeChanged && desktopSize {
		numRects = 2
	}

	var buf bytes.Buffer
	buf.Grow(w*h*pf.bppBytes()/8 + 1024)
	head := make([]byte, 4)
	binary.BigEndian.PutUint16(head[2:4], numRects)
	buf.Write(head)

	if sizeChanged && desktopSize {
		buf.Write(rectHeader(0, 0, w, h, -223))
	}

	var enc int32
	var data []byte
	switch {
	case hextile && pf.bpp == 32:
		// 本仓库内置的 noVNC 对 ZRLE 的跨帧 zlib 状态处理不稳，
		// Hextile 没有状态问题，UI 类画面压缩率也足够。
		enc = 5
		data = hextileEncodeRect(img, 0, 0, w, h, pf)
	case zrle:
		enc = 16
		data = zrleEncode(img, pf)
	default:
		data = rawEncode(img, pf)
	}
	buf.Write(rectHeader(0, 0, w, h, enc))
	buf.Write(data)

	cc.lastFrame, cc.lastW, cc.lastH, cc.sentSeq = img, w, h, seq
	return cc.writeAll(buf.Bytes())
}

// writeIncrementalUpdate 只发送发生变化的瓦片，静止画面几乎零带宽。
func (cc *memuVNCConn) writeIncrementalUpdate(img *image.RGBA, seq uint64, pf rfbPixelFormat) error {
	rects := diffRects(cc.lastFrame, img)
	if len(rects) == 0 {
		return cc.writeEmptyUpdate()
	}
	if len(rects) > memuVNCMaxRects {
		// 碎片过多（整屏滚动/视频）时整屏重发，避免协议开销压过收益
		return cc.writeFullUpdate(img, seq, pf, true, false, false, false)
	}

	var buf bytes.Buffer
	head := make([]byte, 4)
	binary.BigEndian.PutUint16(head[2:4], uint16(len(rects)))
	buf.Write(head)
	for _, r := range rects {
		buf.Write(rectHeader(r[0], r[1], r[2], r[3], 5))
		buf.Write(hextileEncodeRect(img, r[0], r[1], r[2], r[3], pf))
	}

	cc.lastFrame, cc.sentSeq = img, seq
	cc.lastW, cc.lastH = img.Bounds().Dx(), img.Bounds().Dy()
	return cc.writeAll(buf.Bytes())
}

func (cc *memuVNCConn) writeEmptyUpdate() error {
	return cc.writeAll([]byte{0, 0, 0, 0})
}

// writeAll 串行化同一连接上的写入：
// 画面更新、光标伪编码都走这里，避免两个协程把帧写花。
func (cc *memuVNCConn) writeAll(data []byte) error {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	_ = cc.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := cc.conn.Write(data)
	_ = cc.conn.SetWriteDeadline(time.Time{})
	return err
}

// diffRects 计算两帧之间发生变化的 16x16 瓦片，按行合并成矩形。
func diffRects(prev, cur *image.RGBA) [][4]int {
	if cur == nil {
		return nil
	}
	cb := cur.Bounds()
	if prev == nil || prev.Bounds() != cb || prev.Stride != cur.Stride {
		return [][4]int{{0, 0, cb.Dx(), cb.Dy()}}
	}
	w, h := cb.Dx(), cb.Dy()

	var rects [][4]int
	for ty := 0; ty < h; ty += memuVNCTile {
		th := min(memuVNCTile, h-ty)
		runStart := -1
		for tx := 0; tx < w; tx += memuVNCTile {
			tw := min(memuVNCTile, w-tx)
			changed := false
			for y := ty; y < ty+th; y++ {
				off := y*cur.Stride + tx*4
				if !bytes.Equal(prev.Pix[off:off+tw*4], cur.Pix[off:off+tw*4]) {
					changed = true
					break
				}
			}
			switch {
			case changed && runStart < 0:
				runStart = tx
			case !changed && runStart >= 0:
				rects = append(rects, [4]int{runStart, ty, tx - runStart, th})
				runStart = -1
			}
		}
		if runStart >= 0 {
			rects = append(rects, [4]int{runStart, ty, w - runStart, th})
		}
	}
	return rects
}

// sendCursor 下发光标形状（Cursor 伪编码 -239）。
func (cc *memuVNCConn) sendCursor() error {
	if cc.pixelFormat().bpp != 32 {
		return nil
	}
	w, h, hotX, hotY, pixels, mask := memuArrowCursor()

	var buf bytes.Buffer
	buf.Grow(len(pixels) + len(mask) + 16)
	buf.Write([]byte{0, 0, 0, 1})
	buf.Write(rectHeader(hotX, hotY, w, h, -239))
	buf.Write(pixels)
	buf.Write(mask)
	return cc.writeAll(buf.Bytes())
}

// memuArrowCursor 生成 24x24 经典箭头光标（BGRX 像素 + 位掩码）。
//
// 返回的 hotX/hotY 是热点，放在箭头左上角。
func memuArrowCursor() (w, h, hotX, hotY int, pixels, mask []byte) {
	const size = 24
	// 经典鼠标箭头轮廓（左上为热点）
	poly := [][2]float64{
		{0.5, 0.5}, {0.5, 17.5}, {5.0, 13.0}, {8.0, 20.5},
		{11.0, 19.0}, {8.0, 12.0}, {15.5, 12.0},
	}

	fill := make([][]bool, size)
	for y := 0; y < size; y++ {
		fill[y] = make([]bool, size)
		for x := 0; x < size; x++ {
			fill[y][x] = pointInPolygon(float64(x)+0.5, float64(y)+0.5, poly)
		}
	}

	pixels = make([]byte, size*size*4)
	mask = make([]byte, ((size+7)/8)*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b uint8
			visible := false
			switch {
			case fill[y][x]:
				r, g, b, visible = 255, 255, 255, true
			case hasFilledNeighbor(fill, x, y):
				r, g, b, visible = 0, 0, 0, true
			}
			if !visible {
				continue
			}
			i := (y*size + x) * 4
			// little-endian 32bpp：B,G,R,0
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = b, g, r, 0
			mask[y*((size+7)/8)+x/8] |= 0x80 >> uint(x%8)
		}
	}
	return size, size, 0, 0, pixels, mask
}

// pointInPolygon 射线法判断点是否在多边形内部
func pointInPolygon(px, py float64, poly [][2]float64) bool {
	inside := false
	j := len(poly) - 1
	for i := 0; i < len(poly); i++ {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > py) != (yj > py) && px < (xj-xi)*(py-yi)/(yj-yi)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

// hasFilledNeighbor 判断像素周围 8 邻域是否有填充像素（用于描边）
func hasFilledNeighbor(fill [][]bool, x, y int) bool {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			nx, ny := x+dx, y+dy
			if nx < 0 || ny < 0 || ny >= len(fill) || nx >= len(fill[ny]) {
				continue
			}
			if fill[ny][nx] {
				return true
			}
		}
	}
	return false
}

// rectHeader 构造 RFB 矩形头
func rectHeader(x, y, w, h int, encoding int32) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], uint16(x))
	binary.BigEndian.PutUint16(b[2:4], uint16(y))
	binary.BigEndian.PutUint16(b[4:6], uint16(w))
	binary.BigEndian.PutUint16(b[6:8], uint16(h))
	binary.BigEndian.PutUint32(b[8:12], uint32(encoding))
	return b
}

func appendU16(b []byte, v uint16) []byte {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func parseSmallInt(b []byte) (int, error) {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("非法数字")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ============================================================
// Hextile 编码（16x16 瓦片）
// ============================================================

// hextileColor 16 位色深下的 RGB 颜色
type hextileColor struct{ r, g, b uint8 }

// hextileEncode 生成整屏 Hextile 编码。
func hextileEncode(img *image.RGBA, pf rfbPixelFormat) []byte {
	b := img.Bounds()
	return hextileEncodeRect(img, 0, 0, b.Dx(), b.Dy(), pf)
}

// hextileEncodeRect 生成指定矩形的 Hextile 编码。
//
// 每个 16x16 瓦片取出现次数最多的颜色作为背景，其余像素按「同一行连续同色」
// 合并成子矩形；纯色瓦片只占 5 字节，UI 类画面压缩效果足够好，
// 且不像 ZRLE 那样依赖跨帧保持的 zlib 状态。
//
// 颜色统计上限为 16 色：旧实现按整张瓦片记录全部颜色（照片瓦片最多 256 色），
// 每个像素都要线性扫描整张颜色表，单帧编码可达数百毫秒。连续超过 16 色的
// 瓦片（照片/视频）直接按 Raw 子编码发送，把编码复杂度限死在常数级。
func hextileEncodeRect(img *image.RGBA, x0, y0, w, h int, pf rfbPixelFormat) []byte {
	var out bytes.Buffer
	out.Grow(w*h/2 + 1024)
	pixel := make([]byte, 4)
	writePx := func(c hextileColor) {
		n := pf.writePixel(pixel, c.r, c.g, c.b)
		out.Write(pixel[:n])
	}

	stride, pix := img.Stride, img.Pix
	at := func(x, y int) hextileColor {
		i := (y0+y)*stride + (x0+x)*4
		return hextileColor{pix[i], pix[i+1], pix[i+2]}
	}

	const maxTileColors = 16
	var colors [maxTileColors]hextileColor
	var counts [maxTileColors]int

	type run struct {
		x, y, w int
		c       hextileColor
	}
	runs := make([]run, 0, 64)

	for ty := 0; ty < h; ty += memuVNCTile {
		th := min(memuVNCTile, h-ty)
		for tx := 0; tx < w; tx += memuVNCTile {
			tw := min(memuVNCTile, w-tx)

			ncolors := 0
			tooMany := false
			for y := 0; y < th && !tooMany; y++ {
				for x := 0; x < tw; x++ {
					c := at(tx+x, ty+y)
					found := false
					for i := 0; i < ncolors; i++ {
						if colors[i] == c {
							counts[i]++
							found = true
							break
						}
					}
					if found {
						continue
					}
					if ncolors == maxTileColors {
						tooMany = true
						break
					}
					colors[ncolors] = c
					counts[ncolors] = 1
					ncolors++
				}
			}

			if tooMany {
				writeRawTile(&out, at, tx, ty, tw, th, writePx)
				continue
			}

			bg := colors[0]
			best := counts[0]
			for i := 1; i < ncolors; i++ {
				if counts[i] > best {
					bg, best = colors[i], counts[i]
				}
			}

			runs = runs[:0]
			overflow := false
			for y := 0; y < th && !overflow; y++ {
				x := 0
				for x < tw {
					c := at(tx+x, ty+y)
					if c == bg {
						x++
						continue
					}
					start := x
					for x < tw && at(tx+x, ty+y) == c {
						x++
					}
					if len(runs) == 255 {
						overflow = true
						break
					}
					runs = append(runs, run{x: start, y: y, w: x - start, c: c})
				}
			}
			if overflow {
				writeRawTile(&out, at, tx, ty, tw, th, writePx)
				continue
			}

			if len(runs) == 0 {
				out.WriteByte(0x02) // BackgroundSpecified
				writePx(bg)
				continue
			}

			// BackgroundSpecified | AnySubrects | SubrectsColoured
			out.WriteByte(0x02 | 0x08 | 0x10)
			writePx(bg)
			out.WriteByte(byte(len(runs)))
			for _, rn := range runs {
				writePx(rn.c)
				out.WriteByte(byte(rn.x<<4 | rn.y))
				out.WriteByte(byte((rn.w - 1) << 4))
			}
		}
	}
	return out.Bytes()
}

// writeRawTile 以 Raw 子编码发送一个瓦片
func writeRawTile(out *bytes.Buffer, at func(x, y int) hextileColor, tx, ty, tw, th int, writePx func(hextileColor)) {
	out.WriteByte(0x01) // Raw
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			writePx(at(tx+x, ty+y))
		}
	}
}

// rawEncode 生成 Raw 编码的整屏像素
func rawEncode(img *image.RGBA, pf rfbPixelFormat) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pixSize := pf.bppBytes()
	out := make([]byte, w*h*pixSize)
	pos := 0
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			i := x * 4
			pos += pf.writePixel(out[pos:], row[i], row[i+1], row[i+2])
		}
	}
	return out
}

// zrleEncode 生成 ZRLE 编码（64x64 瓦片，Raw 子编码 + zlib）
func zrleEncode(img *image.RGBA, pf rfbPixelFormat) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cpSize := pf.cpixelSize()

	var raw bytes.Buffer
	raw.Grow(w * h * cpSize)
	pixel := make([]byte, 4)

	for ty := 0; ty < h; ty += 64 {
		th := 64
		if ty+th > h {
			th = h - ty
		}
		for tx := 0; tx < w; tx += 64 {
			tw := 64
			if tx+tw > w {
				tw = w - tx
			}
			raw.WriteByte(0) // 子编码 0：原始像素
			for y := ty; y < ty+th; y++ {
				row := img.Pix[y*img.Stride:]
				for x := tx; x < tx+tw; x++ {
					i := x * 4
					n := pf.writeCPixel(pixel, row[i], row[i+1], row[i+2])
					raw.Write(pixel[:n])
				}
			}
		}
	}

	var compressed bytes.Buffer
	zw, err := zlib.NewWriterLevel(&compressed, zlib.BestSpeed)
	if err != nil {
		return rawEncode(img, pf)
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		_ = zw.Close()
		return rawEncode(img, pf)
	}
	_ = zw.Close()

	out := make([]byte, 4+compressed.Len())
	binary.BigEndian.PutUint32(out[0:4], uint32(compressed.Len()))
	copy(out[4:], compressed.Bytes())
	return out
}

// ============================================================
// 输入转发
// ============================================================

// androidKeyMap X11 keysym → Android keycode
var androidKeyMap = map[uint32]int{
	0xff0d: 66,  // Enter
	0xff8d: 66,  // KP_Enter
	0xff08: 67,  // BackSpace
	0xffff: 67,  // Delete
	0xff1b: 4,   // Escape → 返回键
	0xff52: 19,  // Up
	0xff54: 20,  // Down
	0xff51: 21,  // Left
	0xff53: 22,  // Right
	0xff50: 3,   // Home
	0xff57: 123, // End
	0xff55: 92,  // Page Up
	0xff56: 93,  // Page Down
	0xff09: 61,  // Tab
}

// motionEventSupport 探测设备是否支持 `input motionevent`。
//
// 支持时可以把「按下—拖动—抬起」注入成一条连续的触摸流，Android 才会
// 给出真正的拖动、惯性滑动与长按；不支持时只能退化为分段 input swipe。
// 结果按实例缓存，避免每次打开控制台都多跑一条 adb。
func (s *memuVNCServer) motionEventSupport() bool {
	if v := atomic.LoadInt32(&s.motionMode); v != 0 {
		return v > 0
	}
	args := append([]string{s.d.memucBin()}, "adb", "-i", s.idx, "--", "shell", "input")
	raw, _, _ := security.SafeExecWithTimeout(args, 10*time.Second)
	if !strings.Contains(decodeMemucOutput(raw), "motionevent") {
		// 只缓存成功结果：这次探测失败可能只是模拟器还没启动完，
		// 下次打开控制台时重新探测即可。
		return false
	}
	atomic.StoreInt32(&s.motionMode, 1)
	return true
}

// injectMotion 注入一条 motionevent；失败说明该机型实际不支持，退回分段 swipe。
func (cc *memuVNCConn) injectMotion(action string, x, y int) bool {
	if _, err := cc.srv.d.runAdb(cc.srv.idx, "shell", "input", "motionevent", action, itoa(x), itoa(y)); err != nil {
		cc.motionEvents = false
		log.Printf("[memu-vnc] 实例 #%s 的 input motionevent 不可用，改用分段 swipe：%v", cc.srv.idx, err)
		return false
	}
	return true
}

// inputWorker 串行执行输入命令，避免阻塞抓帧与读循环
func (cc *memuVNCConn) inputWorker() {
	for {
		cc.inMu.Lock()
		for len(cc.inQueue) == 0 && !cc.inStop {
			cc.inCond.Wait()
		}
		if cc.inStop {
			cc.inMu.Unlock()
			return
		}
		cmd := cc.inQueue[0]
		cc.inQueue = cc.inQueue[1:]
		cc.inMu.Unlock()
		cc.runInput(cmd)
	}
}

func (cc *memuVNCConn) enqueue(cmd string) {
	cc.inMu.Lock()
	if !cc.inStop {
		cc.inQueue = append(cc.inQueue, cmd)
		cc.inCond.Signal()
	}
	cc.inMu.Unlock()
}

// enqueueLatest 追加命令；队尾是同前缀的旧命令时直接替换。
// 拖动时每帧都可能产生 move，堆在队列里只会让手指越来越滞后。
func (cc *memuVNCConn) enqueueLatest(prefix, cmd string) {
	cc.inMu.Lock()
	if !cc.inStop {
		if n := len(cc.inQueue); n > 0 && strings.HasPrefix(cc.inQueue[n-1], prefix) {
			cc.inQueue[n-1] = cmd
		} else {
			cc.inQueue = append(cc.inQueue, cmd)
		}
		cc.inCond.Signal()
	}
	cc.inMu.Unlock()
}

func (cc *memuVNCConn) stopInput() {
	cc.inMu.Lock()
	cc.inStop = true
	cc.inCond.Broadcast()
	cc.inMu.Unlock()
}

// runInput 执行一条输入命令：
// down:x,y / move:x,y / up:x,y（触摸手势）、tap:x,y / swipe:x1,y1,x2,y2,dur
// （兼容旧命令）、key:code、text:内容。
func (cc *memuVNCConn) runInput(cmd string) {
	idx := cc.srv.idx
	d := cc.srv.d
	switch {
	case strings.HasPrefix(cmd, "down:"):
		var x, y int
		if _, err := fmt.Sscanf(cmd[5:], "%d,%d", &x, &y); err != nil {
			return
		}
		cc.inPressAt = time.Now()
		cc.inPressX, cc.inPressY = x, y
		cc.inLastX, cc.inLastY = x, y
		cc.inDragged = false
		if cc.motionEvents {
			// 按下即注入：Android 才能识别长按，并让后续 MOVE 接在同一根手指上
			cc.injectMotion("DOWN", x, y)
		}
	case strings.HasPrefix(cmd, "move:"):
		var x, y int
		if _, err := fmt.Sscanf(cmd[5:], "%d,%d", &x, &y); err != nil {
			return
		}
		if cc.inPressAt.IsZero() {
			return
		}
		if cc.motionEvents {
			if absInt(x-cc.inLastX)+absInt(y-cc.inLastY) < 2 {
				return
			}
			if cc.injectMotion("MOVE", x, y) {
				cc.inLastX, cc.inLastY = x, y
				return
			}
			// 注入失败：先松开可能仍处于按下状态的手指，再走退化路径
			_, _ = d.runAdb(idx, "shell", "input", "motionevent", "UP", itoa(x), itoa(y))
		}
		if !cc.inDragged {
			// 手指轻微抖动不该被当成滑动
			if absInt(x-cc.inPressX)+absInt(y-cc.inPressY) < 8 {
				return
			}
			cc.inDragged = true
		}
		if absInt(x-cc.inLastX)+absInt(y-cc.inLastY) < 8 {
			return
		}
		// 退化路径：每一小段用一次 input swipe，让画面跟着手指移动
		_, _ = d.runAdb(idx, "shell", "input", "swipe",
			itoa(cc.inLastX), itoa(cc.inLastY), itoa(x), itoa(y), "120")
		cc.inLastX, cc.inLastY = x, y
	case strings.HasPrefix(cmd, "up:"):
		var x, y int
		if _, err := fmt.Sscanf(cmd[3:], "%d,%d", &x, &y); err != nil {
			return
		}
		if cc.inPressAt.IsZero() {
			return
		}
		dur := int(time.Since(cc.inPressAt) / time.Millisecond)
		cc.inPressAt = time.Time{}

		if cc.motionEvents {
			if cc.injectMotion("UP", x, y) {
				cc.inDragged = false
				return
			}
		}
		switch {
		case cc.inDragged:
			// 最后一段在 move 中已经走完；抬起位置不同时补一小段
			if absInt(x-cc.inLastX)+absInt(y-cc.inLastY) >= 8 {
				_, _ = d.runAdb(idx, "shell", "input", "swipe",
					itoa(cc.inLastX), itoa(cc.inLastY), itoa(x), itoa(y), "100")
			}
		case dur < 350:
			_, _ = d.runAdb(idx, "shell", "input", "tap", itoa(x), itoa(y))
		default:
			// 原地长按：Android 以「按下时长」判定
			if dur > 2000 {
				dur = 2000
			}
			_, _ = d.runAdb(idx, "shell", "input", "swipe", itoa(x), itoa(y), itoa(x), itoa(y), itoa(dur))
		}
		cc.inDragged = false
	case strings.HasPrefix(cmd, "tap:"):
		var x, y int
		if _, err := fmt.Sscanf(cmd[4:], "%d,%d", &x, &y); err == nil {
			_, _ = d.runAdb(idx, "shell", "input", "tap", itoa(x), itoa(y))
		}
	case strings.HasPrefix(cmd, "swipe:"):
		var x1, y1, x2, y2, dur int
		if _, err := fmt.Sscanf(cmd[6:], "%d,%d,%d,%d,%d", &x1, &y1, &x2, &y2, &dur); err == nil {
			_, _ = d.runAdb(idx, "shell", "input", "swipe",
				itoa(x1), itoa(y1), itoa(x2), itoa(y2), itoa(dur))
		}
	case strings.HasPrefix(cmd, "key:"):
		_, _ = d.runAdb(idx, "shell", "input", "keyevent", cmd[4:])
	case strings.HasPrefix(cmd, "text:"):
		text := androidInputText(cmd[5:])
		if text != "" {
			_, _ = d.runAdb(idx, "shell", "input", "text", text)
		}
	}
}

// handlePointer 把 RFB 指针事件转成触摸手势。
//
// RFB 只给出「当前按下状态」和坐标，真正的点击/长按/拖动判定放在输入
// 协程里按时间与位移完成，这样按下就能立刻下发，拖动也能边走边发，
// 不再是「松开鼠标后才补一次 swipe」。
func (cc *memuVNCConn) handlePointer(mask uint8, x, y int) {
	// 滚轮：按钮 4/5（mask bit3/bit4）→ 上下滑动
	if mask&8 != 0 {
		cc.enqueue(fmt.Sprintf("swipe:%d,%d,%d,%d,%d", x, y, x, maxInt(y-260, 0), 240))
		return
	}
	if mask&16 != 0 {
		cc.enqueue(fmt.Sprintf("swipe:%d,%d,%d,%d,%d", x, y, x, y+260, 240))
		return
	}
	// 右键 → 返回键
	if mask&4 != 0 && !cc.pressed {
		cc.enqueue("key:4")
		return
	}

	if mask&1 != 0 {
		if cc.pressed {
			cc.enqueueLatest("move:", fmt.Sprintf("move:%d,%d", x, y))
			return
		}
		cc.pressed = true
		cc.enqueue(fmt.Sprintf("down:%d,%d", x, y))
		return
	}

	if !cc.pressed {
		return
	}
	cc.pressed = false
	cc.enqueue(fmt.Sprintf("up:%d,%d", x, y))
}

// handleKey 把 RFB 键盘事件转成 Android 按键或文本输入
func (cc *memuVNCConn) handleKey(down bool, sym uint32) {
	if !down {
		return
	}
	if code, ok := androidKeyMap[sym]; ok {
		cc.textBuf.flushNow(cc.enqueue)
		cc.enqueue(fmt.Sprintf("key:%d", code))
		return
	}
	if sym >= 0x20 && sym <= 0x7e {
		cc.textBuf.add(string(rune(sym)), cc.enqueue)
	}
}

// androidInputText 转义 `input text` 的参数。
//
// adb shell 会再次经过设备端 shell 解析，特殊字符会被吞掉或截断命令，
// 这里只保留常用可打印字符，空格用 Android 约定的 %s 表示。
func androidInputText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' {
			b.WriteString("%s")
			continue
		}
		if r < 0x20 || r > 0x7e {
			continue
		}
		switch r {
		case '\'', '"', '\\', '`', '$', '&', '|', ';', '<', '>', '(', ')', '*', '?', '#', '~', '!', '[', ']', '{', '}':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func itoa(v int) string {
	return fmt.Sprintf("%d", v)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ============================================================
// 文本输入缓冲：连续字符合并成一次 input text
// ============================================================

type textBuffer struct {
	mu     sync.Mutex
	buf    strings.Builder
	timer  *time.Timer
	closed bool
}

func (tb *textBuffer) add(s string, flush func(string)) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if tb.closed {
		return
	}
	tb.buf.WriteString(s)
	if tb.timer != nil {
		tb.timer.Stop()
	}
	tb.timer = time.AfterFunc(250*time.Millisecond, func() {
		tb.mu.Lock()
		text := tb.buf.String()
		tb.buf.Reset()
		tb.timer = nil
		closed := tb.closed
		tb.mu.Unlock()
		if text != "" && !closed {
			flush(text)
		}
	})
}

func (tb *textBuffer) flushNow(flush func(string)) {
	tb.mu.Lock()
	text := tb.buf.String()
	tb.buf.Reset()
	if tb.timer != nil {
		tb.timer.Stop()
		tb.timer = nil
	}
	closed := tb.closed
	tb.mu.Unlock()
	if text != "" && !closed {
		flush(text)
	}
}

func (tb *textBuffer) close() {
	tb.mu.Lock()
	tb.closed = true
	if tb.timer != nil {
		tb.timer.Stop()
		tb.timer = nil
	}
	tb.mu.Unlock()
}
