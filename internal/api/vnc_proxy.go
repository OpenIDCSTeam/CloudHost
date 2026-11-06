// Package api VNC/WebSocket代理服务
// 在6080端口提供WebSocket->TCP VNC代理（Websockify功能）
// 在7681端口提供WebSocket SSH终端服务（ttyd功能）
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// vncProxyPort VNC 代理（websockify）监听端口。
// 与 cmd/server/main.go 中 NewVNCProxyServer(6080) 保持一致。
const vncProxyPort = 6080

// vncTokenTTL 控制台 token 有效期。
// 控制台页面可能长时间开着，断线重连也要能复用同一个 token，
// 因此给足 30 分钟（token 为 32 位随机串，过期即失效）。
const vncTokenTTL = 30 * time.Minute

// VNCToken VNC访问Token
type VNCToken struct {
	HsName   string
	VmUUID   string
	Host     string
	Port     int
	ClientIP string
	ExpireAt time.Time

	// Kind 代理类型："tcp"（默认，裸 VNC 端口）或 "pvewebsocket"（PVE 官方通道）。
	// PVE 的 vncproxy 只给出节点本地端口 + 一次性 ticket，必须经
	// /api2/json/nodes/{node}/qemu/{vmid}/vncwebsocket 才能访问。
	Kind    string
	APIHost string // PVE API 的 host[:port]
	Node    string
	VMID    int
	Ticket  string
	// Cookie/CSRF PVE API 登录凭据：vncwebsocket 仅认 vncticket 会返回
	// 401 No ticket，必须同时带上 API 会话；创建 vnc 会话还需要 CSRF。
	Cookie string
	CSRF   string
}

// newVNCToken 生成一个随机 token 字符串。
func newVNCToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 随机源不可用时退化为时间戳派生的值，仍保证唯一性
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		buf = sum[:16]
	}
	return hex.EncodeToString(buf)
}

// registerVNCBridgeToken 生成并注册一个 VNC 桥接 token，返回 token 字符串。
//
// ClientIP 留空表示不绑定来源 IP：控制台地址由后端拼装后交给浏览器，
// 浏览器再连 6080，此时来源 IP 与 API 请求 IP 一致，但若中间有 NAT/代理
// 则可能不同，绑定 IP 会造成偶发 401。VNC 端口本身不对外暴露时风险可控。
func registerVNCBridgeToken(hsName, vmUUID, host string, port int) string {
	token := newVNCToken()
	RegisterVNCToken(token, hsName, vmUUID, host, "", port)
	return token
}

// registerPVEVNCToken 注册一个经 PVE 官方 vncwebsocket 通道转发的 token。
//
// port/ticket 来自调用方预先创建的 vnc 会话：noVNC 的 VNC 密码是
// ticket 的前 8 位，必须和代理实际使用的会话保持一致，因此不能
// 等到连接时再新建。
func registerPVEVNCToken(hsName, vmUUID, apiHost, node string, vmid, vncPort int, ticket, cookie, csrf string) string {
	token := newVNCToken()
	vncTokensMu.Lock()
	vncTokens[token] = &VNCToken{
		HsName:   hsName,
		VmUUID:   vmUUID,
		Host:     apiHost,
		Port:     vncPort,
		Kind:     "pvewebsocket",
		APIHost:  apiHost,
		Node:     node,
		VMID:     vmid,
		Ticket:   ticket,
		Cookie:   cookie,
		CSRF:     csrf,
		ExpireAt: time.Now().Add(vncTokenTTL),
	}
	vncTokensMu.Unlock()
	return token
}

var (
	vncTokens   = make(map[string]*VNCToken)
	vncTokensMu sync.RWMutex
)

// RegisterVNCToken 注册VNC一次性Token（有效期5分钟，绑定客户端IP）
func RegisterVNCToken(token, hsName, vmUUID, host, clientIP string, port int) {
	vncTokensMu.Lock()
	defer vncTokensMu.Unlock()
	vncTokens[token] = &VNCToken{
		HsName:   hsName,
		VmUUID:   vmUUID,
		Host:     host,
		Port:     port,
		ClientIP: clientIP,
		ExpireAt: time.Now().Add(vncTokenTTL),
	}
}

// GetVNCToken 获取并验证VNC Token。
//
// token 在有效期内可重复使用：noVNC 在断线后会自动重连，
// 若第一次握手就把它删掉，重连必然 401（浏览器提示
// 「HTTP Authentication failed; no valid credentials available」），
// 表现为「控制台打不开/一断就再也连不上」。
// token 本身是 32 位随机串且有过期时间，重复使用不额外扩大暴露面。
func GetVNCToken(token string, clientIP string) *VNCToken {
	vncTokensMu.Lock()
	defer vncTokensMu.Unlock()
	t, ok := vncTokens[token]
	if !ok || time.Now().After(t.ExpireAt) {
		if ok {
			delete(vncTokens, token)
		}
		return nil
	}
	// 验证客户端IP绑定（跳过本地回环）
	if clientIP != "127.0.0.1" && clientIP != "::1" && t.ClientIP != clientIP && t.ClientIP != "" {
		return nil
	}
	return t
}

// shortToken 截断 token 用于日志，避免把完整凭据写进日志文件。
func shortToken(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8]
}

// VNCProxyServer VNC WebSocket代理服务器（Websockify）
type VNCProxyServer struct {
	port     int
	upgrader websocket.Upgrader
	srv      *http.Server
	mu       sync.Mutex
}

// allowedOrigins VNC允许的Origin白名单。
//
// 必须覆盖前端实际使用的所有端口：Vite 开发端口为 5171
// （见 FrontPage/vite.config.ts），后端 1881；5183/5173 为历史端口，
// 保留以免影响已在用的本地环境。
//
// 说明：白名单过窄会导致 WebSocket 握手被 403 拒绝，
// 表现为「控制台打不开 / 终端连不上」，且只在浏览器控制台可见。
var allowedOrigins = map[string]bool{
	"http://localhost:1881": true,
	"http://localhost:5171": true,
	"http://localhost:5173": true,
	"http://localhost:5183": true,
	"http://127.0.0.1:1881": true,
	"http://127.0.0.1:5171": true,
	"http://127.0.0.1:5173": true,
	"http://127.0.0.1:5183": true,
}

// checkWSOrigin 校验 WebSocket 升级请求的 Origin。
//
// 生产部署下前端与后端同源（同一端口由 Gin 托管静态资源），
// 此时 Origin 形如 http://<host>[:port]，不属于 localhost 白名单，
// 若直接按白名单拒绝会导致线上 VNC/SSH 终端全部不可用。
// 因此策略为：
//  1. 无 Origin（同源非浏览器客户端、原生 WebSocket）：放行；
//  2. localhost/127.0.0.1 白名单命中：放行（本地开发）；
//  3. 其余情况：要求 Origin 的 host 与请求 Host 一致（同源），否则拒绝。
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if allowedOrigins[origin] {
		return true
	}
	// 同源判定：比较 Origin 的 host 与请求的 Host
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	log.Printf("[ws] 拒绝跨源连接 Origin=%s Host=%s", origin, r.Host)
	return false
}

// NewVNCProxyServer 创建VNC代理服务器
func NewVNCProxyServer(port int) *VNCProxyServer {
	return &VNCProxyServer{
		port: port,
		upgrader: websocket.Upgrader{
			CheckOrigin:     checkWSOrigin,
			ReadBufferSize:  8192,
			WriteBufferSize: 8192,
		},
	}
}

// resolveConsoleWebRoot 定位 noVNC 页面目录（必须包含 vnc.html）。
//
// 原实现固定用 "./VNCConsole"：IDCClient 部署目录下并不存在该目录，
// 导致 6080 的 /vnc.html 恒为 404 —— 即使控制台地址拼装正确也打不开。
func resolveConsoleWebRoot() string {
	candidates := []string{"./VNCConsole"}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(base, "VNCConsole"),
			filepath.Join(base, "..", "VNCConsole"),
		)
	}
	// 开发环境：noVNC 资源随 Python 版 HostAgent 一起存放
	candidates = append(candidates, filepath.Join("..", "HostAgent", "VNCConsole", "Sources"))

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "vnc.html")); err != nil {
			continue
		}
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	return ""
}

// Start 启动VNC代理服务器
func (s *VNCProxyServer) Start() {
	webRoot := resolveConsoleWebRoot()

	mux := http.NewServeMux()
	mux.HandleFunc("/websockify", s.handleWebsockify)
	if webRoot != "" {
		files := http.FileServer(http.Dir(webRoot))
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// noVNC 资源是静态文件（约 1.4 MB），允许浏览器缓存：
			// 每次打开控制台都重新下载会明显拖慢首屏。
			w.Header().Set("Cache-Control", "public, max-age=86400")
			files.ServeHTTP(w, r)
		}))
		log.Printf("[vnc-proxy] noVNC 页面目录: %s", webRoot)
	} else {
		// 明确报错而不是静默 404：否则浏览器只看到空白页，无从排查
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "noVNC 静态资源缺失：请确认 VNCConsole/vnc.html 存在", http.StatusNotFound)
		})
		log.Printf("[vnc-proxy] 警告: 未找到 VNCConsole/vnc.html，控制台页面将不可用")
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.port),
		Handler:           mux,
		ReadHeaderTimeout: 20 * time.Second,
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()

	go func() {
		log.Printf("[vnc-proxy] 启动VNC代理服务，端口: %d", s.port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[vnc-proxy] 启动失败: %v", err)
		}
	}()
}

// Stop 关闭VNC代理服务
func (s *VNCProxyServer) Stop(ctx context.Context) {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[vnc-proxy] 关闭失败: %v", err)
	}
}

// handleWebsockify 处理WebSocket->TCP代理
func (s *VNCProxyServer) handleWebsockify(w http.ResponseWriter, r *http.Request) {
	// 验证Token + IP绑定
	token := r.URL.Query().Get("token")
	clientIP := getRealIP(r)
	vncToken := GetVNCToken(token, clientIP)
	if vncToken == nil {
		// 400/401 只会在浏览器控制台可见，这里留一条带前缀的日志便于排查：
		// 常见原因是「页面停留过久 token 过期」或「服务重启后旧页面未刷新」。
		log.Printf("[vnc-proxy] token 无效或已过期，拒绝连接（client=%s token=%s…）",
			clientIP, shortToken(token))
		http.Error(w, "Token无效或已过期", http.StatusUnauthorized)
		return
	}

	// 升级WebSocket连接
	wsConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[vnc-proxy] WebSocket升级失败: %v", err)
		return
	}
	defer wsConn.Close()

	// 设置读写超时（空闲10分钟断开）
	wsConn.SetReadDeadline(time.Now().Add(10 * time.Minute))
	wsConn.SetPongHandler(func(string) error {
		wsConn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		return nil
	})

	// PVE 的 VNC 只能经官方 vncwebsocket 通道访问，走 WebSocket 隧道
	if vncToken.Kind == "pvewebsocket" {
		s.proxyPVEWebsocket(wsConn, vncToken)
		return
	}

	// 连接到VNC TCP服务（JoinHostPort 兼容 IPv6 地址）
	tcpAddr := net.JoinHostPort(vncToken.Host, strconv.Itoa(vncToken.Port))
	tcpConn, err := net.DialTimeout("tcp", tcpAddr, 10*time.Second)
	if err != nil {
		log.Printf("[vnc-proxy] 连接VNC服务失败 %s: %v", tcpAddr, err)
		return
	}
	defer tcpConn.Close()

	// 双向代理（使用channel等待双方都结束）
	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		for {
			// NextReader + io.Copy 直接把 WebSocket 帧写入 TCP 连接，
			// 避免 ReadMessage 每帧都分配新缓冲区（noVNC 的帧率高、帧小）。
			_, reader, err := wsConn.NextReader()
			if err != nil {
				return
			}
			if _, err := io.Copy(tcpConn, reader); err != nil {
				return
			}
		}
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32768)
		for {
			n, err := tcpConn.Read(buf)
			if err != nil {
				return
			}
			if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	<-done
}

// proxyPVEWebsocket 把浏览器 WebSocket 隧道到 PVE 的 vncwebsocket。
//
// PVE 的 vncproxy 只返回「节点本地 VNC 端口 + 一次性 ticket」，
// 该端口不对局域网开放，也没有可用的 VNC 密码；官方客户端（noVNC）
// 走的是 wss://<node>:8006/api2/json/nodes/<node>/qemu/<vmid>/vncwebsocket。
// 原实现用 net.Dial 直连那个内部端口，必然连不上，表现为控制台黑屏/认证失败。
func (s *VNCProxyServer) proxyPVEWebsocket(client *websocket.Conn, t *VNCToken) {
	scheme := "wss"
	apiHost := strings.TrimSpace(t.APIHost)
	lower := strings.ToLower(apiHost)
	switch {
	case strings.HasPrefix(lower, "https://"):
		apiHost = apiHost[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		apiHost = apiHost[len("http://"):]
		scheme = "ws"
	}

	// 优先使用调用方预先创建好的会话：noVNC 的 VNC 密码就是它的
	// ticket 前 8 位，现场另建会话会得到不同密码，认证必然失败。
	vncPort, vncTicket := t.Port, t.Ticket
	if vncPort <= 0 || vncTicket == "" {
		// 兜底：没有预创建会话时才现场创建
		var sessErr error
		vncPort, vncTicket, sessErr = createPVEVNCSession(scheme, apiHost, t.Node, t.VMID, t.Cookie, t.CSRF)
		if sessErr != nil {
			log.Printf("[vnc-proxy] 创建 PVE vnc 会话失败: %v", sessErr)
			_ = client.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "pve vnc session failed"))
			return
		}
	}

	target := fmt.Sprintf("%s://%s/api2/json/nodes/%s/qemu/%d/vncwebsocket?port=%d&vncticket=%s",
		scheme, apiHost, url.PathEscape(t.Node), t.VMID, vncPort, url.QueryEscape(vncTicket))

	dialer := websocket.Dialer{
		// PVE 默认自签证书
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 10 * time.Second,
	}
	header := http.Header{}
	if t.Cookie != "" {
		header.Set("Cookie", "PVEAuthCookie="+t.Cookie)
	}
	remote, resp, err := dialer.Dial(target, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("[vnc-proxy] 连接 PVE vncwebsocket 失败（HTTP %d）: %v", status, err)
		_ = client.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "pve vnc connect failed"))
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	pipe := func(src, dst *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			_, reader, err := src.NextReader()
			if err != nil {
				return
			}
			w, err := dst.NextWriter(websocket.BinaryMessage)
			if err != nil {
				return
			}
			if _, err := io.Copy(w, reader); err != nil {
				_ = w.Close()
				return
			}
			if err := w.Close(); err != nil {
				return
			}
		}
	}
	go pipe(client, remote)
	go pipe(remote, client)
	<-done
}

// createPVEVNCSession 通过 PVE API 现场创建一个 vnc 会话，返回端口与 vncticket。
//
// vncproxy 需要 API 会话（Cookie）与 CSRF token；每次调用都会新建会话，
// 因此必须在真正要连接时创建，不能提前缓存。
func createPVEVNCSession(scheme, apiHost, node string, vmid int, cookie, csrf string) (int, string, error) {
	// 调用方传的是 WebSocket 的 scheme（ws/wss），但这里是普通 HTTP 请求：
	// 直接用 wss:// 会被 net/http 拒绝（unsupported protocol scheme "wss"）。
	u := fmt.Sprintf("%s://%s/api2/json/nodes/%s/qemu/%d/vncproxy",
		httpScheme(scheme), apiHost, url.PathEscape(node), vmid)

	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Cookie", "PVEAuthCookie="+cookie)
	if csrf != "" {
		req.Header.Set("CSRFPreventionToken", csrf)
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("vncproxy HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Data struct {
			Port   interface{} `json:"port"`
			Ticket string      `json:"ticket"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, "", fmt.Errorf("解析 vncproxy 响应失败: %w", err)
	}
	port := toPortInt(out.Data.Port)
	if port <= 0 || out.Data.Ticket == "" {
		return 0, "", fmt.Errorf("vncproxy 响应缺少 port/ticket")
	}
	return port, out.Data.Ticket, nil
}

// httpScheme 把 WebSocket 的 ws/wss 映射为 HTTP 的 http/https。
func httpScheme(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "ws", "http":
		return "http"
	default:
		return "https"
	}
}

// getRealIP 获取客户端真实IP（考虑反向代理）
func getRealIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// SSHConnInfo SSH连接信息
type SSHConnInfo struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	UseKey   bool   `json:"use_key,omitempty"`
}

// SSHTerminalServer SSH终端WebSocket代理服务（ttyd功能）
type SSHTerminalServer struct {
	port     int
	upgrader websocket.Upgrader
	tokens   map[string]*SSHConnInfo
	tokenMu  sync.RWMutex
	srv      *http.Server
	mu       sync.Mutex
	// writeMu 保证同一连接只有一个协程写入：
	// gorilla/websocket 不支持并发写，stdout 与 stderr 两个转发协程
	// 同时写会触发连接状态错乱。
	writeMu sync.Mutex
	// hostKeys 已知主机密钥存储（未注入时不校验，并打印告警）
	hostKeys *security.HostKeyStore
}

// SetHostKeyStore 注入已知主机密钥存储
func (s *SSHTerminalServer) SetHostKeyStore(store *security.HostKeyStore) {
	s.hostKeys = store
}

// hostKeyCallback 返回 SSH 主机密钥校验回调。
//
// 原实现无条件使用 InsecureIgnoreHostKey，无法识别中间人；
// 现在按 OPENIDCS_SSH_HOSTKEY 策略校验（默认 tofu）。
func (s *SSHTerminalServer) hostKeyCallback() ssh.HostKeyCallback {
	policy := security.CurrentSSHHostKeyPolicy()
	if policy == security.SSHHostKeyInsecure {
		log.Printf("[ssh-terminal] 警告：SSH 主机密钥校验已禁用（OPENIDCS_SSH_HOSTKEY=insecure）")
		return ssh.InsecureIgnoreHostKey()
	}
	if s.hostKeys == nil {
		log.Printf("[ssh-terminal] 警告：未注入已知主机密钥存储，本次连接不校验主机密钥")
		return ssh.InsecureIgnoreHostKey()
	}
	return s.hostKeys.Callback(policy)
}

// NewSSHTerminalServer 创建SSH终端服务
func NewSSHTerminalServer(port int) *SSHTerminalServer {
	s := &SSHTerminalServer{
		port:   port,
		tokens: make(map[string]*SSHConnInfo),
		upgrader: websocket.Upgrader{
			CheckOrigin:     checkWSOrigin,
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
	return s
}

// RegisterToken 注册SSH连接Token（有效期30秒，一次性使用）
func (s *SSHTerminalServer) RegisterToken(token string, info *SSHConnInfo) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	s.tokens[token] = info
	// 30秒后自动过期
	go func() {
		time.Sleep(30 * time.Second)
		s.tokenMu.Lock()
		delete(s.tokens, token)
		s.tokenMu.Unlock()
	}()
}

// validateToken 验证并消费Token
func (s *SSHTerminalServer) validateToken(token string) *SSHConnInfo {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	info, ok := s.tokens[token]
	if ok {
		delete(s.tokens, token) // 一次性使用
	}
	return info
}

// Start 启动SSH终端服务器
func (s *SSHTerminalServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.port),
		Handler:           mux,
		ReadHeaderTimeout: 20 * time.Second,
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()

	go func() {
		log.Printf("[ssh-terminal] 启动SSH终端服务，端口: %d", s.port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[ssh-terminal] 启动失败: %v", err)
		}
	}()
}

// Stop 关闭SSH终端服务
func (s *SSHTerminalServer) Stop(ctx context.Context) {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[ssh-terminal] 关闭失败: %v", err)
	}
}

// writeWS 串行化对同一WebSocket连接的写入
func (s *SSHTerminalServer) writeWS(conn *websocket.Conn, messageType int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(messageType, data)
}

// handleWS 处理WebSocket连接，代理SSH终端I/O
func (s *SSHTerminalServer) handleWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "缺少token参数", http.StatusBadRequest)
		return
	}
	connInfo := s.validateToken(token)
	if connInfo == nil {
		http.Error(w, "Token无效或已过期", http.StatusUnauthorized)
		return
	}

	wsConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ssh-terminal] WebSocket升级失败: %v", err)
		return
	}
	defer wsConn.Close()

	// 配置SSH客户端
	sshConfig := &ssh.ClientConfig{
		User: connInfo.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(connInfo.Password),
		},
		HostKeyCallback: s.hostKeyCallback(),
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(connInfo.Host, strconv.Itoa(connInfo.Port))
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		log.Printf("[ssh-terminal] SSH连接失败 %s: %v", addr, err)
		_ = s.writeWS(wsConn, websocket.TextMessage,
			[]byte(fmt.Sprintf("\r\n\x1b[31mSSH连接失败: %v\x1b[0m\r\n", err)))
		return
	}
	defer sshClient.Close()

	// 打开SSH会话
	session, err := sshClient.NewSession()
	if err != nil {
		log.Printf("[ssh-terminal] 创建SSH会话失败: %v", err)
		return
	}
	defer session.Close()

	// 请求PTY（伪终端）
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 40, 80, modes); err != nil {
		log.Printf("[ssh-terminal] PTY请求失败: %v", err)
		return
	}

	stdinPipe, _ := session.StdinPipe()
	stdoutPipe, _ := session.StdoutPipe()
	stderrPipe, _ := session.StderrPipe()

	if err := session.Shell(); err != nil {
		log.Printf("[ssh-terminal] 启动Shell失败: %v", err)
		return
	}

	done := make(chan struct{}, 3)

	// WebSocket -> SSH stdin
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := wsConn.ReadMessage()
			if err != nil {
				return
			}
			// 解析终端大小调整消息（JSON格式：{"cols":80,"rows":24}）
			var resize struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(msg, &resize) == nil && resize.Cols > 0 && resize.Rows > 0 {
				session.WindowChange(resize.Rows, resize.Cols)
				continue
			}
			if _, err := stdinPipe.Write(msg); err != nil {
				return
			}
		}
	}()

	// SSH stdout -> WebSocket
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 8192)
		for {
			n, err := stdoutPipe.Read(buf)
			if err != nil {
				return
			}
			if err := s.writeWS(wsConn, websocket.TextMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// SSH stderr -> WebSocket
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 8192)
		for {
			n, err := stderrPipe.Read(buf)
			if err != nil {
				return
			}
			if err := s.writeWS(wsConn, websocket.TextMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// 等待任意一端断开
	<-done
	log.Printf("[ssh-terminal] 会话结束: %s@%s", connInfo.User, addr)
}
