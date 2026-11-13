// Package api 主机运维接口：终端、文件、防火墙、进程、服务。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
)

// hostOps 取主机运维能力
func (app *App) hostOps(hsName string) (driver.HostOps, *model.HSConfig, error) {
	cfg, err := app.HostSvc.GetHost(hsName)
	if err != nil {
		return nil, nil, fmt.Errorf("查询主机失败: %w", err)
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("主机不存在")
	}
	if !cfg.EnableHost {
		return nil, nil, fmt.Errorf("主机已禁用，启用后才能执行运维操作")
	}
	return driver.NewHostOps(cfg), cfg, nil
}

// handleHostProcessList GET /api/server/ops/:hs_name/processes
func (app *App) handleHostProcessList(c *gin.Context) {
	ops, _, err := app.hostOps(c.Param("hs_name"))
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	items, listErr := ops.ListProcesses()
	if listErr != nil {
		utils.BadRequest(c, listErr.Error())
		return
	}
	utils.Success(c, gin.H{"os": ops.HostOS(), "items": items})
}

// handleHostProcessKill POST /api/server/ops/:hs_name/processes/kill
func (app *App) handleHostProcessKill(c *gin.Context) {
	hsName := c.Param("hs_name")
	var req struct {
		PID int `json:"pid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	if req.PID <= 1 {
		utils.BadRequest(c, "进程号不合法")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.KillProcess(req.PID); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	app.logEvent(c, "host", "结束进程").Host(hsName).
		Detail("pid", req.PID).Info("结束主机进程：%s/%d", hsName, req.PID)
	utils.SuccessMsg(c, "已发送结束信号", nil)
}

// handleHostServiceList GET /api/server/ops/:hs_name/services
func (app *App) handleHostServiceList(c *gin.Context) {
	ops, _, err := app.hostOps(c.Param("hs_name"))
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	items, listErr := ops.ListServices()
	if listErr != nil {
		utils.BadRequest(c, listErr.Error())
		return
	}
	utils.Success(c, gin.H{"os": ops.HostOS(), "items": items})
}

// handleHostServiceControl POST /api/server/ops/:hs_name/services/control
func (app *App) handleHostServiceControl(c *gin.Context) {
	hsName := c.Param("hs_name")
	var req struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		utils.BadRequest(c, "服务名不能为空")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.ControlService(req.Name, req.Action); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	app.logEvent(c, "host", "服务操作").Host(hsName).
		Detail("service", req.Name).Detail("action", req.Action).
		Info("主机服务操作：%s %s %s", hsName, req.Action, req.Name)
	utils.SuccessMsg(c, "操作已完成", nil)
}

// handleHostFirewallList GET /api/server/ops/:hs_name/firewall
func (app *App) handleHostFirewallList(c *gin.Context) {
	ops, _, err := app.hostOps(c.Param("hs_name"))
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	rules, listErr := ops.ListFirewallRules()
	if listErr != nil {
		utils.BadRequest(c, listErr.Error())
		return
	}
	utils.Success(c, gin.H{"os": ops.HostOS(), "rules": rules})
}

// handleHostFirewallAdd POST /api/server/ops/:hs_name/firewall
func (app *App) handleHostFirewallAdd(c *gin.Context) {
	hsName := c.Param("hs_name")
	var rule driver.HostFirewallRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.AddFirewallRule(rule); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	app.logEvent(c, "host", "添加防火墙规则").Host(hsName).
		Detail("protocol", rule.Protocol).Detail("port", rule.Port).
		Detail("target", rule.Target).Info("添加主机防火墙规则：%s", hsName)
	utils.SuccessMsg(c, "规则已添加", nil)
}

// handleHostFirewallDelete DELETE /api/server/ops/:hs_name/firewall?id=
func (app *App) handleHostFirewallDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		utils.BadRequest(c, "缺少规则标识")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.DeleteFirewallRule(id); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	app.logEvent(c, "host", "删除防火墙规则").Host(hsName).
		Detail("id", id).Info("删除主机防火墙规则：%s/%s", hsName, id)
	utils.SuccessMsg(c, "规则已删除", nil)
}

// handleHostFileList GET /api/server/ops/:hs_name/files?path=
func (app *App) handleHostFileList(c *gin.Context) {
	hsName := c.Param("hs_name")
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	dir := strings.TrimSpace(c.Query("path"))
	if dir == "" {
		if ops.HostOS() == "windows" {
			dir = `C:\`
		} else {
			dir = "/"
		}
	}
	resolved, items, listErr := ops.ListHostDir(dir)
	if listErr != nil {
		utils.BadRequest(c, listErr.Error())
		return
	}
	utils.Success(c, gin.H{
		"os":    ops.HostOS(),
		"path":  resolved,
		"items": items,
	})
}

// handleHostFileUpload POST /api/server/ops/:hs_name/files/upload
func (app *App) handleHostFileUpload(c *gin.Context) {
	hsName := c.Param("hs_name")
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		utils.BadRequest(c, "请求必须是 multipart/form-data 格式")
		return
	}

	dir := ""
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			utils.BadRequest(c, "读取上传数据失败: "+partErr.Error())
			return
		}
		switch part.FormName() {
		case "path":
			buf, _ := io.ReadAll(io.LimitReader(part, 4096))
			dir = strings.TrimSpace(string(buf))
			_ = part.Close()
		case "file":
			rawName := strings.ReplaceAll(strings.TrimSpace(part.FileName()), "\\", "/")
			fileName := path.Base(rawName)
			if fileName == "" || fileName == "." || fileName == "/" {
				_ = part.Close()
				utils.BadRequest(c, "上传文件缺少文件名")
				return
			}
			size, upErr := ops.UploadHostFile(dir, fileName, part)
			_ = part.Close()
			if upErr != nil {
				utils.BadRequest(c, upErr.Error())
				return
			}
			utils.SuccessMsg(c, "上传完成", gin.H{"name": fileName, "size": size})
			return
		default:
			_ = part.Close()
		}
	}
	utils.BadRequest(c, "未找到上传文件")
}

// handleHostFileDownload GET /api/server/ops/:hs_name/files/download?path=&name=
func (app *App) handleHostFileDownload(c *gin.Context) {
	ops, _, err := app.hostOps(c.Param("hs_name"))
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	dir := strings.TrimSpace(c.Query("path"))
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		utils.BadRequest(c, "缺少文件名")
		return
	}
	reader, err := ops.DownloadHostFile(dir, name)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	c.Header("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(c.Writer, reader); err != nil {
		return
	}
}

// handleHostFileMkdir POST /api/server/ops/:hs_name/files/mkdir
func (app *App) handleHostFileMkdir(c *gin.Context) {
	hsName := c.Param("hs_name")
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.CreateHostDir(req.Path, req.Name); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.SuccessMsg(c, "目录已创建", nil)
}

// handleHostFileRename POST /api/server/ops/:hs_name/files/rename
func (app *App) handleHostFileRename(c *gin.Context) {
	hsName := c.Param("hs_name")
	var req struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		NewName string `json:"new_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.RenameHostPath(req.Path, req.Name, req.NewName); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.SuccessMsg(c, "重命名成功", nil)
}

// handleHostFileDelete DELETE /api/server/ops/:hs_name/files?path=&name=
func (app *App) handleHostFileDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	dir := strings.TrimSpace(c.Query("path"))
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		utils.BadRequest(c, "缺少文件名")
		return
	}
	ops, _, err := app.hostOps(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := ops.DeleteHostPath(dir, name); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	app.logEvent(c, "host", "删除主机文件").Host(hsName).
		Detail("path", dir).Detail("name", name).
		Info("删除主机文件：%s %s/%s", hsName, dir, name)
	utils.SuccessMsg(c, "删除成功", nil)
}

// handleHostTerminal POST /api/server/ops/:hs_name/terminal
//
// 生成一次性 SSH 终端票据，前端用返回的 ws_url 连接 7681 端口的终端服务。
func (app *App) handleHostTerminal(c *gin.Context) {
	hsName := c.Param("hs_name")
	cfg, err := app.HostSvc.GetHost(hsName)
	if err != nil || cfg == nil {
		utils.BadRequest(c, "主机不存在")
		return
	}
	if !cfg.EnableHost {
		utils.BadRequest(c, "主机已禁用")
		return
	}
	if app.SSHTerminal == nil || app.SSHPort <= 0 {
		utils.BadRequest(c, "终端服务未启动")
		return
	}

	ops := driver.NewHostOps(cfg)
	if ops.HostOS() == "windows" {
		utils.BadRequest(c, "该平台不提供 Linux Shell 终端")
		return
	}

	host := strings.TrimSpace(cfg.ServerAddr)
	if strings.HasPrefix(host, "ssh://") || host == "" || isLocalHostAddr(host) {
		host = "127.0.0.1"
	}
	port := cfg.ServerPort
	if port <= 0 {
		port = 22
	}
	user := strings.TrimSpace(cfg.ServerUser)
	if user == "" {
		user = "root"
	}

	token, err := randomToken(24)
	if err != nil {
		utils.InternalError(c, "生成终端票据失败")
		return
	}
	app.SSHTerminal.RegisterToken(token, &SSHConnInfo{
		Host:     host,
		Port:     port,
		User:     user,
		Password: cfg.ServerPass,
	})

	scheme := "ws"
	if c.Request.TLS != nil {
		scheme = "wss"
	}
	hostOnly := c.Request.Host
	if h, _, splitErr := net.SplitHostPort(hostOnly); splitErr == nil {
		hostOnly = h
	}
	wsURL := fmt.Sprintf("%s://%s:%d/ws?token=%s", scheme, hostOnly, app.SSHPort, token)

	app.logEvent(c, "host", "打开终端").Host(hsName).
		Detail("user", user).Info("打开主机终端：%s", hsName)
	utils.Success(c, gin.H{
		"ws_url":     wsURL,
		"token":      token,
		"expires_in": 30,
		"user":       user,
	})
}

// isLocalHostAddr 判断地址是否指向本机（与 driver 包的判定保持一致的宽松版本）
func isLocalHostAddr(addr string) bool {
	switch strings.ToLower(strings.TrimSpace(addr)) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

// randomToken 生成随机票据
func randomToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
