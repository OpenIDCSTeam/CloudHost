// Package driver MEmu 图形控制台（内置 RFB/VNC 服务）。
//
// 逍遥模拟器本身只提供 ADB，没有 VNC 服务；HostAgent 在这里启动一个
// 仅监听 127.0.0.1 的 RFB 3.8 服务：
//
//   - 画面：通过 `memuc adb exec-out screencap -p` 抓帧，ZRLE 压缩后推送；
//   - 输入：鼠标按下/移动/抬起的轨迹转成 `input tap/swipe`，
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
	"image/png"
	"io"
	"net"
	"strings"
	"sync"
	"time"
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

	frameMu sync.Mutex
	frame   *image.RGBA
	frameAt time.Time
	// retryAt 抓帧失败后的退避时间，避免对已关机的实例高频拉起 adb
	retryAt time.Time

	lastErr string
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
		d:     d,
		idx:   idx,
		ln:    ln,
		port:  ln.Addr().(*net.TCPAddr).Port,
		conns: map[net.Conn]struct{}{},
	}
	memuVNCServers[key] = s
	go s.serve()
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

	_ = s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
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
// 帧来源
// ============================================================

// currentFrame 返回最新画面（带短缓存，避免多个客户端重复抓帧）。
// 抓帧失败时返回占位黑屏，客户端仍能正常握手。
func (s *memuVNCServer) currentFrame() (*image.RGBA, int, int) {
	const (
		cacheTTL  = 600 * time.Millisecond
		fallbackW = 1280
		fallbackH = 720
	)

	s.frameMu.Lock()
	defer s.frameMu.Unlock()

	if s.frame != nil && time.Since(s.frameAt) < cacheTTL {
		return s.frame, s.frame.Bounds().Dx(), s.frame.Bounds().Dy()
	}
	if s.frame != nil && !s.retryAt.IsZero() && time.Now().Before(s.retryAt) {
		return s.frame, s.frame.Bounds().Dx(), s.frame.Bounds().Dy()
	}

	raw, err := s.d.captureMemuFrame(s.idx)
	if err == nil {
		decoded, decErr := png.Decode(bytes.NewReader(raw))
		if decErr == nil {
			rgba := toRGBAImage(decoded)
			s.frame = rgba
			s.frameAt = time.Now()
			s.lastErr = ""
			return rgba, rgba.Bounds().Dx(), rgba.Bounds().Dy()
		}
		err = decErr
	}
	if err != nil {
		s.lastErr = err.Error()
		s.retryAt = time.Now().Add(2 * time.Second)
	} else {
		s.retryAt = time.Time{}
	}

	if s.frame == nil {
		img := image.NewRGBA(image.Rect(0, 0, fallbackW, fallbackH))
		for i := 0; i+3 < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 18, 20, 26, 255
		}
		s.frame = img
	}
	s.frameAt = time.Now()
	return s.frame, s.frame.Bounds().Dx(), s.frame.Bounds().Dy()
}

// toRGBAImage 把任意图像转成 RGBA，便于按像素编码
func toRGBAImage(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok {
		return rgba
	}
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rgba.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return rgba
}

// ============================================================
// RFB 连接
// ============================================================

// memuVNCConn 单个客户端会话
type memuVNCConn struct {
	srv  *memuVNCServer
	conn net.Conn
	pf   rfbPixelFormat

	zrle        bool
	hextile     bool
	desktopSize bool
	cursor      bool
	cursorSent  bool

	lastW, lastH int

	inputCh chan string
	done    chan struct{}

	// 指针手势状态
	pressX, pressY int
	pressAt        time.Time
	pressed        bool

	textBuf textBuffer
}

func (s *memuVNCServer) handleConn(conn net.Conn) {
	cc := &memuVNCConn{
		srv:     s,
		conn:    conn,
		pf:      defaultRFBPixelFormat,
		inputCh: make(chan string, 64),
		done:    make(chan struct{}),
	}
	defer func() {
		close(cc.done)
		cc.textBuf.close()
	}()

	go cc.inputWorker()

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

	img, w, h := s.currentFrame()
	_ = img
	cc.lastW, cc.lastH = w, h
	if err := cc.writeServerInit(w, h); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	cc.readLoop()
}

// writeServerInit 发送 ServerInit（含像素格式与桌面名称）
func (cc *memuVNCConn) writeServerInit(w, h int) error {
	name := "MEmu Android"
	if cc.srv.d.Config != nil {
		name = "MEmu " + cc.srv.d.Config.ServerName + " #" + cc.srv.idx
	}

	buf := make([]byte, 0, 24+len(name))
	buf = appendU16(buf, uint16(w))
	buf = appendU16(buf, uint16(h))
	buf = append(buf,
		cc.pf.bpp, cc.pf.depth, cc.pf.bigEndian, cc.pf.trueColor,
	)
	buf = appendU16(buf, cc.pf.redMax)
	buf = appendU16(buf, cc.pf.greenMax)
	buf = appendU16(buf, cc.pf.blueMax)
	buf = append(buf, cc.pf.redShift, cc.pf.greenShift, cc.pf.blueShift)
	buf = append(buf, 0, 0, 0) // padding
	buf = appendU32(buf, uint32(len(name)))
	buf = append(buf, []byte(name)...)
	_, err := cc.conn.Write(buf)
	return err
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
			cc.pf = parseRFBPixelFormat(b[3:])
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
			// 客户端声明支持光标伪编码后立即下发一次箭头光标。
			// 不发送光标时 noVNC 会一直使用 RFB.cursors.none，
			// 表现就是「控制台里看不到鼠标」。
			if cc.cursor && !cc.cursorSent {
				cc.cursorSent = true
				if err := cc.sendCursor(); err != nil {
					return
				}
			}
		case 3: // FramebufferUpdateRequest
			b := make([]byte, 9)
			if _, err := io.ReadFull(cc.conn, b); err != nil {
				return
			}
			if err := cc.sendFramebufferUpdate(); err != nil {
				return
			}
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

// sendCursor 下发光标形状（Cursor 伪编码 -239）。
func (cc *memuVNCConn) sendCursor() error {
	if cc.pf.bpp != 32 {
		return nil
	}
	w, h, hotX, hotY, pixels, mask := memuArrowCursor()

	var buf bytes.Buffer
	buf.Grow(len(pixels) + len(mask) + 16)
	buf.Write([]byte{0, 0, 0, 1})
	buf.Write(rectHeader(hotX, hotY, w, h, -239))
	buf.Write(pixels)
	buf.Write(mask)

	_ = cc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := cc.conn.Write(buf.Bytes())
	_ = cc.conn.SetWriteDeadline(time.Time{})
	return err
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

// sendFramebufferUpdate 抓取一帧并推送（Raw 或 ZRLE）
func (cc *memuVNCConn) sendFramebufferUpdate() error {
	img, w, h := cc.srv.currentFrame()
	if w <= 0 || h <= 0 {
		return errors.New("无效的画面尺寸")
	}

	sizeChanged := w != cc.lastW || h != cc.lastH
	numRects := uint16(1)
	if sizeChanged && cc.desktopSize {
		numRects = 2
	}

	var buf bytes.Buffer
	buf.Grow(w*h*cc.pf.bppBytes() + 64)
	buf.Write([]byte{0, 0, 0, 0})
	binary.BigEndian.PutUint16(buf.Bytes()[2:4], numRects)

	if sizeChanged && cc.desktopSize {
		buf.Write(rectHeader(0, 0, w, h, -223))
		buf.Write(nil)
	}

	encoding := int32(0)
	data := []byte(nil)
	switch {
	case cc.hextile && cc.pf.bpp == 32:
		// 本仓库内置的 noVNC 在 ZRLE 解码后没有 inflateReset，
		// 第二帧起会报 "Incomplete zlib block"；Hextile 无此状态问题。
		encoding = 5
		data = hextileEncode(img, cc.pf)
	case cc.zrle:
		encoding = 16
		data = zrleEncode(img, cc.pf)
	default:
		data = rawEncode(img, cc.pf)
	}
	buf.Write(rectHeader(0, 0, w, h, encoding))
	buf.Write(data)

	cc.lastW, cc.lastH = w, h

	_ = cc.conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	_, err := cc.conn.Write(buf.Bytes())
	_ = cc.conn.SetWriteDeadline(time.Time{})
	return err
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

// hextileEncode 生成 Hextile 编码。
//
// 每个 16x16 瓦片取出现次数最多的颜色作为背景，其余像素按「同一行连续同色」
// 合并成子矩形；纯色瓦片只占 5 字节，UI 类画面压缩效果足够好，
// 且不像 ZRLE 那样依赖跨帧保持的 zlib 状态。
func hextileEncode(img *image.RGBA, pf rfbPixelFormat) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	var out bytes.Buffer
	out.Grow(w*h + 1024)
	pixel := make([]byte, 4)
	writePx := func(c hextileColor) {
		n := pf.writePixel(pixel, c.r, c.g, c.b)
		out.Write(pixel[:n])
	}
	at := func(x, y int) hextileColor {
		i := y*img.Stride + x*4
		return hextileColor{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
	}

	type run struct {
		x, y, w int
		c       hextileColor
	}
	colors := make([]hextileColor, 0, 16)
	counts := make([]int, 0, 16)
	runs := make([]run, 0, 32)

	for ty := 0; ty < h; ty += 16 {
		th := 16
		if ty+th > h {
			th = h - ty
		}
		for tx := 0; tx < w; tx += 16 {
			tw := 16
			if tx+tw > w {
				tw = w - tx
			}

			colors = colors[:0]
			counts = counts[:0]
			for y := ty; y < ty+th; y++ {
				for x := tx; x < tx+tw; x++ {
					c := at(x, y)
					found := false
					for i := range colors {
						if colors[i] == c {
							counts[i]++
							found = true
							break
						}
					}
					if !found {
						colors = append(colors, c)
						counts = append(counts, 1)
					}
				}
			}
			bg := colors[0]
			best := counts[0]
			for i := 1; i < len(colors); i++ {
				if counts[i] > best {
					bg, best = colors[i], counts[i]
				}
			}

			runs = runs[:0]
			for y := 0; y < th; y++ {
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
					runs = append(runs, run{x: start, y: y, w: x - start, c: c})
				}
			}

			if len(runs) == 0 {
				out.WriteByte(0x02) // BackgroundSpecified
				writePx(bg)
				continue
			}
			if len(runs) > 255 {
				out.WriteByte(0x01) // Raw
				for y := ty; y < ty+th; y++ {
					for x := tx; x < tx+tw; x++ {
						writePx(at(x, y))
					}
				}
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

// inputWorker 串行执行输入命令，避免阻塞抓帧
func (cc *memuVNCConn) inputWorker() {
	for {
		select {
		case <-cc.done:
			return
		case cmd, ok := <-cc.inputCh:
			if !ok {
				return
			}
			cc.runInput(cmd)
		}
	}
}

func (cc *memuVNCConn) enqueue(cmd string) {
	select {
	case cc.inputCh <- cmd:
	case <-cc.done:
	}
}

// runInput 执行一条输入命令：tap:x,y / swipe:x1,y1,x2,y2,dur / key:code / text:内容
func (cc *memuVNCConn) runInput(cmd string) {
	idx := cc.srv.idx
	d := cc.srv.d
	switch {
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
// RFB 只给出「当前按下状态」，Android 需要完整的 tap/swipe：
// 按下时记录起点，抬起时按位移与耗时决定点击、长按或滑动。
func (cc *memuVNCConn) handlePointer(mask uint8, x, y int) {
	// 滚轮：按钮 4/5（mask bit3/bit4）→ 上下滑动
	if mask&8 != 0 {
		cc.enqueue(fmt.Sprintf("swipe:%d,%d,%d,%d,%d", x, y, x, maxInt(y-260, 0), 220))
		return
	}
	if mask&16 != 0 {
		cc.enqueue(fmt.Sprintf("swipe:%d,%d,%d,%d,%d", x, y, x, y+260, 220))
		return
	}
	// 右键 → 返回键
	if mask&4 != 0 {
		cc.enqueue("key:4")
		return
	}

	if mask&1 != 0 {
		cc.pressX, cc.pressY = x, y
		cc.pressAt = time.Now()
		cc.pressed = true
		return
	}

	if !cc.pressed {
		return
	}
	cc.pressed = false
	dur := int(time.Since(cc.pressAt) / time.Millisecond)
	dx := x - cc.pressX
	dy := y - cc.pressY
	dist := dx*dx + dy*dy

	if dist <= 144 && dur < 350 {
		cc.enqueue(fmt.Sprintf("tap:%d,%d", x, y))
		return
	}
	if dist <= 144 {
		// 原地长按
		if dur < 400 {
			dur = 400
		}
		if dur > 2000 {
			dur = 2000
		}
		cc.enqueue(fmt.Sprintf("swipe:%d,%d,%d,%d,%d", x, y, x, y, dur))
		return
	}
	if dur < 100 {
		dur = 100
	}
	if dur > 3000 {
		dur = 3000
	}
	cc.enqueue(fmt.Sprintf("swipe:%d,%d,%d,%d,%d", cc.pressX, cc.pressY, x, y, dur))
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
