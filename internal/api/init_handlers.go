package api

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/auth"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/data"
	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
)

const (
	// defaultSystemTitle 未设置系统标题时的默认值
	defaultSystemTitle = "OpenIDCS 虚拟化管理平台"
	// systemTitleSettingKey 系统标题在 system_settings 中的键名
	systemTitleSettingKey = "system_title"
	// adminUsername 初始化时创建的管理员账号
	adminUsername = "admin"
	// initPasswordMinLen 初始化管理员口令的最小长度
	initPasswordMinLen = 8
	// initPasswordMaxLen bcrypt 只接受 72 字节以内的口令，超出部分会被静默截断
	initPasswordMaxLen = 72
	// systemTitleMaxLen 系统标题的最大字符数
	systemTitleMaxLen = 64
)

// SystemTitle 返回当前系统标题，未配置时回落到默认值。
func (app *App) SystemTitle() string {
	settings, err := app.SystemSvc.GetSettings()
	if err != nil {
		return defaultSystemTitle
	}
	if title, ok := settings[systemTitleSettingKey].(string); ok {
		if trimmed := strings.TrimSpace(title); trimmed != "" {
			return trimmed
		}
	}
	return defaultSystemTitle
}

// handleSiteInfo GET /api/public/site-info
//
// 公开接口：登录页与初始化页据此判断是否需要进入初始化流程，
// 并读取系统标题用于品牌展示。不返回任何凭据或用户信息。
func (app *App) handleSiteInfo(c *gin.Context) {
	initialized, err := app.DB.SystemInitialized()
	if err != nil {
		utils.InternalError(c, "获取系统状态失败")
		return
	}
	utils.Success(c, gin.H{
		"initialized":  initialized,
		"system_title": app.SystemTitle(),
	})
}

// handleInitializeSystem POST /api/system/init
//
// 首次部署初始化：创建管理员账号并设置系统标题。
// 仅在系统还没有任何管理员时可用，避免初始化接口被用于重置口令。
func (app *App) handleInitializeSystem(c *gin.Context) {
	var req struct {
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
		SystemTitle     string `json:"system_title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}

	initialized, err := app.DB.SystemInitialized()
	if err != nil {
		utils.InternalError(c, "获取系统状态失败")
		return
	}
	if initialized {
		utils.Fail(c, 409, "系统已完成初始化，请直接登录")
		return
	}

	if strings.TrimSpace(req.Password) == "" {
		utils.BadRequest(c, "管理员密码不能为空")
		return
	}
	if len(req.Password) < initPasswordMinLen {
		utils.BadRequest(c, "管理员密码长度不能少于8个字符")
		return
	}
	if len(req.Password) > initPasswordMaxLen {
		utils.BadRequest(c, "管理员密码长度不能超过72个字符")
		return
	}
	if req.ConfirmPassword != req.Password {
		utils.BadRequest(c, "两次输入的密码不一致")
		return
	}

	title := strings.TrimSpace(req.SystemTitle)
	if title == "" {
		title = defaultSystemTitle
	}
	if utf8.RuneCountInString(title) > systemTitleMaxLen {
		utils.BadRequest(c, "系统标题不能超过64个字符")
		return
	}

	hashed, err := auth.HashPassword(req.Password)
	if err != nil {
		utils.InternalError(c, "密码处理失败")
		return
	}

	if err := app.DB.CreateInitialAdmin(hashed); err != nil {
		if errors.Is(err, data.ErrSystemInitialized) {
			utils.Fail(c, 409, "系统已完成初始化，请直接登录")
			return
		}
		app.Logger.Event("系统", "初始化").IP(auth.GetClientIP(c)).Err(err).
			Errorf("创建初始管理员失败：%v", err)
		utils.InternalError(c, "初始化失败，请稍后重试")
		return
	}

	// 系统标题与初始化标记一并落库：标题在登录页与后台品牌位展示。
	settings, _ := app.SystemSvc.GetSettings()
	if settings == nil {
		settings = make(map[string]interface{})
	}
	settings[systemTitleSettingKey] = title
	if err := app.SystemSvc.UpdateSettings(settings); err != nil {
		// 管理员已创建成功，此时失败只影响标题，不能把整个初始化判为失败，
		// 否则用户会重复提交并收到「已初始化」的困惑提示。
		app.Logger.Event("系统", "初始化").IP(auth.GetClientIP(c)).Err(err).
			Warn("初始化已创建管理员，但保存系统标题失败")
	}

	app.Logger.Event("系统", "初始化").User(adminUsername).IP(auth.GetClientIP(c)).
		Detail("system_title", title).
		Info("系统初始化完成，已创建管理员账号")
	utils.SuccessMsg(c, "初始化完成，请使用管理员账号登录", gin.H{
		"username":     adminUsername,
		"system_title": title,
	})
}
