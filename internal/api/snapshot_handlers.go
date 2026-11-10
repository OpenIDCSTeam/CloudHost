// Package api 虚拟机快照 API
package api

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
)

// ============================================================
// 虚拟机快照 API（仅平台支持时可用）
// ============================================================

// snapshotDriver 取出支持快照能力的驱动。
//
// 快照是可选能力：所有驱动都通过 BaseDriver 继承了方法集，
// 因此必须同时检查 SupportsSnapshot，不能只看类型断言是否成功。
func (app *App) snapshotDriver(hsName string) (driver.SnapshotDriver, bool) {
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		return nil, false
	}
	snap, ok := d.(driver.SnapshotDriver)
	if !ok || !snap.SupportsSnapshot() {
		return nil, false
	}
	return snap, true
}

// handleSnapshotList GET /api/client/snapshot/:hs_name/:vm_uuid
func (app *App) handleSnapshotList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	snap, ok := app.snapshotDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机快照")
		return
	}
	if _, err := app.loadVMConfig(hsName, vmUUID); err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	snapshots, err := snap.ListSnapshots(vmUUID)
	if err != nil {
		app.Logger.Log(hsName, "ERROR", "快照", "读取快照列表失败: "+err.Error(), false)
		utils.Fail(c, 502, "获取快照列表失败: "+err.Error())
		return
	}
	utils.Success(c, snapshots)
}

// snapshotCreateRequest 创建快照请求体
type snapshotCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleSnapshotCreate POST /api/client/snapshot/:hs_name/:vm_uuid
func (app *App) handleSnapshotCreate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	snap, ok := app.snapshotDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机快照")
		return
	}
	var req snapshotCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误: "+err.Error())
		return
	}
	name, err := driver.NormalizeSnapshotName(req.Name)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	if err := snap.CreateSnapshot(vmUUID, name, req.Description); err != nil {
		app.Logger.Log(hsName, "ERROR", "快照", "创建快照失败: "+err.Error(), false)
		utils.Fail(c, 502, "创建快照失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "快照创建成功", gin.H{"name": name})
}

// snapshotNameRequest 快照名称请求体（恢复）
type snapshotNameRequest struct {
	Name string `json:"name"`
}

// handleSnapshotRestore POST /api/client/snapshot/:hs_name/:vm_uuid/restore
func (app *App) handleSnapshotRestore(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	snap, ok := app.snapshotDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机快照")
		return
	}
	var req snapshotNameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		utils.BadRequest(c, "缺少快照名称")
		return
	}

	if err := snap.RestoreSnapshot(vmUUID, name); err != nil {
		app.Logger.Log(hsName, "ERROR", "快照", "回滚快照失败: "+err.Error(), false)
		utils.Fail(c, 502, "回滚快照失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "已回滚到指定快照", nil)
}

// handleSnapshotDelete DELETE /api/client/snapshot/:hs_name/:vm_uuid?name=xxx
//
// 快照名走 query 而不是路径段：名称可能包含平台允许的字符，
// 放进路径会与 Gin 的路由匹配冲突。
func (app *App) handleSnapshotDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	snap, ok := app.snapshotDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机快照")
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		utils.BadRequest(c, "缺少快照名称")
		return
	}

	if err := snap.DeleteSnapshot(vmUUID, name); err != nil {
		app.Logger.Log(hsName, "ERROR", "快照", "删除快照失败: "+err.Error(), false)
		utils.Fail(c, 502, "删除快照失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "快照已删除", nil)
}
