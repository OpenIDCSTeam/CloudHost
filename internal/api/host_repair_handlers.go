package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
)

// handleHostRepair POST /api/server/repair/:hs_name
//
// 平台运行环境自检与修复（MEmu：MpsSvc 防火墙服务 + Hyper-V 磁盘 ACL）。
// 这部分逻辑原先由 Fix-MEmuHyperV.ps1 完成，内置后无需再手动执行脚本。
func (app *App) handleHostRepair(c *gin.Context) {
	hsName := c.Param("hs_name")

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}
	repairer, ok := d.(driver.PlatformRepairer)
	if !ok {
		utils.BadRequest(c, "当前平台无需修复运行环境")
		return
	}

	start := time.Now()
	msg, err := repairer.RepairPlatform()
	if err != nil {
		app.logEvent(c, "host", "修复运行环境").Host(hsName).Err(err).
			Errorf("修复运行环境失败：%v", err)
		utils.InternalErrorf(c, "修复运行环境失败", err)
		return
	}
	if msg == nil {
		utils.InternalError(c, "修复运行环境失败")
		return
	}
	if !msg.Success {
		utils.Fail(c, 500, msg.Message)
		return
	}

	app.logEvent(c, "host", "修复运行环境").Host(hsName).
		Duration(time.Since(start)).
		Info("运行环境修复完成：%s", msg.Message)
	utils.SuccessMsg(c, msg.Message, msg.Results)
}
