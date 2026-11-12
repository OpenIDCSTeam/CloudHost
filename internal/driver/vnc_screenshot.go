// Package driver 虚拟机屏幕采集：最小 RFB（VNC）客户端。
//
// 用于没有截图 API 的平台（如 VMware Workstation）：读取一次帧缓冲并编码为 PNG。
// 只实现抓屏必需的最小子集：
//   - 协议 3.3 / 3.8 握手，安全类型 None
//   - SetPixelFormat 固定请求 32bpp / depth 24 / 小端 / true-colour
//   - SetEncodings 只请求 Raw，逐矩形解析 FramebufferUpdate
//
// 不支持的场景（VncAuth 密码、Tight/ZRLE 等压缩编码）会返回明确错误，
// 由调用方转成用户可读的提示。
package driver

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// vncMaxDimension 帧缓冲尺寸上限，防止异常 ServerInit 触发超大内存分配
const vncMaxDimension = 8192

// captureVNCScreenshot 连接 VNC 服务并抓取一帧，返回 PNG 字节。
func captureVNCScreenshot(host string, port int, timeout time.Duration) ([]byte, error) {
	if port <= 0 {
		return nil, fmt.Errorf("VNC 端口无效")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接 VNC 失败(%s): %w", addr, err)
	}
	defer conn.Close()
	// 整个握手 + 抓帧共用同一个截止时间
	_ = conn.SetDeadline(time.Now().Add(timeout))

	img, err := readVNCFrame(conn)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("编码 PNG 失败: %w", err)
	}
	return buf.Bytes(), nil
}

// readVNCFrame 完成 RFB 握手并读取一帧完整画面。
func readVNCFrame(conn net.Conn) (*image.RGBA, error) {
	r := bufio.NewReaderSize(conn, 64*1024)

	major, minor, err := vncHandshakeVersion(r, conn)
	if err != nil {
		return nil, err
	}
	if err := vncHandshakeSecurity(r, conn, major, minor); err != nil {
		return nil, err
	}

	// ClientInit：shared=1，允许与现有控制台并发观看
	if _, err := conn.Write([]byte{1}); err != nil {
		return nil, fmt.Errorf("发送 ClientInit 失败: %w", err)
	}

	// ServerInit
	var init [4]byte
	if _, err := io.ReadFull(r, init[:]); err != nil {
		return nil, fmt.Errorf("读取 ServerInit 失败: %w", err)
	}
	width := int(binary.BigEndian.Uint16(init[0:2]))
	height := int(binary.BigEndian.Uint16(init[2:4]))
	if width <= 0 || height <= 0 || width > vncMaxDimension || height > vncMaxDimension {
		return nil, fmt.Errorf("VNC 画面尺寸异常: %dx%d", width, height)
	}
	var pf [16]byte
	if _, err := io.ReadFull(r, pf[:]); err != nil {
		return nil, fmt.Errorf("读取像素格式失败: %w", err)
	}
	var nameLen [4]byte
	if _, err := io.ReadFull(r, nameLen[:]); err != nil {
		return nil, fmt.Errorf("读取桌面名长度失败: %w", err)
	}
	if n := binary.BigEndian.Uint32(nameLen[:]); n > 0 && n <= 4096 {
		if _, err := io.ReadFull(r, make([]byte, n)); err != nil {
			return nil, fmt.Errorf("读取桌面名失败: %w", err)
		}
	}

	// SetPixelFormat：32bpp / depth 24 / 小端 / true-colour，R<<16 G<<8 B
	setPF := []byte{
		0, 0, 0, 0, // type + padding
		32, 24, 0, 1, // bits-per-pixel, depth, big-endian, true-colour
		0, 255, 0, 255, 0, 255, // red/green/blue max
		16, 8, 0, // red/green/blue shift
		0, 0, 0, // padding
	}
	if _, err := conn.Write(setPF); err != nil {
		return nil, fmt.Errorf("发送 SetPixelFormat 失败: %w", err)
	}
	// SetEncodings：只接受 Raw
	if _, err := conn.Write([]byte{2, 0, 0, 1, 0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("发送 SetEncodings 失败: %w", err)
	}

	req := vncFramebufferRequest(width, height)
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("发送 FramebufferUpdateRequest 失败: %w", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	received := false
	// 限制消息轮次：正常情况下第一帧就返回，桌面尺寸变化等消息最多也只需几轮
	for round := 0; round < 32; round++ {
		var msg [1]byte
		if _, err := io.ReadFull(r, msg[:]); err != nil {
			return nil, fmt.Errorf("读取 VNC 消息失败: %w", err)
		}
		switch msg[0] {
		case 0: // FramebufferUpdate
			painted, err := readVNCUpdate(r, img)
			if err != nil {
				return nil, err
			}
			if painted {
				received = true
			}
			if received {
				return img, nil
			}
			// 只收到 DesktopSize 等无像素消息，再请求一次
			if _, err := conn.Write(req); err != nil {
				return nil, fmt.Errorf("再次请求画面失败: %w", err)
			}
		case 1: // SetColourMapEntries
			var hdr [5]byte
			if _, err := io.ReadFull(r, hdr[:]); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(hdr[3:5]))
			if _, err := io.ReadFull(r, make([]byte, n*6)); err != nil {
				return nil, err
			}
		case 2: // Bell
		case 3: // ServerCutText
			var hdr [7]byte
			if _, err := io.ReadFull(r, hdr[:]); err != nil {
				return nil, err
			}
			n := binary.BigEndian.Uint32(hdr[3:7])
			if n > 0 && n <= 1<<20 {
				if _, err := io.ReadFull(r, make([]byte, n)); err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("未知 VNC 消息类型: %d", msg[0])
		}
	}
	return nil, fmt.Errorf("VNC 未返回有效画面")
}

// vncHandshakeVersion 交换协议版本，返回对端主/次版本号。
func vncHandshakeVersion(r io.Reader, conn net.Conn) (int, int, error) {
	var ver [12]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return 0, 0, fmt.Errorf("读取 VNC 版本失败: %w", err)
	}
	text := strings.TrimSpace(string(ver[:]))
	if !strings.HasPrefix(text, "RFB ") {
		return 0, 0, fmt.Errorf("对端不是 VNC 服务: %q", text)
	}
	parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(text, "RFB ")), ".", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("VNC 版本格式异常: %q", text)
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])

	// 统一回 3.8：3.3 服务端只解析前 7 字节，兼容性最好
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return 0, 0, fmt.Errorf("发送 VNC 版本失败: %w", err)
	}
	return major, minor, nil
}

// vncHandshakeSecurity 处理安全握手中的 None 认证。
func vncHandshakeSecurity(r io.Reader, conn net.Conn, major, minor int) error {
	if major > 3 || (major == 3 && minor >= 7) {
		var count [1]byte
		if _, err := io.ReadFull(r, count[:]); err != nil {
			return fmt.Errorf("读取 VNC 安全类型失败: %w", err)
		}
		if count[0] == 0 {
			return fmt.Errorf("VNC 拒绝连接: %s", vncReadReason(r))
		}
		types := make([]byte, int(count[0]))
		if _, err := io.ReadFull(r, types); err != nil {
			return fmt.Errorf("读取 VNC 安全类型列表失败: %w", err)
		}
		none, vncAuth := false, false
		for _, t := range types {
			switch t {
			case 1: // None
				none = true
			case 2: // VNC Authentication
				vncAuth = true
			case 16, 18: // Tight / TLS 等，本实现不支持
			}
		}
		switch {
		case none:
			// 选择 None
			if _, err := conn.Write([]byte{1}); err != nil {
				return fmt.Errorf("发送安全类型失败: %w", err)
			}
		case vncAuth:
			return fmt.Errorf("VNC 启用了密码认证（RemoteDisplay.vnc.password），暂不支持抓屏")
		default:
			return fmt.Errorf("VNC 不支持的安全类型: %v", types)
		}

		var result [4]byte
		if _, err := io.ReadFull(r, result[:]); err != nil {
			return fmt.Errorf("读取 VNC 认证结果失败: %w", err)
		}
		if binary.BigEndian.Uint32(result[:]) != 0 {
			return fmt.Errorf("VNC 认证失败: %s", vncReadReason(r))
		}
		return nil
	}

	// 3.3：4 字节安全类型，无 SecurityResult
	var sec [4]byte
	if _, err := io.ReadFull(r, sec[:]); err != nil {
		return fmt.Errorf("读取 VNC 安全类型失败: %w", err)
	}
	switch binary.BigEndian.Uint32(sec[:]) {
	case 1: // None
		return nil
	case 2:
		return fmt.Errorf("VNC 启用了密码认证（RemoteDisplay.vnc.password），暂不支持抓屏")
	case 0:
		return fmt.Errorf("VNC 拒绝连接: %s", vncReadReason(r))
	default:
		return fmt.Errorf("VNC 不支持的安全类型: %d", binary.BigEndian.Uint32(sec[:]))
	}
}

// vncReadReason 读取服务端拒绝原因字符串。
func vncReadReason(r io.Reader) string {
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return "未知原因"
	}
	n := binary.BigEndian.Uint32(l[:])
	if n == 0 || n > 4096 {
		return "未知原因"
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "未知原因"
	}
	return strings.TrimSpace(string(buf))
}

// vncFramebufferRequest 构造全屏非增量刷新请求。
func vncFramebufferRequest(width, height int) []byte {
	req := make([]byte, 10)
	req[0] = 3 // FramebufferUpdateRequest
	// req[1] = 0 非增量
	binary.BigEndian.PutUint16(req[6:], uint16(width))
	binary.BigEndian.PutUint16(req[8:], uint16(height))
	return req
}

// readVNCUpdate 解析一条 FramebufferUpdate，返回是否画上了像素。
func readVNCUpdate(r io.Reader, img *image.RGBA) (bool, error) {
	var pad [1]byte
	if _, err := io.ReadFull(r, pad[:]); err != nil {
		return false, err
	}
	var cnt [2]byte
	if _, err := io.ReadFull(r, cnt[:]); err != nil {
		return false, err
	}
	painted := false
	for i := 0; i < int(binary.BigEndian.Uint16(cnt[:])); i++ {
		var rh [12]byte
		if _, err := io.ReadFull(r, rh[:]); err != nil {
			return false, err
		}
		x := int(binary.BigEndian.Uint16(rh[0:2]))
		y := int(binary.BigEndian.Uint16(rh[2:4]))
		w := int(binary.BigEndian.Uint16(rh[4:6]))
		h := int(binary.BigEndian.Uint16(rh[6:8]))
		enc := int32(binary.BigEndian.Uint32(rh[8:12]))

		switch enc {
		case 0: // Raw
			if w <= 0 || h <= 0 {
				continue
			}
			buf := make([]byte, w*h*4)
			if _, err := io.ReadFull(r, buf); err != nil {
				return false, fmt.Errorf("读取画面像素失败: %w", err)
			}
			vncBlitRGBA(img, x, y, w, h, buf)
			painted = true
		case -223: // DesktopSize：无像素数据，下一条消息即可
			continue
		default:
			return false, fmt.Errorf("VNC 返回了不支持的编码 %d", enc)
		}
	}
	return painted, nil
}

// vncBlitRGBA 把 32bpp 小端像素（R<<16|G<<8|B）写入 RGBA 图像。
func vncBlitRGBA(img *image.RGBA, x, y, w, h int, buf []byte) {
	bounds := img.Rect
	for row := 0; row < h; row++ {
		dy := y + row
		if dy < bounds.Min.Y || dy >= bounds.Max.Y {
			continue
		}
		for col := 0; col < w; col++ {
			dx := x + col
			if dx < bounds.Min.X || dx >= bounds.Max.X {
				continue
			}
			off := (row*w + col) * 4
			p := img.PixOffset(dx, dy)
			img.Pix[p] = buf[off+2]
			img.Pix[p+1] = buf[off+1]
			img.Pix[p+2] = buf[off]
			img.Pix[p+3] = 0xff
		}
	}
}
