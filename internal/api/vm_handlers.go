// Package api 虚拟机管理、网络、存储、备份、设备直通处理器
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/auth"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/logger"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/service"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/task"
	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
)

// ============================================================
// 虚拟机管理 API
// ============================================================

// bindRequestContext 把当前 HTTP 请求的 context 绑定到指定虚拟机。
//
// 驱动内部的平台调用通过 BaseDriver.Ctx(vmUUID) 取用该 context，
// 客户端断开或请求超时后会立刻中断正在等待的平台任务（PVE WaitFor、
// govmomi task.Wait 等），不再出现「用户取消 10 个创建请求、后端仍跑完」
// 的资源堆积。返回值必须在请求结束时调用（defer）。
//
// 注意：只适用于同步处理路径。异步任务（创建/删除等）应改用任务自身的
// context，否则请求一返回就会把正在执行的任务取消。
func (app *App) bindRequestContext(c *gin.Context, hsName, vmUUID string) func() {
	if c == nil || c.Request == nil || vmUUID == "" {
		return func() {}
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		return func() {}
	}
	type contextBinder interface {
		BindContext(string, context.Context) func()
	}
	binder, ok := d.(contextBinder)
	if !ok {
		return func() {}
	}
	return binder.BindContext(vmUUID, c.Request.Context())
}

// loadVMConfig 读取虚拟机配置：数据库为权威，驱动内存覆盖运行时状态。
//
// 与 handleVMList 的策略完全一致（"以数据库为基础，再用驱动内存覆盖运行时状态"），
// 也是 Python 各处理器「server.data_get() 后读 server.vm_saving」的等价实现。
//
// 为什么不能直接用 d.GetVMDetail：
//
//	驱动的 VmSaving 只有两种来源 —— 启动时 SetVMSaving 从数据库恢复，
//	或平台扫描刷新。后者的致命问题在于：web_all（反向代理）、own_all（所有者）、
//	backups 等是 IDCClient 自有的业务元数据，平台（PVE/ESXi/云）根本不认识，
//	一旦平台扫描刷新覆盖了 VmSaving，这些字段就会变成空或被截断的旧值。
//	此时：
//	  - 反向代理刚创建成功、数据库已落库，详情接口却只回旧的 1 条；
//	  - 所有者/权限修改后回读不到；
//	  - 平台临时不可达 / 驱动刚创建时，整片子资源接口统一 404
//	    「虚拟机不存在」，而列表页却能正常显示，表现为
//	    「看得见但点进去所有操作都失败」。
//
// 因此这里改为：数据库记录作为业务字段的权威来源，
// 仅用驱动内存覆盖运行时状态（电源状态、软删除标记、平台扩展数据如 vmid）。
func (app *App) loadVMConfig(hsName, vmUUID string) (*model.VMConfig, error) {
	d := app.HostSvc.GetDriver(hsName)

	var live *model.VMConfig
	if d != nil {
		if cfg, err := d.GetVMDetail(vmUUID); err == nil {
			live = cfg
		}
	}

	// 注意：必须直接读数据库，不能用 HostSvc.MustGetVMConfig ——
	// 后者内部同样是「驱动优先」，会让上面的数据库权威策略失效。
	var base *model.VMConfig
	if raw, err := app.DB.GetVMConfig(hsName, vmUUID); err == nil && raw != "" {
		if cfg, decErr := service.DecodeVMConfig(raw); decErr == nil {
			base = cfg
		}
	}

	switch {
	case base != nil:
		// 用驱动侧的运行时字段覆盖数据库记录
		if live != nil {
			base.VmFlag = live.VmFlag
			base.VmDeleted = live.VmDeleted
			if live.ExtData != nil {
				base.ExtData = live.ExtData
			}
		}
		return base, nil
	case live != nil:
		// 数据库尚无记录（平台新发现、尚未落库）：退回驱动内存
		return live, nil
	default:
		return nil, fmt.Errorf("虚拟机不存在: %s/%s", hsName, vmUUID)
	}
}

// handleVMList GET /api/client/detail/:hs_name
//
// 返回结构与 Python 版 get_vms 一致：{uuid: {uuid, config, status, user_permissions}}。
// 前端通过 vm.config.own_all 判断所有者、通过 vm.status[0] 展示监控数据，
// 因此不能再返回扁平的 VMConfig。
func (app *App) handleVMList(c *gin.Context) {
	hsName := c.Param("hs_name")
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}
	if !uc.IsAdmin && !service.CheckHostAccess(hsName, uc.UserData) {
		utils.Forbidden(c)
		return
	}

	// 禁用主机不返回虚拟机列表（与 Python 版一致）
	if row, err := app.DB.GetHost(hsName); err != nil || row == nil || row.EnableHost == 0 {
		utils.Success(c, map[string]interface{}{})
		return
	}

	// 以数据库为基础，再用驱动内存覆盖运行时状态。
	// 与 Python 版 get_vms 先执行 data_get() 再列出的语义一致：
	// 只依赖驱动内存会在驱动刚创建/平台扫描失败时返回空列表。
	vms := make(map[string]*model.VMConfig)
	if rawMap, err := app.DB.GetVMSaving(hsName); err == nil {
		for uuid, raw := range rawMap {
			if cfg, decodeErr := service.DecodeVMConfig(raw); decodeErr == nil && cfg != nil {
				vms[uuid] = cfg
			}
		}
	}
	if d := app.HostSvc.GetDriver(hsName); d != nil {
		for uuid, live := range d.GetVMSaving() {
			if live == nil {
				continue
			}
			if base, ok := vms[uuid]; ok {
				// 保留数据库中的业务字段，仅同步平台侧运行时状态
				base.VmFlag = live.VmFlag
				base.VmDeleted = live.VmDeleted
				continue
			}
			vms[uuid] = live
		}
	}

	isSuperUser := uc.IsAdmin || uc.IsToken
	result := make(map[string]interface{}, len(vms))
	for uuid, vm := range vms {
		if vm == nil || vm.VmDeleted {
			// 平台侧已不存在（扫描标记软删除）的虚拟机不再返回
			continue
		}
		// 普通用户只能看到自己拥有的虚拟机，
		// 否则同主机内其它用户的系统/VNC 口令会被一并返回。
		if !isSuperUser {
			if vm.OwnAll == nil {
				continue
			}
			if _, owns := vm.OwnAll[uc.Username]; !owns {
				continue
			}
		}
		result[uuid] = app.buildVMView(hsName, uuid, vm, uc)
	}
	utils.Success(c, result)
}

// handleVMDetail GET /api/client/detail/:hs_name/:vm_uuid
//
// 返回结构与 Python 版 get_vm_detail 一致：
// {uuid, config, status, user_permissions, is_admin, current_user}
func (app *App) handleVMDetail(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}

	// 授权由 HostVMAccessMiddleware 统一执行，此处按统一规则复核
	if !app.canOperateVM(uc, hsName, vmUUID) {
		utils.Fail(c, 403, "无权访问此虚拟机")
		return
	}

	vm := app.HostSvc.MustGetVMConfig(hsName, vmUUID)
	if vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	// 存量虚拟机可能没有 vc_pass（早期创建时未保存），
	// 详情页「远程密码」不应为空，这里补齐一次。
	app.ensureVMVncPassword(hsName, vmUUID, vm)

	view := app.buildVMView(hsName, vmUUID, vm, uc)
	view["current_user"] = uc.Username
	utils.Success(c, view)
}

// handleVMCreate POST /api/client/create/:hs_name
func (app *App) handleVMCreate(c *gin.Context) {
	hsName := c.Param("hs_name")
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}

	// 主机访问权限已由 HostVMAccessMiddleware 统一校验，
	// 这里复用以数据库身份为准的检查（原实现读取不存在的 gin 键，恒为空）。
	if !app.HostSvc.CheckHostAccess(uc.Username, hsName) {
		utils.Forbidden(c)
		return
	}

	// 权限检查
	if ok, msg := service.CheckVMPermission("create", uc.UserData); !ok {
		utils.Fail(c, 403, msg)
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}

	vmUUID, _ := req["vm_uuid"].(string)
	if vmUUID == "" {
		utils.BadRequest(c, "虚拟机名称不能为空")
		return
	}

	cfg := model.NewVMConfig()
	cfg.VmUUID = vmUUID
	cfg.VmFlag = model.VMPowerUnknown
	if v, ok := req["vm_name"].(string); ok {
		_ = v // vm_name作为显示名称，存储在vm_uuid中
	}
	if v, ok := req["os_name"].(string); ok {
		cfg.OsName = v
	}
	if v, ok := req["cpu_num"]; ok {
		cfg.CpuNum = toIntVal(v)
	}
	if v, ok := req["mem_num"]; ok {
		cfg.MemNum = toIntVal(v)
	}
	if v, ok := req["hdd_num"]; ok {
		cfg.HddNum = toIntVal(v)
	}
	if v, ok := req["gpu_num"]; ok {
		cfg.GpuMem = toIntVal(v)
	}
	if v, ok := req["speed_u"]; ok {
		cfg.SpeedU = toIntVal(v)
	}
	if v, ok := req["speed_d"]; ok {
		cfg.SpeedD = toIntVal(v)
	}

	// 系统账号与密码：原实现完全没有解析这两个字段，
	// 创建出来的虚拟机 os_pass / vc_pass 恒为空，
	// 表现为「创建时填了密码，详情里却没有」。
	if v, ok := req["os_user"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.OsUser = strings.TrimSpace(v)
	}
	for _, key := range []string{"os_pass", "password", "vm_password"} {
		if v, ok := req[key].(string); ok && v != "" {
			cfg.OsPass = v
			break
		}
	}
	if cfg.OsPass == "" {
		// 前端提示「留空自动生成密码」，这里落实该承诺，
		// 避免创建出来的虚拟机既没有账号密码也没有可展示的初始口令。
		cfg.OsPass = generateVNCPassword()
	}

	// VNC 密码：未显式指定时按规则生成，避免详情页/控制台出现空口令
	if v, ok := req["vc_pass"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.VcPass = strings.TrimSpace(v)
	} else {
		cfg.VcPass = generateVNCPassword()
	}
	switch v := req["vc_port"].(type) {
	case string:
		cfg.VcPort = strings.TrimSpace(v)
	case float64:
		cfg.VcPort = strconv.Itoa(int(v))
	}

	// 补全网卡、NAT、反向代理等字段（从请求中解析）
	if v, ok := req["nic_all"].(map[string]interface{}); ok {
		cfg.NicAll = parseNicAll(v)
	}
	if v, ok := req["nat_all"].([]interface{}); ok {
		cfg.NatAll = parseNatList(v)
	} else if v, ok := req["nat_all"].(map[string]interface{}); ok {
		cfg.NatAll = parseNatListFromMap(v)
	}
	if v, ok := req["web_all"]; ok {
		cfg.WebAll = parseWebAll(v)
	}

	// 设置所有者
	cfg.OwnAll = map[string]model.UserMask{
		uc.Username: model.FullUserMask(),
	}

	// 创建虚拟用户（用于财务系统临时凭据访问）
	// 虚拟用户名格式：{hs_name}-{vm_uuid}
	virtualUser := hsName + "-" + vmUUID
	cfg.OwnAll[virtualUser] = model.VirtualUserMask()

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}

	// 幂等保护：同一 vm_uuid 只允许存在一台虚拟机。
	// 原实现既不查数据库也不查驱动内存，任务完成后重复提交会真的创建第二台
	// （Proxmox 侧拿到新的 VMID，本地记录被覆盖，前一台成为失去纳管的孤儿）。
	if app.HostSvc.VMExists(hsName, vmUUID) {
		utils.Fail(c, 409, "虚拟机已存在: "+vmUUID)
		return
	}
	if _, exists := d.GetVMSaving()[vmUUID]; exists {
		utils.Fail(c, 409, "虚拟机已存在: "+vmUUID)
		return
	}

	// 配额校验：与 Python 版 _validate_vm_resources 等价，在提交任务前拒绝
	if err := app.checkQuotaForUser(uc, cfg); err != nil {
		utils.Fail(c, 400, err.Error())
		return
	}

	params, err := marshalTaskParams(map[string]interface{}{
		taskParamVMConfig:  cfg,
		taskParamQuotaUser: uc.Username,
	})
	if err != nil {
		utils.InternalError(c, "任务参数序列化失败")
		return
	}

	taskID, err := app.submitVMTask("创建虚拟机 "+vmUUID, task.TaskTypeVMCreate,
		uc.Username, hsName, vmUUID, params)
	if err != nil {
		app.logEvent(c, "vm", "提交创建任务").Host(hsName).VM(vmUUID, cfg.OsName).
			Err(err).Errorf("提交虚拟机创建任务失败：%v", err)
		utils.Fail(c, 400, err.Error())
		return
	}
	app.logEvent(c, "vm", "提交创建任务").Host(hsName).VM(vmUUID, cfg.OsName).
		Details(vmSpecDetails(cfg)).
		Detail("task_id", taskID).
		Info("已提交虚拟机创建任务：%s", vmUUID)
	utils.SuccessMsg(c, "虚拟机创建任务已提交", gin.H{"task_id": taskID})
}

// handleVMReinstall POST /api/client/reinstall/:hs_name/:vm_uuid
func (app *App) handleVMReinstall(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}

	if app.HostSvc.GetDriver(hsName) == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}
	if !app.HostSvc.VMExists(hsName, vmUUID) {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	taskID, err := app.submitVMTask("重装系统 "+vmUUID, task.TaskTypeVMReinstall,
		uc.Username, hsName, vmUUID, nil)
	if err != nil {
		app.logEvent(c, "vm", "提交重装任务").Host(hsName).VM(vmUUID, "").
			Err(err).Errorf("提交系统重装任务失败：%v", err)
		utils.Fail(c, 400, err.Error())
		return
	}
	app.logEvent(c, "vm", "提交重装任务").Host(hsName).VM(vmUUID, "").
		Detail("task_id", taskID).
		Info("已提交系统重装任务：%s", vmUUID)
	utils.SuccessMsg(c, "系统重装任务已提交", gin.H{"task_id": taskID})
}

// handleVMUpdate PUT /api/client/update/:hs_name/:vm_uuid
func (app *App) handleVMUpdate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}

	oldVM, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}

	// 更新配置
	newVM := *oldVM
	if v, ok := req["cpu_num"]; ok {
		newVM.CpuNum = toIntVal(v)
	}
	if v, ok := req["mem_num"]; ok {
		newVM.MemNum = toIntVal(v)
	}
	if v, ok := req["hdd_num"]; ok {
		newVM.HddNum = toIntVal(v)
	}
	if v, ok := req["speed_u"]; ok {
		newVM.SpeedU = toIntVal(v)
	}
	if v, ok := req["speed_d"]; ok {
		newVM.SpeedD = toIntVal(v)
	}
	if v, ok := req["os_name"].(string); ok {
		newVM.OsName = v
	}

	// 配额校验：扩容场景必须在提交任务前拒绝
	if err := app.checkQuotaForUser(uc, &newVM); err != nil {
		utils.Fail(c, 400, err.Error())
		return
	}

	params, err := marshalTaskParams(map[string]interface{}{
		taskParamVMConfig:  &newVM,
		taskParamOldConfig: oldVM,
		taskParamQuotaUser: uc.Username,
	})
	if err != nil {
		utils.InternalError(c, "任务参数序列化失败")
		return
	}

	taskID, err := app.submitVMTask("更新虚拟机 "+vmUUID, task.TaskTypeVMUpdate,
		uc.Username, hsName, vmUUID, params)
	if err != nil {
		app.logEvent(c, "vm", "提交更新任务").Host(hsName).VM(vmUUID, newVM.OsName).
			Err(err).Errorf("提交虚拟机更新任务失败：%v", err)
		utils.Fail(c, 400, err.Error())
		return
	}
	app.logEvent(c, "vm", "提交更新任务").Host(hsName).VM(vmUUID, newVM.OsName).
		Detail("old", vmSpecDetails(oldVM)).
		Detail("new", vmSpecDetails(&newVM)).
		Detail("task_id", taskID).
		Info("已提交虚拟机更新任务：%s", vmUUID)
	utils.SuccessMsg(c, "虚拟机更新任务已提交", gin.H{"task_id": taskID})
}

// handleVMDelete DELETE /api/client/delete/:hs_name/:vm_uuid
func (app *App) handleVMDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}

	if ok, _ := service.CheckVMPermission("delete", uc.UserData); !ok {
		utils.Forbidden(c)
		return
	}
	if app.HostSvc.GetDriver(hsName) == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}

	// 与 Python 版一致：删除以最后一个所有者为准；
	// 管理员删除他人虚拟机时必须显式确认主所有者，避免误删。
	vmCfg := app.HostSvc.MustGetVMConfig(hsName, vmUUID)
	if vmCfg == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	primaryOwner := service.PrimaryOwner(hsName, vmUUID, vmCfg.OwnAll)

	if !uc.IsAdmin && primaryOwner != "" && primaryOwner != uc.Username {
		utils.Fail(c, 403, "只有主所有者可以删除该虚拟机")
		return
	}
	if uc.IsAdmin && primaryOwner != "" && primaryOwner != uc.Username {
		confirm := strings.TrimSpace(c.Query("confirm_owner"))
		if confirm == "" {
			if v, ok := c.GetPostForm("confirm_owner"); ok {
				confirm = strings.TrimSpace(v)
			}
		}
		// 前端 axios 把确认值放在 DELETE 请求体里
		// （apis.ts deleteVM: http.delete(url, { data: { confirm_owner } })），
		// 只读 query/form 会永远拿不到，导致管理员无法删除他人虚拟机。
		if confirm == "" {
			var body struct {
				ConfirmOwner string `json:"confirm_owner"`
			}
			if err := c.ShouldBindBodyWith(&body, binding.JSON); err == nil {
				confirm = strings.TrimSpace(body.ConfirmOwner)
			}
		}
		if confirm != primaryOwner {
			// 与 Python 版一致：需要二次确认时返回提示信息而非直接执行
			utils.Fail(c, 400, "该虚拟机属于其它用户，请在请求中携带 confirm_owner="+primaryOwner)
			return
		}
	}

	// record_only=true：只移除本机纳管记录（例如扫描时被错前缀纳入的历史记录），
	// 不提交平台删除任务，因此不会影响平台上真实存在的虚拟机。
	if v := strings.ToLower(strings.TrimSpace(c.Query("record_only"))); v == "1" || v == "true" {
		if d := app.HostSvc.GetOrCreateDriver(hsName); d != nil {
			d.RemoveVMSaving(vmUUID)
		}
		if err := app.DB.DeleteVMConfig(hsName, vmUUID); err != nil {
			utils.InternalErrorf(c, "移除虚拟机记录失败", err)
			return
		}
		_ = app.DB.DeleteVMStatus(hsName, vmUUID)
		// 仅移除纳管记录不会动平台虚拟机，但它会让该虚拟机从本系统「消失」：
		// 用 WARNING 级别留痕（结果仍为成功），便于事后追溯是谁移除的。
		app.logEvent(c, "vm", "移除纳管记录").Host(hsName).VM(vmUUID, "").
			Emit(logger.LevelWarn, "仅移除纳管记录（未删除平台虚拟机）: %s", vmUUID)
		utils.SuccessMsg(c, "已移除虚拟机记录（平台虚拟机未删除）", gin.H{"vm_uuid": vmUUID, "record_only": true})
		return
	}

	params, err := marshalTaskParams(map[string]interface{}{
		taskParamQuotaUser:    primaryOwner,
		taskParamRemoveBackup: true,
	})
	if err != nil {
		utils.InternalError(c, "任务参数序列化失败")
		return
	}

	taskID, err := app.submitVMTask("删除虚拟机 "+vmUUID, task.TaskTypeVMDelete,
		uc.Username, hsName, vmUUID, params)
	if err != nil {
		app.logEvent(c, "vm", "提交删除任务").Host(hsName).VM(vmUUID, "").
			Err(err).Errorf("提交虚拟机删除任务失败：%v", err)
		utils.Fail(c, 400, err.Error())
		return
	}
	// 删除请求附带主所有者信息，便于区分「本人删除」与「管理员代删」
	app.logEvent(c, "vm", "提交删除任务").Host(hsName).VM(vmUUID, "").
		Detail("primary_owner", primaryOwner).
		Detail("task_id", taskID).
		Emit(logger.LevelWarn, "已提交虚拟机删除任务：%s", vmUUID)
	utils.SuccessMsg(c, "虚拟机删除任务已提交", gin.H{"task_id": taskID})
}

// handleVMPowers POST /api/client/powers/:hs_name/:vm_uuid
func (app *App) handleVMPowers(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	var req struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}
	// 电源操作会等待平台任务（软关机最长 30 秒），请求中断时应同步取消
	release := app.bindRequestContext(c, hsName, vmUUID)
	defer release()

	action, ok := normalizePowerAction(req.Action)
	if !ok {
		utils.BadRequest(c, fmt.Sprintf("不支持的电源操作: %s", req.Action))
		return
	}
	result, err := d.PowerControl(vmUUID, action)
	if err != nil {
		app.logEvent(c, "vm", "电源操作").Host(hsName).VM(vmUUID, "").
			Detail("power_action", string(action)).Err(err).
			Errorf("虚拟机电源操作失败：%s (%v)", action, err)
		utils.InternalError(c, "电源操作失败")
		return
	}
	if !result.Success {
		app.logEvent(c, "vm", "电源操作").Host(hsName).VM(vmUUID, "").
			Detail("power_action", string(action)).
			Detail("platform_message", result.Message).
			Errorf("虚拟机电源操作被平台拒绝：%s", result.Message)
		utils.Fail(c, 500, result.Message)
		return
	}

	// 更新数据库中的VM状态
	vm, _ := app.loadVMConfig(hsName, vmUUID)
	if vm != nil {
		_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)
	}

	// 电源状态变化后旧截图必然失效，丢弃缓存避免前端继续看到关机画面
	invalidateScreenshotCache(hsName, vmUUID)

	// 软关机 / 软重启是异步操作：平台受理后状态不会立刻变化，
	// 需要后台监控直到实际状态稳定（对应 Python 版 BasicServer.soft_pwr）。
	if action == model.VMPowerSClose || action == model.VMPowerSReset {
		type softPowerStarter interface {
			SoftPowerMonitor(vmUUID string, expected model.VMPowers)
		}
		if sp, ok := d.(softPowerStarter); ok {
			sp.SoftPowerMonitor(vmUUID, model.VMPowerOnStop)
		}
	}

	app.logEvent(c, "vm", "电源操作").Host(hsName).VM(vmUUID, "").
		Detail("power_action", string(action)).
		Info("虚拟机电源操作已执行：%s", action)
	utils.SuccessMsg(c, "电源操作已执行", nil)
}

// normalizePowerAction 把外部传入的电源操作归一化为驱动使用的指令常量。
//
// 驱动（Proxmox / Hyper-V 等）只认 model 里的 S_START / S_CLOSE /
// S_RESET / H_CLOSE / H_RESET / A_PAUSE / A_WAKED；而前端部分页面发送的是
// 语义值（start / stop / hard_stop / reset / pause / resume），直接透传会
// 全部落到驱动的 default 分支，报「不支持的电源操作」。
//
// 归一化放在 API 入口而不是各驱动内：所有调用方（前端各页面、第三方 API）
// 都会经过这里，避免逐平台重复实现、也避免新驱动再踩同一个坑。
func normalizePowerAction(raw string) (model.VMPowers, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "s_start", "start", "on", "boot", "poweron":
		return model.VMPowerSStart, true
	case "s_close", "stop", "shutdown", "poweroff":
		return model.VMPowerSClose, true
	case "h_close", "hard_stop", "hardstop", "force_stop", "forceshutdown":
		return model.VMPowerHClose, true
	case "s_reset", "reset", "reboot":
		return model.VMPowerSReset, true
	case "h_reset", "hard_reset", "hardreset", "force_reset", "forcereset":
		return model.VMPowerHReset, true
	case "a_pause", "pause", "suspend":
		return model.VMPowerAPause, true
	case "a_waked", "resume", "wake":
		return model.VMPowerAWaked, true
	}
	return model.VMPowerUnknown, false
}

// handleVMRemote GET /api/client/remote/:hs_name/:vm_uuid
//
// 返回结构：
//
//	{ console_url, terminal_url, type, host, port, password, ... }
//
// Python 版 VMRemote 返回的是「可直接打开的 noVNC 地址」字符串
// （形如 http://<public>:<remote_port>/vnc.html?autoconnect=true&path=websockify?token=xxx），
// 前端读取 `data.console_url` 后直接 window.open。
// 此前 Go 版只把驱动的 Results({type,host,port,password}) 原样返回，
// 前端取 console_url 恒为 undefined，表现为「获取的控制台地址无效」。
// 这里在保留原始字段的前提下，额外组装 console_url / terminal_url。
func (app *App) handleVMRemote(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}
	release := app.bindRequestContext(c, hsName, vmUUID)
	defer release()

	ipAddr := c.ClientIP()
	result, err := d.GetVNCInfo(vmUUID, ipAddr)
	if err != nil {
		utils.InternalError(c, "获取控制台信息失败")
		return
	}
	if !result.Success {
		utils.Fail(c, 500, result.Message)
		return
	}

	// ZMessage.Results 是 interface{}：按 Python 契约应为 dict，
	// 但个别驱动可能返回字符串（直接给地址）或其它形态，这里统一归一化。
	results := map[string]interface{}{}
	switch v := result.Results.(type) {
	case map[string]interface{}:
		results = v
	case string:
		// 驱动直接返回了控制台地址（如 VMware/Proxmox 的 noVNC 链接）
		if strings.HasPrefix(v, "http") {
			results["console_url"] = v
		}
	default:
		// 尝试通过 JSON 往返转换（如 struct 形态）
		if raw, err := json.Marshal(v); err == nil {
			_ = json.Unmarshal(raw, &results)
		}
	}

	// 保留驱动原始结果（type/host/port/password/ticket 等）
	out := make(map[string]interface{}, len(results)+4)
	for k, v := range results {
		out[k] = v
	}
	// 驱动可能直接给 message 就是地址（Python 版 VMRemote 的返回风格）
	if _, ok := out["console_url"]; !ok {
		if m := strings.TrimSpace(result.Message); strings.HasPrefix(m, "http") {
			out["console_url"] = m
		}
	}

	// 拼装 noVNC 控制台地址
	if _, ok := out["console_url"]; !ok {
		if consoleURL := app.buildConsoleURL(c, d, hsName, vmUUID, results); consoleURL != "" {
			out["console_url"] = consoleURL
		}
	}

	// 仍然没有地址时给出可读原因：前端统一提示「地址无效」会把
	// 「平台只提供命令行控制台」和「VNC 未启用」混为一谈，难以排查。
	if _, ok := out["console_url"]; !ok {
		out["console_error"] = consoleUnavailableReason(results)
	}

	utils.Success(c, out)
}

// consoleUnavailableReason 说明为什么没有可用的图形控制台地址。
func consoleUnavailableReason(results map[string]interface{}) string {
	typ, _ := results["type"].(string)
	switch strings.ToLower(typ) {
	case "tty":
		return "该平台仅提供命令行控制台，请使用终端入口"
	case "vmconnect":
		return "Hyper-V 需使用 VMConnect 客户端连接，浏览器控制台不可用"
	case "vmrc", "webmks":
		return "ESXi 需使用 VMRC 客户端连接，浏览器控制台不可用"
	}
	if toPortInt(results["port"]) <= 0 {
		return "平台未返回可用的控制台端口，请确认虚拟机已启用 VNC"
	}
	return "无法生成控制台地址"
}

// consoleHostFromRequest 推导控制台对外可访问的主机名。
//
// 优先级：请求 Host（生产同源部署时最准确） > 主机配置 public_addr[0]。
// 需要去掉端口部分，因为我们只取主机名再拼 remote_port。
func consoleHostFromRequest(c *gin.Context, cfg *model.HSConfig) string {
	if c != nil && c.Request != nil && c.Request.Host != "" {
		if h, _, err := net.SplitHostPort(c.Request.Host); err == nil && h != "" {
			return h
		}
		return c.Request.Host
	}
	if cfg != nil {
		for _, a := range cfg.PublicAddr {
			if a != "" && a != "localhost" && a != "127.0.0.1" {
				return a
			}
		}
		if len(cfg.PublicAddr) > 0 {
			return cfg.PublicAddr[0]
		}
	}
	return "127.0.0.1"
}

// buildConsoleURL 组装 noVNC 访问地址（与 Python VMRemote 行为一致）。
//
// 当驱动直接给出了可用地址（Results.url / console_url）时优先采用；
// 否则依据 host + port 注册一次性 token 并拼装。
//
// port 的取值类型在各驱动间并不统一：Proxmox 返回命名类型
// proxmox.StringOrInt、ESXi 返回 int32、部分驱动返回字符串。
// 早期只认 int/int64/float64，这些平台的地址会恒为空，
// 前端表现为「获取的控制台地址无效」，因此统一走 toPortInt。
func (app *App) buildConsoleURL(c *gin.Context, d driver.HostDriver, hsName, vmUUID string, results map[string]interface{}) string {
	// 1. 驱动已给出完整地址
	for _, key := range []string{"console_url", "url"} {
		if v, ok := results[key].(string); ok && strings.HasPrefix(v, "http") {
			return v
		}
	}

	// ESXi 的票据走 WebMKS（wss），websockify 只能做 TCP→VNC 转发，
	// 拼出 noVNC 地址只会得到黑屏，如实交给 console_error 说明限制。
	if typ, _ := results["type"].(string); strings.EqualFold(typ, "vmrc") || strings.EqualFold(typ, "webmks") {
		return ""
	}

	cfg := d.GetConfig()

	// VNC 对外地址可能经过反向代理：
	//   vnc_wan 完整对外地址（host[:port]）优先；
	//   vnc_ip  仅替换主机名（端口仍用本地 6080）。
	consoleHost, consolePort := consoleHostFromRequest(c, cfg), vncProxyPort
	if cfg != nil {
		if v := strings.TrimSpace(cfg.VncWan); v != "" {
			if h, p := splitHostPortLoose(v); h != "" {
				consoleHost = h
				if p > 0 {
					consolePort = p
				}
			}
		} else if v := strings.TrimSpace(cfg.VncIP); v != "" {
			consoleHost = v
		}
	}

	// Proxmox：走官方 vncwebsocket 通道。
	// vncproxy 返回的端口只绑在节点本地，且该通道用 ticket 鉴权、
	// 不需要（也没有）VNC 密码。原实现按裸 TCP 连接这个端口，
	// 并把这串 ticket 当作 VNC 密码塞给 noVNC，
	// 表现为「连不上」或「no valid credentials available」。
	if typ, _ := results["type"].(string); strings.EqualFold(typ, "proxmox") {
		apiHost := strings.TrimSpace(strOf(results["api_host"]))
		node := strings.TrimSpace(strOf(results["node"]))
		vmid := toPortInt(results["vmid"])
		vncPort := toPortInt(results["port"])
		ticket := strOf(results["ticket"])
		password := strOf(results["password"])
		cookie := strOf(results["cookie"])
		csrf := strOf(results["csrf"])
		if apiHost == "" || node == "" || vmid <= 0 || vncPort <= 0 || ticket == "" || cookie == "" {
			return ""
		}
		token := registerPVEVNCToken(hsName, vmUUID, apiHost, node, vmid, vncPort, ticket, cookie, csrf)
		// password 是 QEMU 的 VNC 密码（ticket 前 8 位），不带就过不了认证
		return composeConsoleURL("http", consoleHost, consolePort, token, password)
	}

	host := ""
	if v, ok := results["host"].(string); ok {
		host = strings.TrimSpace(v)
	}
	// 驱动未给 host 时退回主机地址：这里是后端连接 VNC 服务的目标，
	// 与浏览器访问 6080 用的 consoleHost 不是同一个视角。
	if host == "" && cfg != nil {
		host = strings.TrimSpace(cfg.ServerAddr)
	}
	port := toPortInt(results["port"])
	if host == "" || port <= 0 {
		return ""
	}

	// 其余平台（QEMU/VirtualBox 等）直连裸 VNC 端口，密码可能必需
	password := ""
	for _, key := range []string{"password", "ticket"} {
		if v, ok := results[key].(string); ok && v != "" {
			password = v
			break
		}
	}

	// 注册一次性 token，供 6080 的 /websockify 校验
	token := registerVNCBridgeToken(hsName, vmUUID, host, port)
	return composeConsoleURL("http", consoleHost, consolePort, token, password)
}

// splitHostPortLoose 解析「host[:port]」，容忍带 scheme 与末尾斜杠的写法。
func splitHostPortLoose(v string) (string, int) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "http://"), "https://")
	v = strings.TrimPrefix(strings.TrimPrefix(v, "ws://"), "wss://")
	v = strings.TrimSuffix(v, "/")
	if h, p, err := net.SplitHostPort(v); err == nil {
		if n, convErr := strconv.Atoi(p); convErr == nil {
			return h, n
		}
		return h, 0
	}
	return v, 0
}

// generateVNCPassword 生成 8 位随机 VNC 密码（去掉易混淆字符）。
func generateVNCPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%08d", time.Now().UnixNano()%100000000)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// strOf 取字符串结果字段（不存在时返回空串）。
func strOf(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// composeConsoleURL 拼接 noVNC 页面地址。
//
// token 必须拼进 path（即 `path=websockify?token=xxx`）而不是作为独立查询参数：
// noVNC 只把 path 当 WebSocket 路径透传，独立参数不会进入握手请求，
// 6080 的 /websockify 收不到 token 会直接 401，表现为「VNC 页面能打开但连不上」。
// 这与 Python 版 get_url 的形式一致。
func composeConsoleURL(scheme, host string, proxyPort int, token, password string) string {
	// 只对整段 path 做一次编码：token 在内层再次编码会变成双重转义，
	// noVNC 解出来就不是原始 token 了。
	wsPath := "websockify"
	if token != "" {
		wsPath += "?token=" + token
	}
	u := fmt.Sprintf("%s://%s:%d/vnc.html?autoconnect=true&path=%s",
		scheme, host, proxyPort, url.QueryEscape(wsPath))
	if password != "" {
		u += "&password=" + url.QueryEscape(password)
	}
	return u
}

// toPortInt 把驱动返回的端口值统一转成 int，无法识别或非正数时返回 0。
// 用 reflect 覆盖所有整数种类与命名类型（如 proxmox.StringOrInt、int32），
// 否则这些平台拼不出控制台地址。
func toPortInt(v interface{}) int {
	switch n := v.(type) {
	case nil:
		return 0
	case string:
		if p, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && p > 0 {
			return p
		}
		return 0
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if p := rv.Int(); p > 0 {
			return int(p)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if p := rv.Uint(); p > 0 {
			return int(p)
		}
	case reflect.Float32, reflect.Float64:
		if p := rv.Float(); p > 0 {
			return int(p)
		}
	case reflect.String:
		if p, err := strconv.Atoi(strings.TrimSpace(rv.String())); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

// handleVMScreenshot GET /api/client/screenshot/:hs_name/:vm_uuid
func (app *App) handleVMScreenshot(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}

	release := app.bindRequestContext(c, hsName, vmUUID)
	defer release()

	cacheKey := hsName + "/" + vmUUID
	if cached, ok := getCachedScreenshot(cacheKey); ok {
		utils.Success(c, gin.H{"screenshot": normalizeScreenshot(cached), "cached": true})
		return
	}

	screenshot, err := d.GetScreenshot(vmUUID)
	if err != nil {
		// 截图失败原因必须透出：前端只显示「获取截图失败」时，
		// 无法区分是虚拟机未运行、SSH 不通还是平台不支持，排查成本极高。
		app.Logger.Log(hsName, "WARNING", "虚拟机截图", "截图失败: "+err.Error(), false)
		utils.Fail(c, 502, "获取截图失败: "+err.Error())
		return
	}
	if strings.TrimSpace(screenshot) != "" {
		setCachedScreenshot(cacheKey, screenshot)
	}
	utils.Success(c, gin.H{"screenshot": normalizeScreenshot(screenshot)})
}

// normalizeScreenshot 统一截图返回格式为纯 base64（不含 data: 前缀）。
//
// 各驱动历史上返回两种形态：裸 base64（Python 契约）与 data URL。
// 前端固定按 data:image/png;base64,<base64> 拼接，多一层前缀会让
// 图片解码失败、界面只显示占位图，因此在这里收敛。
func normalizeScreenshot(raw string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "base64,"); idx >= 0 {
		return raw[idx+len("base64,"):]
	}
	return raw
}

// handleVMPassword POST /api/client/password/:hs_name/:vm_uuid
//
// 前端按 type 区分三类修改：os_password（默认）、vnc_password、vnc_port。
// 原实现只有 password 一个字段，改「远程密码（VNC）」时会被当成系统密码
// 下发给来宾系统，既改了错误的对象，vc_pass 也不会被保存。
func (app *App) handleVMPassword(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	var req struct {
		Type        string `json:"type"`
		Password    string `json:"password"`
		VncPassword string `json:"vnc_password"`
		VncPort     *int   `json:"vnc_port"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}

	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil || vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	release := app.bindRequestContext(c, hsName, vmUUID)
	defer release()

	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case "vnc_password":
		pwd := strings.TrimSpace(req.Password)
		if pwd == "" {
			pwd = strings.TrimSpace(req.VncPassword)
		}
		if pwd == "" {
			utils.BadRequest(c, "密码不能为空")
			return
		}
		vm.VcPass = pwd
		if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
			utils.InternalError(c, "保存VNC密码失败")
			return
		}
		utils.SuccessMsg(c, "远程密码设置成功", nil)
		return
	case "vnc_port":
		if req.VncPort == nil {
			utils.BadRequest(c, "端口不能为空")
			return
		}
		vm.VcPort = strconv.Itoa(*req.VncPort)
		if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
			utils.InternalError(c, "保存VNC端口失败")
			return
		}
		utils.SuccessMsg(c, "远程端口设置成功", nil)
		return
	}

	// 系统密码：下发到平台/来宾系统
	if req.Password == "" {
		utils.BadRequest(c, "密码不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}
	result, err := d.SetPassword(vmUUID, req.Password)
	if err != nil {
		utils.InternalError(c, "设置密码失败")
		return
	}
	if result != nil && !result.Success {
		utils.Fail(c, 500, result.Message)
		return
	}
	// 下发成功后同步记录，避免详情页仍显示旧密码
	vm.OsPass = req.Password
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		utils.Fail(c, 500, "密码已修改但保存记录失败，请联系管理员核对")
		return
	}
	utils.SuccessMsg(c, "密码设置成功", nil)
}

// ensureVMVncPassword 为历史虚拟机补齐 VNC 密码。
//
// 早期版本创建时没有保存 vc_pass，详情页「远程密码」显示为空。
// 这里在读取详情时补齐并落库（只补一次，后续稳定返回同一值）。
func (app *App) ensureVMVncPassword(hsName, vmUUID string, vm *model.VMConfig) {
	if vm == nil || strings.TrimSpace(vm.VcPass) != "" {
		return
	}
	vm.VcPass = generateVNCPassword()

	// 必须同时写回驱动内存：详情读取优先取驱动内的配置，
	// 只写数据库会导致「每次刷新都生成一个新密码」。
	if d := app.HostSvc.GetDriver(hsName); d != nil {
		d.UpsertVMSaving(vmUUID, vm)
	}
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		app.Logger.Log(hsName, "WARNING", "虚拟机配置", "补充VNC密码失败: "+err.Error(), false)
	}
}

// handleVMStatus GET /api/client/status/:hs_name/:vm_uuid
//
// 返回结构与 Python get_vm_status 一致：
//
//	{ power_status: "STARTED", history: [ {HWStatus}, ... ] }
//
// 此前返回的是裸数组，前端两处消费因此失效：
//   - DockDetail.tsx 读 data.power_status 恒为 undefined（电源态刷新逻辑失效）；
//   - DockDetail.tsx loadMonitorData 读 data?.history 恒为 undefined，
//     导致性能/资源/网络监控图表永远为空。
//
// 时间范围：前端传 ?limit=<分钟>（Python 同名字段），
// 同时兼容显式的 start/end 时间戳。
func (app *App) handleVMStatus(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	startTS := int64(0)
	endTS := int64(0)
	if s := c.Query("start"); s != "" {
		startTS, _ = strconv.ParseInt(s, 10, 64)
	}
	if e := c.Query("end"); e != "" {
		endTS, _ = strconv.ParseInt(e, 10, 64)
	}
	// limit=分钟数（默认 30 分钟），与 Python request.args.get('limit', type=int, default=30) 对齐
	if startTS == 0 {
		limitMin := 30
		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limitMin = n
			}
		}
		now := time.Now().Unix()
		startTS = now - int64(limitMin)*60
		if endTS == 0 {
			endTS = now
		}
	}

	statusList, err := app.DB.GetVMStatus(hsName, vmUUID, startTS, endTS)
	if err != nil {
		utils.InternalError(c, "获取虚拟机状态失败")
		return
	}

	// 只保留「上报数据」：调度器每轮采集也会写一条
	// {"vm_state":"running","on_update":...} 到同一张表，
	// 这类记录没有任何资源指标，混进来会让前端的「最新状态」取到空值，
	// 看起来像「上报了但后端看不到」。
	history := make([]interface{}, 0, len(statusList))
	for _, s := range statusList {
		var data interface{}
		if err := json.Unmarshal([]byte(s), &data); err != nil {
			continue
		}
		if m, ok := data.(map[string]interface{}); ok {
			_, hasCPU := m["cpu_total"]
			_, hasMem := m["mem_total"]
			_, hasAC := m["ac_status"]
			if !hasCPU && !hasMem && !hasAC {
				continue
			}
		}
		history = append(history, data)
	}

	// DB 查询按 on_update DESC 取最近 100 条，但前端与图表都按升序消费：
	//   - DockDetail 用 status[length-1] 当作「最新状态」；
	//   - 监控图表按数组顺序绘制时间轴。
	// 不反转会出现「最新状态取到最旧的一条」「曲线左右颠倒」。
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	// 最新电源状态取自 VMConfig.vm_flag（与 Python 一致）
	powerStatus := string(model.VMPowerUnknown)
	if vmCfg := app.HostSvc.MustGetVMConfig(hsName, vmUUID); vmCfg != nil && vmCfg.VmFlag != "" {
		powerStatus = string(vmCfg.VmFlag)
	}

	utils.Success(c, map[string]interface{}{
		"power_status": powerStatus,
		"history":      history,
	})
}

// parseNicAll 解析网卡配置（map: 网卡名 -> 配置）
func parseNicAll(data map[string]interface{}) map[string]model.NCConfig {
	result := make(map[string]model.NCConfig, len(data))
	for name, v := range data {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		var nic model.NCConfig
		if b, err := json.Marshal(m); err == nil {
			_ = json.Unmarshal(b, &nic)
		}
		if nic.NicName == "" {
			nic.NicName = name
		}
		result[name] = nic
	}
	return result
}

// parseNatList 解析NAT端口转发列表（数组形式）
func parseNatList(list []interface{}) []model.PortData {
	result := make([]model.PortData, 0, len(list))
	for _, v := range list {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		var pd model.PortData
		if b, err := json.Marshal(m); err == nil {
			_ = json.Unmarshal(b, &pd)
		}
		result = append(result, pd)
	}
	return result
}

// parseNatListFromMap 解析NAT端口转发（map 形式）
func parseNatListFromMap(data map[string]interface{}) []model.PortData {
	list := make([]interface{}, 0, len(data))
	for _, v := range data {
		list = append(list, v)
	}
	return parseNatList(list)
}

// parseWebAll 解析反向代理配置（数组或 map 形式）
func parseWebAll(data interface{}) []model.WebProxy {
	var list []interface{}
	switch v := data.(type) {
	case []interface{}:
		list = v
	case map[string]interface{}:
		for _, item := range v {
			list = append(list, item)
		}
	default:
		return nil
	}

	result := make([]model.WebProxy, 0, len(list))
	for _, v := range list {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		var web model.WebProxy
		if b, err := json.Marshal(m); err == nil {
			_ = json.Unmarshal(b, &web)
		}
		result = append(result, web)
	}
	return result
}

// handleVMScan POST /api/client/scaner/:hs_name
//
// 与 Python 版 VScanner 语义对齐，并修复原先的两个缺陷：
//  1. 扫描结果**持久化到数据库**。原实现只写入驱动内存，
//     一旦进程重启或列表接口走数据库分支，扫描到的虚拟机就会消失，
//     表现为「扫描不到虚拟机」。
//  2. 返回结构化统计（识别/新增/落库/恢复数量），并对失败原因可见：
//     平台命令失败时驱动会静默返回内存列表，导致「扫描完成：0 台」无法排查。
func (app *App) handleVMScan(c *gin.Context) {
	hsName := c.Param("hs_name")

	row, err := app.DB.GetHost(hsName)
	if err != nil || row == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	if row.EnableHost == 0 {
		utils.BadRequest(c, "主机已被禁用，无法扫描虚拟机")
		return
	}

	d := app.HostSvc.GetOrCreateDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在或未启用")
		return
	}

	msg, scanErr := d.ScanVMs()
	if scanErr != nil {
		utils.InternalErrorf(c, "扫描虚拟机失败", scanErr)
		return
	}
	if msg != nil && !msg.Success {
		utils.Fail(c, 500, msg.Message)
		return
	}

	// 平台侧枚举结果（含内存态，用于恢复此前被标记删除的虚拟机）
	platformVMs, listErr := d.GetVMList()
	if listErr != nil {
		utils.InternalErrorf(c, "读取平台虚拟机列表失败", listErr)
		return
	}

	snapshot := d.GetVMSaving()
	recovered := 0
	for uuid, vm := range snapshot {
		if vm == nil || !vm.VmDeleted {
			continue
		}
		if _, exists := platformVMs[uuid]; exists {
			d.SetVMDeleted(uuid, false)
			recovered++
		}
	}

	// 扫描结果必须落库：这是「扫描后能看到虚拟机」的关键一步
	persisted := 0
	for uuid, vm := range d.GetVMSaving() {
		if vm == nil {
			continue
		}
		if err := app.DB.UpsertVMConfig(hsName, uuid, vm); err != nil {
			app.Logger.Log(hsName, "ERROR", "扫描虚拟机",
				fmt.Sprintf("保存虚拟机 %s 失败: %v", uuid, err), false)
			continue
		}
		persisted++
	}

	added := 0
	if msg != nil {
		if results, ok := msg.Results.(map[string]interface{}); ok {
			if v, ok := results["added"].(int); ok {
				added = v
			}
		}
	}

	app.Logger.Log(hsName, "INFO", "扫描虚拟机",
		fmt.Sprintf("扫描完成：识别 %d 台，落库 %d 台，恢复 %d 台", len(platformVMs), persisted, recovered), true)

	utils.SuccessMsg(c, "扫描完成", gin.H{
		"scanned":       len(platformVMs),
		"added":         added,
		"persisted":     persisted,
		"recovered":     recovered,
		"prefix_filter": row.FilterName,
	})
}

// handleVMCleanup POST /api/client/scaner/:hs_name/cleanup
//
// 清理该主机下「不匹配 filter_name 前缀」的历史纳管记录。
//
// 背景：早期版本的 Proxmox / ESXi / Hyper-V / Workstation 驱动在扫描时
// 没有按 filter_name 过滤，把平台上其它虚拟机也纳入了本机；
// 修复扫描逻辑后，这些旧记录仍留在数据库与驱动内存中。
//
// 该接口**只删除本机记录**（vm_saving / vm_status 与驱动内存），
// 不做任何平台操作，因此不会影响平台上真实存在的虚拟机。
// ?dry_run=true 只预览将删除的记录，供界面二次确认。
func (app *App) handleVMCleanup(c *gin.Context) {
	hsName := c.Param("hs_name")

	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}
	if ok, _ := service.CheckVMPermission("delete", uc.UserData); !ok {
		utils.Forbidden(c)
		return
	}
	if !uc.IsAdmin && !service.CheckHostAccess(hsName, uc.UserData) {
		utils.Forbidden(c)
		return
	}

	row, err := app.DB.GetHost(hsName)
	if err != nil || row == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	prefix := strings.TrimSpace(row.FilterName)
	if prefix == "" {
		// 前缀为空时「不匹配」没有判定依据，拒绝执行以避免误删全部记录
		utils.BadRequest(c, "主机未配置「过滤器名称」，为避免误删暂不执行清理")
		return
	}

	// 候选集合 = 数据库记录 ∪ 驱动内存记录
	candidates := make(map[string]*model.VMConfig)
	rawMap, err := app.DB.GetVMSaving(hsName)
	if err != nil {
		utils.InternalErrorf(c, "读取虚拟机记录失败", err)
		return
	}
	for uuid, raw := range rawMap {
		cfg, decodeErr := service.DecodeVMConfig(raw)
		if decodeErr != nil || cfg == nil {
			// 无法解析的记录保守保留，避免误删
			continue
		}
		candidates[uuid] = cfg
	}

	d := app.HostSvc.GetOrCreateDriver(hsName)
	if d != nil {
		for uuid, cfg := range d.GetVMSaving() {
			if cfg == nil {
				continue
			}
			if _, exists := candidates[uuid]; !exists {
				candidates[uuid] = cfg
			}
		}
	}

	removedList := make([]string, 0, len(candidates))
	for uuid, cfg := range candidates {
		if driver.MatchFilterPrefix(prefix, cfg) {
			continue
		}
		removedList = append(removedList, uuid)
	}
	sort.Strings(removedList)

	if v := strings.ToLower(strings.TrimSpace(c.Query("dry_run"))); v == "1" || v == "true" {
		utils.Success(c, gin.H{
			"host_name":     hsName,
			"prefix_filter": prefix,
			"count":         len(removedList),
			"items":         removedList,
			"dry_run":       true,
		})
		return
	}

	removed := 0
	for _, uuid := range removedList {
		if err := app.DB.DeleteVMConfig(hsName, uuid); err != nil {
			app.Logger.Log(hsName, "ERROR", "清理虚拟机记录",
				fmt.Sprintf("删除记录 %s 失败: %v", uuid, err), false)
			continue
		}
		_ = app.DB.DeleteVMStatus(hsName, uuid)
		if d != nil {
			d.RemoveVMSaving(uuid)
		}
		removed++
	}
	// 顺带清理 vm_status 中已无归属记录的孤儿数据
	if n, cleanupErr := app.DB.CleanupDeletedVMStatus(); cleanupErr != nil {
		app.Logger.Log(hsName, "WARN", "清理虚拟机记录",
			fmt.Sprintf("清理孤儿状态记录失败: %v", cleanupErr), false)
	} else if n > 0 {
		app.Logger.Log(hsName, "INFO", "清理虚拟机记录",
			fmt.Sprintf("清理孤儿状态记录 %d 条", n), false)
	}

	app.Logger.Log(hsName, "INFO", "清理虚拟机记录",
		fmt.Sprintf("按前缀 %q 清理 %d 条不匹配记录（仅记录，未删除平台虚拟机）", prefix, removed), true)

	utils.SuccessMsg(c, fmt.Sprintf("已清理 %d 条不匹配前缀的记录", removed), gin.H{
		"host_name":     hsName,
		"prefix_filter": prefix,
		"removed":       removed,
		"items":         removedList,
	})
}

// vmConfigScore 评估虚拟机记录的业务数据完整度，用于合并重复记录时决定保留哪条。
func vmConfigScore(cfg *model.VMConfig) int {
	if cfg == nil {
		return -1
	}
	score := 0
	if len(cfg.OwnAll) > 0 {
		score += 10
	}
	if cfg.OsName != "" {
		score += 5
	}
	if cfg.OsPass != "" {
		score += 3
	}
	if len(cfg.NicAll) > 0 {
		score += 3
	}
	if cfg.VmDeleted {
		score--
	}
	return score + 3*len(cfg.Backups) + 2*(len(cfg.NatAll)+len(cfg.WebAll)+
		len(cfg.HddAll)+len(cfg.IsoAll)+len(cfg.PciAll)+len(cfg.UsbAll))
}

// mergeVMConfigInto 把 src 中「dst 尚缺」的信息补进 dst（dst 优先，不覆盖已有值）。
//
// 合并重复记录时，被删除的一条往往带有另一条缺失的业务字段
// （所有者、端口映射、备份列表等），这里逐字段互补以避免丢数据。
func mergeVMConfigInto(dst, src *model.VMConfig) {
	if dst == nil || src == nil {
		return
	}
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	t := dv.Type()
	for i := 0; i < dv.NumField(); i++ {
		name := t.Field(i).Name
		// ExtData 为运行时缓存，VmUUID/VmDeleted 跟随保留者，均不参与合并
		if name == "ExtData" || name == "VmUUID" || name == "VmDeleted" {
			continue
		}
		df, sf := dv.Field(i), sv.Field(i)
		if !df.CanSet() {
			continue
		}
		switch df.Kind() {
		case reflect.Map:
			if sf.IsNil() || sf.Len() == 0 {
				continue
			}
			if df.IsNil() {
				df.Set(reflect.MakeMap(df.Type()))
			}
			for _, k := range sf.MapKeys() {
				if !df.MapIndex(k).IsValid() {
					df.SetMapIndex(k, sf.MapIndex(k))
				}
			}
		case reflect.Slice:
			if sf.IsNil() || sf.Len() == 0 {
				continue
			}
			if name == "Backups" {
				// 备份按名称去重后再追加
				existing := make(map[string]bool, df.Len())
				for j := 0; j < df.Len(); j++ {
					existing[df.Index(j).FieldByName("BackupName").String()] = true
				}
				for j := 0; j < sf.Len(); j++ {
					backupName := sf.Index(j).FieldByName("BackupName").String()
					if backupName == "" || existing[backupName] {
						continue
					}
					existing[backupName] = true
					df.Set(reflect.Append(df, sf.Index(j)))
				}
				continue
			}
			df.Set(reflect.AppendSlice(df, sf))
		case reflect.String:
			if df.String() == "" {
				df.SetString(sf.String())
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if df.Int() == 0 {
				df.SetInt(sf.Int())
			}
		case reflect.Float32, reflect.Float64:
			if df.Float() == 0 {
				df.SetFloat(sf.Float())
			}
		case reflect.Bool:
			if !df.Bool() {
				df.SetBool(sf.Bool())
			}
		}
	}
}

// handleVMDedupe POST /api/client/scaner/:hs_name/dedupe
//
// 合并同一主机下由「命名变体」造成的重复虚拟机记录。
//
// 背景：Proxmox 平台上的名称使用连字符，而早期 Go 驱动在扫描时把 "-"
// 替换成 "_" 作为 vm_uuid，导致同一台虚拟机同时存在 pve-xxx 与 pve_xxx
// 两条记录。这里按命名变体分组，保留业务数据更完整的一条（另一条缺失的
// 字段会被补齐），删除多余的记录。
//
// 只删除本机记录（vm_saving / vm_status 与驱动内存），不做平台操作；
// ?dry_run=true 仅预览将合并的分组。
func (app *App) handleVMDedupe(c *gin.Context) {
	hsName := c.Param("hs_name")

	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}
	if ok, _ := service.CheckVMPermission("delete", uc.UserData); !ok {
		utils.Forbidden(c)
		return
	}
	if !uc.IsAdmin && !service.CheckHostAccess(hsName, uc.UserData) {
		utils.Forbidden(c)
		return
	}

	row, err := app.DB.GetHost(hsName)
	if err != nil || row == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	if row.ServerType != "PromoxSetup" {
		utils.BadRequest(c, "该主机类型不存在命名变体重复问题")
		return
	}

	// 候选集合 = 数据库记录 ∪ 驱动内存记录
	candidates := make(map[string]*model.VMConfig)
	rawMap, err := app.DB.GetVMSaving(hsName)
	if err != nil {
		utils.InternalErrorf(c, "读取虚拟机记录失败", err)
		return
	}
	for uuid, raw := range rawMap {
		cfg, decodeErr := service.DecodeVMConfig(raw)
		if decodeErr != nil || cfg == nil {
			continue
		}
		candidates[uuid] = cfg
	}
	d := app.HostSvc.GetOrCreateDriver(hsName)
	if d != nil {
		for uuid, cfg := range d.GetVMSaving() {
			if cfg == nil {
				continue
			}
			if _, exists := candidates[uuid]; !exists {
				candidates[uuid] = cfg
			}
		}
	}

	// 按命名变体分组（"_" 与 "-" 视为等价）
	groups := make(map[string][]string)
	for uuid := range candidates {
		normalized := strings.ReplaceAll(uuid, "_", "-")
		groups[normalized] = append(groups[normalized], uuid)
	}

	type dedupeGroup struct {
		Keep   string   `json:"keep"`
		Remove []string `json:"remove"`
		Score  int      `json:"score"`
	}
	plans := make([]dedupeGroup, 0, len(groups))
	merged := make(map[string]*model.VMConfig)

	for _, keys := range groups {
		if len(keys) < 2 {
			continue
		}
		sort.Slice(keys, func(i, j int) bool {
			si, sj := vmConfigScore(candidates[keys[i]]), vmConfigScore(candidates[keys[j]])
			if si != sj {
				return si > sj
			}
			// 同分优先保留连字符形式（与平台名称一致），再按字典序稳定排序
			hi, hj := !strings.Contains(keys[i], "_"), !strings.Contains(keys[j], "_")
			if hi != hj {
				return hi
			}
			return keys[i] < keys[j]
		})

		keep := keys[0]
		cfg := candidates[keep]
		if cfg == nil {
			cfg = model.NewVMConfig()
			cfg.VmUUID = keep
		}
		remove := make([]string, 0, len(keys)-1)
		for _, other := range keys[1:] {
			mergeVMConfigInto(cfg, candidates[other])
			remove = append(remove, other)
		}
		merged[keep] = cfg
		plans = append(plans, dedupeGroup{Keep: keep, Remove: remove, Score: vmConfigScore(candidates[keep])})
	}

	sort.Slice(plans, func(i, j int) bool { return plans[i].Keep < plans[j].Keep })

	if v := strings.ToLower(strings.TrimSpace(c.Query("dry_run"))); v == "1" || v == "true" {
		utils.Success(c, gin.H{
			"host_name":   hsName,
			"count":       len(plans),
			"groups":      plans,
			"dry_run":     true,
			"server_type": row.ServerType,
		})
		return
	}

	mergedCount := 0
	removed := 0
	for _, p := range plans {
		if err := app.DB.UpsertVMConfig(hsName, p.Keep, merged[p.Keep]); err != nil {
			app.Logger.Log(hsName, "ERROR", "合并重复记录",
				fmt.Sprintf("写回保留记录 %s 失败: %v", p.Keep, err), false)
			continue
		}
		for _, uuid := range p.Remove {
			_ = app.DB.DeleteVMConfig(hsName, uuid)
			_ = app.DB.DeleteVMStatus(hsName, uuid)
			if d != nil {
				d.RemoveVMSaving(uuid)
			}
			removed++
		}
		if d != nil {
			d.UpsertVMSaving(p.Keep, merged[p.Keep])
		}
		mergedCount++
	}

	app.Logger.Log(hsName, "INFO", "合并重复记录",
		fmt.Sprintf("合并命名变体重复记录 %d 组，移除多余记录 %d 条（仅本机记录）", mergedCount, removed), true)

	utils.SuccessMsg(c, fmt.Sprintf("已合并 %d 组重复记录，移除 %d 条多余记录", mergedCount, removed), gin.H{
		"host_name": hsName,
		"merged":    mergedCount,
		"removed":   removed,
		"groups":    plans,
	})
}

// handleVMUpload POST /api/client/upload（公开接口，虚拟机上报状态）
//
// 该接口无需认证，因此必须自我约束：
//  1. 限制请求体大小，避免被用于内存放大；
//  2. 校验标识格式并确认虚拟机已登记，避免任意数据写入状态表。
func (app *App) handleVMUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024)

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误或请求体过大")
		return
	}
	hsName, _ := req["hs_name"].(string)
	vmUUID, _ := req["vm_uuid"].(string)

	// 虚拟机内的上报脚本沿用的是 Python 版契约：POST /api/client/upload?nic=<MAC>，
	// body 只有状态字段，靠 MAC 反查虚拟机。
	// 原实现只认 body 里的 hs_name/vm_uuid，按 Python 契约上报会被判成
	// 「hs_name和vm_uuid不能为空」，表现为「虚拟机内服务正常但收不到数据」。
	if hsName == "" || vmUUID == "" {
		mac := strings.ToLower(strings.TrimSpace(c.Query("nic")))
		if !looksLikeMAC(mac) {
			utils.BadRequest(c, "缺少虚拟机标识（hs_name/vm_uuid 或 nic 参数）")
			return
		}
		var found bool
		hsName, vmUUID, found = app.findVMByMAC(mac)
		if !found {
			utils.NotFound(c, "未找到该 MAC 对应的虚拟机")
			return
		}
		// 这两个字段不属于状态数据，避免写进历史状态
		delete(req, "hs_name")
		delete(req, "vm_uuid")
	}

	if !vmUUIDLooksSafe(vmUUID) || !vmUUIDLooksSafe(hsName) {
		utils.BadRequest(c, "标识格式无效")
		return
	}
	// 仅接受已登记虚拟机的上报，拒绝为任意名称创建状态记录
	if !app.HostSvc.VMExists(hsName, vmUUID) {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	// rdp_info（ToDesk 等远程桌面信息）合并进虚拟机配置，不写入状态历史
	if rdp, ok := req["rdp_info"].(map[string]interface{}); ok && len(rdp) > 0 {
		app.mergeVMRdpInfo(hsName, vmUUID, rdp)
		delete(req, "rdp_info")
	}

	acStatus, _ := req["ac_status"].(string)
	now := time.Now().Unix()
	// on_update 必须写进状态体：前端按它排序/判断数据新鲜度，
	// Python 版在 vm_upload 里同样会补这个字段。
	req["on_update"] = now
	if err := app.DB.AddVMStatus(hsName, vmUUID, req, acStatus, now, 0); err != nil {
		utils.InternalError(c, "状态保存失败")
		return
	}

	// 上报本身可作为「虚拟机已开机/已关机」的佐证，推进尚未收敛的中间状态
	app.advanceVMPowerFromUpload(hsName, vmUUID)

	// 返回体与 Python 版一致：上报脚本据此拿到密码、电源态与待执行命令
	payload := gin.H{"hs_name": hsName, "vm_uuid": vmUUID}
	if vm := app.HostSvc.MustGetVMConfig(hsName, vmUUID); vm != nil {
		payload["vm_pass"] = vm.OsPass
		payload["vm_flag"] = string(vm.VmFlag)
		if len(vm.VmCmd) > 0 {
			payload["vm_cmd"] = vm.VmCmd
			// 命令是一次性的：取出后立即清空，避免重复执行
			_ = app.HostSvc.SetVMCmd(hsName, vmUUID, nil)
		}
	}
	utils.SuccessMsg(c, "状态已上报", payload)
}

// looksLikeMAC 校验 MAC 是否形如 aa:bb:cc:dd:ee:ff
func looksLikeMAC(mac string) bool {
	if len(mac) != 17 {
		return false
	}
	for i, ch := range mac {
		if i%3 == 2 {
			if ch != ':' {
				return false
			}
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

// macVMRef MAC -> 虚拟机 的缓存条目
type macVMRef struct {
	hsName string
	vmUUID string
	at     time.Time
}

var (
	macVMCacheMu sync.RWMutex
	macVMCache   = map[string]macVMRef{}
)

// macVMCacheTTL MAC 与虚拟机的绑定关系很稳定，缓存 5 分钟即可，
// 避免每次上报都遍历全部主机与虚拟机（VM 多时是明显的放大开销）。
const macVMCacheTTL = 5 * time.Minute

// findVMByMAC 按网卡 MAC 反查虚拟机（对应 Python vm_upload 的匹配逻辑）。
func (app *App) findVMByMAC(mac string) (hsName, vmUUID string, found bool) {
	macVMCacheMu.RLock()
	ref, ok := macVMCache[mac]
	macVMCacheMu.RUnlock()
	if ok && time.Since(ref.at) < macVMCacheTTL {
		return ref.hsName, ref.vmUUID, true
	}

	hosts, err := app.HostSvc.GetAllHosts()
	if err != nil {
		return "", "", false
	}
	for name := range hosts {
		raw, err := app.DB.GetVMSaving(name)
		if err != nil {
			continue
		}
		for uuid, js := range raw {
			cfg, err := service.DecodeVMConfig(js)
			if err != nil || cfg == nil {
				continue
			}
			for _, nic := range cfg.NicAll {
				if strings.EqualFold(strings.TrimSpace(nic.MacAddr), mac) {
					macVMCacheMu.Lock()
					macVMCache[mac] = macVMRef{hsName: name, vmUUID: uuid, at: time.Now()}
					macVMCacheMu.Unlock()
					return name, uuid, true
				}
			}
		}
	}
	return "", "", false
}

// allowedRdpClients 允许写入的远程桌面客户端标识。
//
// 上报接口无需认证，必须限定键名，避免被用来往配置里塞任意结构。
var allowedRdpClients = map[string]struct{}{
	"ms_rdp":   {}, // Windows RDP
	"todesk":   {}, // ToDesk
	"anydesk":  {}, // AnyDesk（Any远程控制）
	"sunlogin": {}, // 向日葵
}

// sanitizeRdpInfo 过滤远程桌面信息：仅保留已知客户端，字段值统一为字符串并限长。
func sanitizeRdpInfo(rdp map[string]interface{}) map[string]interface{} {
	cleaned := make(map[string]interface{}, len(rdp))

	for client, value := range rdp {
		if _, ok := allowedRdpClients[client]; !ok {
			continue
		}
		entry, ok := value.(map[string]interface{})
		if !ok {
			continue
		}

		fields := make(map[string]interface{}, len(entry))
		for key, fieldValue := range entry {
			if len(key) > 32 {
				continue
			}
			text, okText := fieldValue.(string)
			if !okText || strings.TrimSpace(text) == "" {
				continue
			}
			fields[key] = truncateRunes(text, 256)
		}

		if len(fields) > 0 {
			cleaned[client] = fields
		}
	}

	return cleaned
}

// truncateRunes 按字符截断，避免把多字节字符切成乱码
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// mergeVMRdpInfo 把上报的远程桌面信息合并进虚拟机配置。
//
// 必须深合并且「空值不覆盖」：上报脚本并不每次都带全字段
// （ToDesk 的临时密码通常只在变更时上报），整对象覆盖会把
// 之前保存的临时密码清空，用户就再也看不到有效口令。
func (app *App) mergeVMRdpInfo(hsName, vmUUID string, rdp map[string]interface{}) {
	cleaned := sanitizeRdpInfo(rdp)
	if len(cleaned) == 0 {
		return
	}

	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil || vm == nil {
		return
	}
	if vm.RdpInfo == nil {
		vm.RdpInfo = map[string]interface{}{}
	}
	mergeNonEmptyMap(vm.RdpInfo, cleaned)
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		app.Logger.Log(hsName, "WARNING", "虚拟机上报", "保存远程桌面信息失败: "+err.Error(), false)
	}
}

// mergeNonEmptyMap 递归合并 src 到 dst：nil、空串、空对象、空数组都不覆盖已有值。
//
// 这样「这次上报没带临时密码」等同于「本次不更新该字段」，
// 而不是把它清空。
func mergeNonEmptyMap(dst, src map[string]interface{}) {
	for k, v := range src {
		switch val := v.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(val) == "" {
				continue
			}
			dst[k] = val
		case map[string]interface{}:
			if len(val) == 0 {
				continue
			}
			sub, _ := dst[k].(map[string]interface{})
			if sub == nil {
				sub = make(map[string]interface{}, len(val))
			}
			mergeNonEmptyMap(sub, val)
			dst[k] = sub
		case []interface{}:
			if len(val) == 0 {
				continue
			}
			dst[k] = val
		default:
			dst[k] = v
		}
	}
}

// advanceVMPowerFromUpload 把「启动中/关机中」等中间态推进到稳定态。
// 只在当前仍是中间态时改写，避免与调度器采集互相覆盖。
func (app *App) advanceVMPowerFromUpload(hsName, vmUUID string) {
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil || vm == nil {
		return
	}
	var next model.VMPowers
	switch vm.VmFlag {
	case model.VMPowerSClose, model.VMPowerOnStop:
		next = model.VMPowerStopped
	case model.VMPowerSStart, model.VMPowerOnOpen:
		next = model.VMPowerStarted
	default:
		return
	}
	if d := app.HostSvc.GetDriver(hsName); d != nil {
		if live, err := d.GetVMDetail(vmUUID); err == nil && live != nil {
			live.VmFlag = next
			_, _ = d.UpdateVM(live, live)
		}
	}
	vm.VmFlag = next
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		app.Logger.Log(hsName, "WARNING", "虚拟机上报", "保存电源状态失败: "+err.Error(), false)
	}
}

// ============================================================
// NAT端口转发 API
// ============================================================

// handleNATGet GET /api/client/natget/:hs_name/:vm_uuid
func (app *App) handleNATGet(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	utils.Success(c, vm.NatAll)
}

// bindPortData 解析端口映射请求体。
//
// 前端「外网端口留空自动分配」会把 wan_port 传成空字符串，
// 直接绑定到 int 字段会失败并返回「请求参数错误」；
// 这里先把端口字段归一化为数字再解码。
func bindPortData(c *gin.Context) (model.PortData, error) {
	var p model.PortData
	raw := map[string]interface{}{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		return p, err
	}
	for _, key := range []string{"wan_port", "lan_port"} {
		if v, ok := raw[key]; ok {
			raw[key] = toPortInt(v)
		}
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		return p, err
	}
	return p, nil
}

// handleNATAdd POST /api/client/natadd/:hs_name/:vm_uuid
func (app *App) handleNATAdd(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	req, bindErr := bindPortData(c)
	if bindErr != nil {
		utils.BadRequest(c, "请求参数错误: "+bindErr.Error())
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	// 先下发（平台通道可能自动分配外网端口），再记录到配置
	userID := 0
	if uc := auth.GetUserContext(c); uc != nil {
		userID = model.SafeGetInt(uc.UserData, "id")
	}
	// 先下发，成功后才落库：原实现在下发失败时仍写入配置并返回成功类响应，
	// 造成规则表、虚拟机配置与实际转发状态三方不一致。
	if dispatchErr := app.NATService.ApplyPortMap(hsName, vmUUID, &req, userID); dispatchErr != nil {
		app.Logger.Log(hsName, "ERROR", "NAT端口映射", "下发失败: "+dispatchErr.Error(), false)
		utils.Fail(c, 502, "端口映射下发失败，未写入配置: "+dispatchErr.Error())
		return
	}

	vm.NatAll = append(vm.NatAll, req)
	if _, err := d.UpdateVM(vm, vm); err != nil {
		app.Logger.Log(hsName, "WARNING", "NAT端口映射", "更新驱动配置失败: "+err.Error(), false)
	}
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		app.Logger.Log(hsName, "ERROR", "NAT端口映射", "保存虚拟机配置失败: "+err.Error(), false)
		utils.Fail(c, 500, "端口已下发但配置保存失败，请联系管理员核对")
		return
	}
	utils.SuccessMsg(c, "NAT规则添加成功", nil)
}

// handleNATDelete DELETE /api/client/natdel/:hs_name/:vm_uuid/:rule_index
func (app *App) handleNATDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	ruleIndex, _ := strconv.Atoi(c.Param("rule_index"))

	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	if ruleIndex < 0 || ruleIndex >= len(vm.NatAll) {
		utils.BadRequest(c, "规则索引无效")
		return
	}
	removed := vm.NatAll[ruleIndex]
	vm.NatAll = append(vm.NatAll[:ruleIndex], vm.NatAll[ruleIndex+1:]...)
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)

	// 下发路由器删除
	if err := app.NATService.RevokePortMap(hsName, vmUUID, removed); err != nil {
		app.Logger.Log(hsName, "WARNING", "NAT端口映射", "下发路由器删除失败: "+err.Error(), false)
	}
	utils.SuccessMsg(c, "NAT规则删除成功", nil)
}

// ============================================================
// 虚拟机防火墙 API（仅平台支持时可用）
// ============================================================

// firewallDriver 取出支持防火墙能力的驱动。
//
// 防火墙是可选能力：所有驱动都通过 BaseDriver 继承了方法集，
// 因此必须同时检查 SupportsFirewall，不能只看类型断言是否成功。
func (app *App) firewallDriver(hsName string) (driver.FirewallDriver, bool) {
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		return nil, false
	}
	fw, ok := d.(driver.FirewallDriver)
	if !ok || !fw.SupportsFirewall() {
		return nil, false
	}
	return fw, true
}

// handleFirewallGet GET /api/client/firewall/:hs_name/:vm_uuid
// 返回虚拟机防火墙开关、默认策略与规则列表。
func (app *App) handleFirewallGet(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	fw, ok := app.firewallDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机防火墙")
		return
	}
	if _, err := app.loadVMConfig(hsName, vmUUID); err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	state, err := fw.GetFirewallState(vmUUID)
	if err != nil {
		app.Logger.Log(hsName, "ERROR", "防火墙", "读取状态失败: "+err.Error(), false)
		utils.Fail(c, 502, "获取防火墙状态失败: "+err.Error())
		return
	}
	rules, err := fw.GetFirewallRules(vmUUID)
	if err != nil {
		app.Logger.Log(hsName, "ERROR", "防火墙", "读取规则失败: "+err.Error(), false)
		utils.Fail(c, 502, "获取防火墙规则失败: "+err.Error())
		return
	}
	utils.Success(c, gin.H{"state": state, "rules": rules})
}

// firewallOptionsRequest 防火墙开关与默认策略请求体
type firewallOptionsRequest struct {
	Enable    *bool  `json:"enable"`
	PolicyIn  string `json:"policy_in"`
	PolicyOut string `json:"policy_out"`
}

// handleFirewallOptions PUT /api/client/firewall/:hs_name/:vm_uuid/options
func (app *App) handleFirewallOptions(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	fw, ok := app.firewallDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机防火墙")
		return
	}
	var req firewallOptionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误: "+err.Error())
		return
	}
	if req.Enable == nil {
		utils.BadRequest(c, "缺少 enable 参数")
		return
	}
	if err := fw.SetFirewallOptions(vmUUID, *req.Enable, req.PolicyIn, req.PolicyOut); err != nil {
		app.Logger.Log(hsName, "ERROR", "防火墙", "保存开关失败: "+err.Error(), false)
		utils.Fail(c, 502, "保存防火墙配置失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "防火墙配置已保存", nil)
}

// firewallRuleRequest 规则新增 / 更新请求体
type firewallRuleRequest struct {
	Type    string `json:"type"`
	Action  string `json:"action"`
	Enable  *bool  `json:"enable"`
	Proto   string `json:"proto"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Sport   string `json:"sport"`
	Dport   string `json:"dport"`
	Comment string `json:"comment"`
	Macro   string `json:"macro"`
	Iface   string `json:"iface"`
}

// toRule 转成驱动规则；未显式提供 enable 时默认启用
func (r firewallRuleRequest) toRule() driver.FirewallRule {
	enable := true
	if r.Enable != nil {
		enable = *r.Enable
	}
	return driver.FirewallRule{
		Type:    r.Type,
		Action:  r.Action,
		Enable:  enable,
		Proto:   r.Proto,
		Source:  r.Source,
		Dest:    r.Dest,
		Sport:   r.Sport,
		Dport:   r.Dport,
		Comment: r.Comment,
		Macro:   r.Macro,
		Iface:   r.Iface,
	}
}

// handleFirewallRuleCreate POST /api/client/firewall/:hs_name/:vm_uuid/rules
func (app *App) handleFirewallRuleCreate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	fw, ok := app.firewallDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机防火墙")
		return
	}
	var req firewallRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误: "+err.Error())
		return
	}
	if err := fw.AddFirewallRule(vmUUID, req.toRule()); err != nil {
		app.Logger.Log(hsName, "ERROR", "防火墙", "新增规则失败: "+err.Error(), false)
		utils.Fail(c, 502, "新增防火墙规则失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "防火墙规则已添加", nil)
}

// handleFirewallRuleUpdate PUT /api/client/firewall/:hs_name/:vm_uuid/rules/:pos
func (app *App) handleFirewallRuleUpdate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	pos, err := strconv.Atoi(c.Param("pos"))
	if err != nil || pos < 0 {
		utils.BadRequest(c, "规则位置无效")
		return
	}

	fw, ok := app.firewallDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机防火墙")
		return
	}
	var req firewallRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误: "+err.Error())
		return
	}
	if err := fw.UpdateFirewallRule(vmUUID, pos, req.toRule()); err != nil {
		app.Logger.Log(hsName, "ERROR", "防火墙", "保存规则失败: "+err.Error(), false)
		utils.Fail(c, 502, "保存防火墙规则失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "防火墙规则已保存", nil)
}

// handleFirewallRuleDelete DELETE /api/client/firewall/:hs_name/:vm_uuid/rules/:pos
func (app *App) handleFirewallRuleDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	pos, err := strconv.Atoi(c.Param("pos"))
	if err != nil || pos < 0 {
		utils.BadRequest(c, "规则位置无效")
		return
	}

	fw, ok := app.firewallDriver(hsName)
	if !ok {
		utils.BadRequest(c, "当前虚拟化平台不支持虚拟机防火墙")
		return
	}
	if err := fw.DeleteFirewallRule(vmUUID, pos); err != nil {
		app.Logger.Log(hsName, "ERROR", "防火墙", "删除规则失败: "+err.Error(), false)
		utils.Fail(c, 502, "删除防火墙规则失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "防火墙规则已删除", nil)
}

// ============================================================
// IP地址管理 API
// ============================================================

// handleIPList GET /api/client/ipaddr/detail/:hs_name/:vm_uuid
func (app *App) handleIPList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	utils.Success(c, vm.NicAll)
}

// handleIPCreate POST /api/client/ipaddr/create/:hs_name/:vm_uuid
func (app *App) handleIPCreate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req model.NCConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	if req.NicName == "" {
		req.NicName = nextNicName(vm.NicAll)
	}
	oldVM := driver.CloneVMConfig(vm)
	vm.NicAll[req.NicName] = req

	if !app.applyNICChange(c, hsName, vmUUID, d, oldVM, vm) {
		return
	}
	// 返回实际生效的网卡（含自动分配的 IP/MAC），便于前端直接展示
	utils.SuccessMsg(c, "网卡添加成功", gin.H{"nic": vm.NicAll[req.NicName]})
}

// nextNicName 生成一个未被占用的网卡名。
// 原实现用 "nic"+len(nic_all)，删除过网卡后会与现有名字冲突，
// 导致「新增」变成覆盖已有网卡。
func nextNicName(existing map[string]model.NCConfig) string {
	for i := 0; ; i++ {
		name := "nic" + strconv.Itoa(i)
		if _, ok := existing[name]; !ok {
			return name
		}
	}
}

// applyNICChange 网卡增改的统一流程：
//
//	自动分配 IP/MAC（对应 Python BasicServer.NetCheck）
//	→ 下发平台更新 → 落库 → 重新下发静态绑定（对应 Python IPBinder(vm_conf, True)）
//
// 原实现直接改内存并忽略 UpdateVM 的错误：新网卡既拿不到地址，
// 下发失败也照样返回「成功」，表现为「加了网卡但没有 IP」。
//
// 返回 false 表示已经写入了错误响应，调用方直接 return。
func (app *App) applyNICChange(c *gin.Context, hsName, vmUUID string, d driver.HostDriver,
	oldVM, newVM *model.VMConfig) bool {
	if app.IPAllocSvc != nil {
		if err := app.IPAllocSvc.NetCheck(hsName, newVM); err != nil {
			app.Logger.Log(hsName, "ERROR", "网卡配置", "自动分配IP失败: "+err.Error(), false)
			utils.Fail(c, 400, "网卡地址分配失败: "+err.Error())
			return false
		}
	}

	result, err := d.UpdateVM(newVM, oldVM)
	if err != nil {
		app.Logger.Log(hsName, "ERROR", "网卡配置", "平台下发失败: "+err.Error(), false)
		utils.Fail(c, 502, "网卡下发失败: "+err.Error())
		return false
	}
	if result != nil && !result.Success {
		app.Logger.Log(hsName, "ERROR", "网卡配置", "平台拒绝: "+result.Message, false)
		utils.Fail(c, 502, "网卡下发失败: "+result.Message)
		return false
	}

	if err := app.DB.UpsertVMConfig(hsName, vmUUID, newVM); err != nil {
		app.Logger.Log(hsName, "ERROR", "网卡配置", "保存配置失败: "+err.Error(), false)
		utils.Fail(c, 500, "网卡已下发但保存配置失败，请联系管理员核对")
		return false
	}

	// 静态绑定：未配置爱快的主机会静默跳过（NotConfigured），不阻断网卡变更
	if app.NATService != nil {
		if _, bindErr := app.NATService.BindStaticForVM(hsName, vmUUID, newVM); bindErr != nil {
			app.Logger.Log(hsName, "WARNING", "网卡配置", "静态绑定失败: "+bindErr.Error(), false)
		}
	}
	return true
}

// handleIPDelete DELETE /api/client/ipaddr/delete/:hs_name/:vm_uuid/:ip_index
func (app *App) handleIPDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	ipIndex := c.Param("ip_index")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	removed, exists := vm.NicAll[ipIndex]
	oldVM := driver.CloneVMConfig(vm)
	delete(vm.NicAll, ipIndex)

	result, err := d.UpdateVM(vm, oldVM)
	if err != nil {
		utils.Fail(c, 502, "网卡下发失败: "+err.Error())
		return
	}
	if result != nil && !result.Success {
		utils.Fail(c, 502, "网卡下发失败: "+result.Message)
		return
	}
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		utils.Fail(c, 500, "网卡已下发但保存配置失败，请联系管理员核对")
		return
	}

	// 删除网卡后同步解除路由器上的静态绑定，避免残留（不存在时幂等）。
	// 网卡本来就没有 IP/MAC（例如旧版本添加、未分配地址）时无需解绑，
	// 否则会打印「解绑静态绑定需要 ip 或 mac」这类无意义告警。
	if removedIP, removedMAC := strings.TrimSpace(removed.IP4Addr), strings.TrimSpace(removed.MacAddr); exists && app.NATService != nil && (removedIP != "" || removedMAC != "") {
		if unbindErr := app.NATService.UnbindStatic(hsName, removedIP, removedMAC); unbindErr != nil {
			app.Logger.Log(hsName, "WARNING", "网卡配置", "解除静态绑定失败: "+unbindErr.Error(), false)
		}
	}
	utils.SuccessMsg(c, "网卡删除成功", nil)
}

// handleIPUpdate PUT /api/hosts/:hs_name/vms/:vm_uuid/ip_addresses/:nic_name
func (app *App) handleIPUpdate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	nicName := c.Param("nic_name")
	var req model.NCConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	req.NicName = nicName
	oldVM := driver.CloneVMConfig(vm)
	vm.NicAll[nicName] = req

	if !app.applyNICChange(c, hsName, vmUUID, d, oldVM, vm) {
		return
	}
	utils.SuccessMsg(c, "网卡更新成功", gin.H{"nic": vm.NicAll[nicName]})
}

// handleIPDeleteByName DELETE /api/hosts/:hs_name/vms/:vm_uuid/ip_addresses/:nic_name
func (app *App) handleIPDeleteByName(c *gin.Context) {
	c.Params = append(c.Params, gin.Param{Key: "ip_index", Value: c.Param("nic_name")})
	app.handleIPDelete(c)
}

// staticBindRequest 静态绑定请求体（字段名与 Python 版 NCConfig 对齐）
//
// IPv4 与 IPv6 是两套独立的绑定：IPv4 下发 DHCP 静态地址 + ARP，
// IPv6 下发 DHCPv6 静态分配（MAC + 接口标识后缀），因此可以只给其中之一。
type staticBindRequest struct {
	IP4Addr string `json:"ip4_addr"`
	IP6Addr string `json:"ip6_addr"`
	MacAddr string `json:"mac_addr"`
	DNS1    string `json:"dns1"`
	DNS2    string `json:"dns2"`
}

// handleIPBind POST /api/client/ipaddr/bind/:hs_name/:vm_uuid
//
// 在爱快路由器上下发静态绑定（DHCP 静态地址 + ARP），对应 Python 版
// BasicServer.NetiKuai(flag=true)。两种用法：
//   - 请求体给出 ip4_addr + mac_addr：只下发这一条；
//   - 请求体为空：按虚拟机 nic_all 中所有已配置 IP/MAC 的网卡批量下发
//     （对应 Python 版 IPBinder_ROS 遍历 nic_all 的行为）。
func (app *App) handleIPBind(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	var req staticBindRequest
	_ = c.ShouldBindJSON(&req)

	if app.NATService == nil {
		utils.InternalError(c, "NAT服务未初始化")
		return
	}

	if strings.TrimSpace(req.IP4Addr) != "" && strings.TrimSpace(req.MacAddr) != "" {
		// 未配置爱快时 BindStatic 会静默跳过，这里必须先判定，
		// 否则接口会把「什么都没做」回成成功。
		if !app.NATService.StaticBindConfigured(hsName) {
			utils.Fail(c, 400, "主机 "+hsName+" 未配置爱快路由器，无法下发静态绑定")
			return
		}
		if err := app.NATService.BindStatic(hsName, vmUUID, req.IP4Addr, req.MacAddr, req.DNS1, req.DNS2); err != nil {
			app.Logger.Log(hsName, "ERROR", "静态IP绑定", "下发失败: "+err.Error(), false)
			utils.Fail(c, 502, "静态绑定下发失败: "+err.Error())
			return
		}
		utils.SuccessMsg(c, "静态绑定成功", nil)
		return
	}

	// 单条 IPv6 静态分配：爱快侧只认「MAC + 后缀」，不接受完整地址
	if strings.TrimSpace(req.IP6Addr) != "" && strings.TrimSpace(req.MacAddr) != "" {
		if !app.NATService.StaticBindConfigured(hsName) {
			utils.Fail(c, 400, "主机 "+hsName+" 未配置爱快路由器，无法下发 IPv6 静态绑定")
			return
		}
		if err := app.NATService.BindStaticV6(hsName, vmUUID, req.IP6Addr, req.MacAddr); err != nil {
			app.Logger.Log(hsName, "ERROR", "静态IP绑定", "IPv6 下发失败: "+err.Error(), false)
			utils.Fail(c, 502, "IPv6 静态绑定下发失败: "+err.Error())
			return
		}
		utils.SuccessMsg(c, "IPv6 静态绑定成功", nil)
		return
	}

	vm := app.HostSvc.MustGetVMConfig(hsName, vmUUID)
	if vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	res, err := app.NATService.BindStaticForVM(hsName, vmUUID, vm)
	if err != nil {
		app.Logger.Log(hsName, "ERROR", "静态IP绑定", "批量下发失败: "+err.Error(), false)
		utils.Fail(c, 502, "静态绑定下发失败: "+err.Error())
		return
	}
	if res.NotConfigured {
		utils.Fail(c, 400, "主机 "+hsName+" 未配置爱快路由器，无法下发静态绑定")
		return
	}
	if res.Total() == 0 {
		// 网卡缺少 IP/MAC 时无法下发，必须明确报错而不是返回「成功」。
		// 这里必须用 IPv4+IPv6 的总数：只看 IPv4 会把「只分配了 IPv6」
		// 误判成「一条都没绑定」。
		app.Logger.Log(hsName, "WARNING", "静态IP绑定", "无可绑定的网卡: "+res.Summary(), false)
		utils.Fail(c, 400, "没有可绑定的网卡（请先为网卡配置 IP 与 MAC）: "+res.Summary())
		return
	}
	utils.SuccessMsg(c, "静态绑定成功: "+res.Summary(), nil)
}

// handleIPUnbind DELETE /api/client/ipaddr/bind/:hs_name/:vm_uuid
//
// 解除爱快路由器上的静态绑定（DHCP 静态地址 + ARP），对应 Python 版
// BasicServer.NetiKuai(flag=false)。ip4_addr/mac_addr 可放在请求体或查询串中；
// 两者都未提供时，按虚拟机 nic_all 中的网卡逐条解绑。
// 记录不存在视为已解绑，接口保持幂等。
func (app *App) handleIPUnbind(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	var req staticBindRequest
	_ = c.ShouldBindJSON(&req)
	if strings.TrimSpace(req.IP4Addr) == "" {
		req.IP4Addr = c.Query("ip4_addr")
	}
	if strings.TrimSpace(req.IP6Addr) == "" {
		req.IP6Addr = c.Query("ip6_addr")
	}
	if strings.TrimSpace(req.MacAddr) == "" {
		req.MacAddr = c.Query("mac_addr")
	}

	if app.NATService == nil {
		utils.InternalError(c, "NAT服务未初始化")
		return
	}

	// IPv6 静态分配与 IPv4 是两张表，指定 ip6_addr 时只解除 IPv6
	if strings.TrimSpace(req.IP6Addr) != "" {
		if err := app.NATService.UnbindStaticV6(hsName, req.IP6Addr, req.MacAddr); err != nil {
			app.Logger.Log(hsName, "ERROR", "静态IP绑定", "IPv6 解绑失败: "+err.Error(), false)
			utils.Fail(c, 502, "IPv6 静态绑定解绑失败: "+err.Error())
			return
		}
		utils.SuccessMsg(c, "IPv6 静态绑定已解绑", nil)
		return
	}

	if strings.TrimSpace(req.IP4Addr) != "" || strings.TrimSpace(req.MacAddr) != "" {
		if err := app.NATService.UnbindStatic(hsName, req.IP4Addr, req.MacAddr); err != nil {
			app.Logger.Log(hsName, "ERROR", "静态IP绑定", "解绑失败: "+err.Error(), false)
			utils.Fail(c, 502, "静态绑定解绑失败: "+err.Error())
			return
		}
		utils.SuccessMsg(c, "静态绑定已解绑", nil)
		return
	}

	vm := app.HostSvc.MustGetVMConfig(hsName, vmUUID)
	if vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	if err := app.NATService.UnbindStaticForVM(hsName, vm); err != nil {
		app.Logger.Log(hsName, "ERROR", "静态IP绑定", "批量解绑失败: "+err.Error(), false)
		utils.Fail(c, 502, "静态绑定解绑失败: "+err.Error())
		return
	}
	utils.SuccessMsg(c, "静态绑定已解绑", nil)
}

// ============================================================
// 反向代理 API（用户级）
// ============================================================

// proxyRequest 反代配置的入参。
//
// 同时接受两套字段名，避免前端/接口文档口径不一致导致静默失效：
//   - domain / backend_ip / backend_port / ssl_enabled / description
//     （Python add_vm_proxy_config 与前端 apis.ts createWebProxy 使用）
//   - web_addr / lan_addr / lan_port / is_https / web_tips
//     （model.WebProxy 的内部字段名）
//
// 此前只绑定后者，前端发来的 domain 等字段全部落成零值，
// 结果是域名被写成空串、端口写成 0，代理功能静默失效。
type proxyRequest struct {
	Domain      string `json:"domain"`
	BackendIP   string `json:"backend_ip"`
	BackendPort *int   `json:"backend_port"`
	SSLEnabled  *bool  `json:"ssl_enabled"`
	Description string `json:"description"`

	WebAddr string `json:"web_addr"`
	LanAddr string `json:"lan_addr"`
	LanPort *int   `json:"lan_port"`
	IsHTTPS *bool  `json:"is_https"`
	WebTips string `json:"web_tips"`
}

// toWebProxy 归一化为内部模型（Python 口径优先）
func (r proxyRequest) toWebProxy() model.WebProxy {
	p := model.WebProxy{}
	if r.Domain != "" {
		p.WebAddr = r.Domain
	} else {
		p.WebAddr = r.WebAddr
	}
	if r.BackendIP != "" {
		p.LanAddr = r.BackendIP
	} else {
		p.LanAddr = r.LanAddr
	}
	switch {
	case r.BackendPort != nil:
		p.LanPort = *r.BackendPort
	case r.LanPort != nil:
		p.LanPort = *r.LanPort
	default:
		p.LanPort = 80
	}
	switch {
	case r.SSLEnabled != nil:
		p.IsHTTPS = *r.SSLEnabled
	case r.IsHTTPS != nil:
		p.IsHTTPS = *r.IsHTTPS
	}
	if r.Description != "" {
		p.WebTips = r.Description
	} else {
		p.WebTips = r.WebTips
	}
	return p
}

// proxyToFrontendMap 把内部模型摊平为前端/Python 期望的字段名。
//
// Python get_vm_proxy_configs / list_all_proxys_unified 下发的是
// domain / backend_ip / backend_port / ssl_enabled / description，
// 前端 HttpProxys.tsx、DockDetail.tsx 均按这套字段渲染。
// 这里额外带上 proxy_index 与两个别名（port / proxy_*），
// 兼容旧页面里 rule.port || rule.proxy_port 之类的取值方式。
func proxyToFrontendMap(p model.WebProxy, index int, hsName, vmUUID, vmName string) map[string]interface{} {
	m := map[string]interface{}{
		"domain":       p.WebAddr,
		"backend_ip":   p.LanAddr,
		"backend_port": p.LanPort,
		"ssl_enabled":  p.IsHTTPS,
		"description":  p.WebTips,
		"proxy_index":  index,
		"enabled":      true,
		// 别名：兼容不同页面写法
		"port":         p.LanPort,
		"proxy_domain": p.WebAddr,
		"proxy_port":   p.LanPort,
		"host_name":    hsName,
		"vm_uuid":      vmUUID,
	}
	if vmName != "" {
		m["vm_name"] = vmName
	} else {
		m["vm_name"] = vmUUID
	}
	return m
}

// handleProxyUserList GET /api/client/proxys/list
//
// 与 Python list_all_user_proxys 一致：{list:[...], total:n}
// 且每项为摊平字段，而非嵌套 {hs_name, vm_uuid, proxy:{...}}。
func (app *App) handleProxyUserList(c *gin.Context) {
	uc := auth.GetUserContext(c)
	if uc == nil {
		utils.Unauthorized(c)
		return
	}
	list := []interface{}{}
	hosts, _ := app.DB.GetAllHosts()
	for _, h := range hosts {
		if !uc.IsAdmin && !service.CheckHostAccess(h.HsName, uc.UserData) {
			continue
		}
		d := app.HostSvc.GetDriver(h.HsName)
		if d == nil {
			continue
		}
		for vmUUID, vm := range d.GetVMSaving() {
			for i, proxy := range vm.WebAll {
				list = append(list, proxyToFrontendMap(proxy, i, h.HsName, vmUUID, ""))
			}
		}
	}
	utils.Success(c, map[string]interface{}{"list": list, "total": len(list)})
}

// handleProxyDetail GET /api/client/proxys/detail/:hs_name/:vm_uuid
//
// 与 Python get_vm_proxy_configs 一致：返回**裸数组**（不是 {list,total}）。
// DockDetail.tsx 用 Array.isArray(response.data) 判断，
// DockDetailV2.tsx 同理，返回对象会导致列表恒为空。
func (app *App) handleProxyDetail(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	list := make([]interface{}, 0, len(vm.WebAll))
	for i, proxy := range vm.WebAll {
		list = append(list, proxyToFrontendMap(proxy, i, hsName, vmUUID, ""))
	}
	utils.Success(c, list)
}

// handleProxyCreate POST /api/client/proxys/create/:hs_name/:vm_uuid
func (app *App) handleProxyCreate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var raw proxyRequest
	if err := c.ShouldBindJSON(&raw); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	req := raw.toWebProxy()
	if req.WebAddr == "" {
		utils.BadRequest(c, "域名不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	// 域名重复校验（与 Python ProxyMap 的校验语义一致）
	for _, w := range vm.WebAll {
		if w.WebAddr == req.WebAddr {
			utils.BadRequest(c, "域名已存在: "+req.WebAddr)
			return
		}
	}

	vm.WebAll = append(vm.WebAll, req)
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)

	// 记录代理并下发（平台通道优先，否则重载 Caddy；失败不阻断）
	userID := 0
	if uc := auth.GetUserContext(c); uc != nil {
		userID = model.SafeGetInt(uc.UserData, "id")
	}
	_ = app.DB.UpsertWebProxy(hsName, vmUUID, userID, req.WebAddr, req.WebAddr, req.LanAddr, req.LanPort, req.IsHTTPS, req.WebTips)
	if err := app.ProxyService.ApplyProxy(hsName, vmUUID, req, true); err != nil {
		app.Logger.Log(hsName, "WARNING", "反向代理", "下发失败: "+err.Error(), false)
		utils.SuccessMsg(c, "代理已记录，但下发失败: "+err.Error(), nil)
		return
	}
	utils.SuccessMsg(c, "代理添加成功", nil)
}

// handleProxyUpdate PUT /api/client/proxys/update/:hs_name/:vm_uuid/:proxy_index
// 以及 PUT /api/admin/proxys/update/:hs_name/:vm_uuid/:proxy_index
//
// 对应 Python admin_update_proxy：先撤下旧代理，再写入新配置。
func (app *App) handleProxyUpdate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	proxyIndex, _ := strconv.Atoi(c.Param("proxy_index"))
	var raw proxyRequest
	if err := c.ShouldBindJSON(&raw); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	if proxyIndex < 0 || proxyIndex >= len(vm.WebAll) {
		utils.BadRequest(c, "代理索引无效")
		return
	}

	old := vm.WebAll[proxyIndex]
	// 未提供的字段沿用旧值（与 Python admin_update_proxy 的 getattr 兜底一致）
	req := raw.toWebProxy()
	if req.WebAddr == "" {
		req.WebAddr = old.WebAddr
	}
	if req.LanAddr == "" {
		req.LanAddr = old.LanAddr
	}
	if raw.BackendPort == nil && raw.LanPort == nil {
		req.LanPort = old.LanPort
	}
	if raw.SSLEnabled == nil && raw.IsHTTPS == nil {
		req.IsHTTPS = old.IsHTTPS
	}
	if req.WebTips == "" {
		req.WebTips = old.WebTips
	}
	if req.WebAddr == "" {
		utils.BadRequest(c, "域名不能为空")
		return
	}
	// 换域名时不能与其他规则冲突
	for i, w := range vm.WebAll {
		if i != proxyIndex && w.WebAddr == req.WebAddr {
			utils.BadRequest(c, "域名已存在: "+req.WebAddr)
			return
		}
	}

	vm.WebAll[proxyIndex] = req
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)

	// 撤下旧规则、下发新规则
	if old.WebAddr != req.WebAddr {
		_ = app.DB.DeleteWebProxy(hsName, vmUUID, old.WebAddr)
	}
	if err := app.ProxyService.ApplyProxy(hsName, vmUUID, old, false); err != nil {
		app.Logger.Log(hsName, "WARNING", "反向代理", "撤销旧规则失败: "+err.Error(), false)
	}
	userID := 0
	if uc := auth.GetUserContext(c); uc != nil {
		userID = model.SafeGetInt(uc.UserData, "id")
	}
	_ = app.DB.UpsertWebProxy(hsName, vmUUID, userID, req.WebAddr, req.WebAddr, req.LanAddr, req.LanPort, req.IsHTTPS, req.WebTips)
	if err := app.ProxyService.ApplyProxy(hsName, vmUUID, req, true); err != nil {
		app.Logger.Log(hsName, "WARNING", "反向代理", "下发失败: "+err.Error(), false)
		utils.SuccessMsg(c, "代理已更新，但下发失败: "+err.Error(), nil)
		return
	}
	utils.SuccessMsg(c, "代理更新成功", nil)
}

// handleProxyDelete DELETE /api/client/proxys/delete/:hs_name/:vm_uuid/:proxy_index
func (app *App) handleProxyDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	proxyIndex, _ := strconv.Atoi(c.Param("proxy_index"))
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	if proxyIndex < 0 || proxyIndex >= len(vm.WebAll) {
		utils.BadRequest(c, "代理索引无效")
		return
	}
	removed := vm.WebAll[proxyIndex]
	vm.WebAll = append(vm.WebAll[:proxyIndex], vm.WebAll[proxyIndex+1:]...)
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)

	// 删除代理记录并下发
	_ = app.DB.DeleteWebProxy(hsName, vmUUID, removed.WebAddr)
	if err := app.ProxyService.ApplyProxy(hsName, vmUUID, removed, false); err != nil {
		app.Logger.Log(hsName, "WARNING", "反向代理", "下发失败: "+err.Error(), false)
	}
	utils.SuccessMsg(c, "代理删除成功", nil)
}

// ============================================================
// 反向代理 API（管理员级）
// ============================================================

func (app *App) handleProxyAdminList(c *gin.Context) {
	app.handleProxyUserList(c)
}

// handleProxyAdminListByHost GET /api/admin/proxys/list/:hs_name
//
// 与 Python admin_list_host_proxys 一致：{list:[...], total:n}
// （注意：与 client 的 detail 接口不同，这里不是裸数组）
func (app *App) handleProxyAdminListByHost(c *gin.Context) {
	hsName := c.Param("hs_name")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	list := []interface{}{}
	for vmUUID, vm := range d.GetVMSaving() {
		for i, proxy := range vm.WebAll {
			list = append(list, proxyToFrontendMap(proxy, i, hsName, vmUUID, ""))
		}
	}
	utils.Success(c, map[string]interface{}{"list": list, "total": len(list)})
}

// handleProxyAdminDetail GET /api/admin/proxys/detail/:hs_name/:vm_uuid
//
// 与 Python admin_get_vm_proxys 一致：{list:[...], total:n}
func (app *App) handleProxyAdminDetail(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	list := make([]interface{}, 0, len(vm.WebAll))
	for i, proxy := range vm.WebAll {
		list = append(list, proxyToFrontendMap(proxy, i, hsName, vmUUID, ""))
	}
	utils.Success(c, map[string]interface{}{"list": list, "total": len(list)})
}

func (app *App) handleProxyAdminCreate(c *gin.Context) {
	app.handleProxyCreate(c)
}

// handleProxyAdminUpdate PUT /api/admin/proxys/update/:hs_name/:vm_uuid/:proxy_index
func (app *App) handleProxyAdminUpdate(c *gin.Context) {
	app.handleProxyUpdate(c)
}

func (app *App) handleProxyAdminDelete(c *gin.Context) {
	app.handleProxyDelete(c)
}

// ============================================================
// 存储管理 API
// ============================================================

func (app *App) handleHDDList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	utils.Success(c, vm.HddAll)
}

func (app *App) handleHDDMount(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req model.SDConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	// 磁盘名由系统生成，不再让用户填写：前端已移除该输入项，
	// 这里兜底保证 HddAll 的 key 非空（空 key 会让后续挂载/卸载互相覆盖）。
	req.HddName = autoResourceName("data", req.HddName)
	result, err := d.MountHDD(vmUUID, &req, true)
	if err != nil {
		utils.InternalError(c, "挂载磁盘失败")
		return
	}
	if result != nil && !result.Success {
		utils.Fail(c, 502, "挂载磁盘失败: "+result.Message)
		return
	}
	app.persistDriverResources(hsName, vmUUID, d, func(vm, live *model.VMConfig) {
		vm.HddAll = live.HddAll
	})
	utils.SuccessMsg(c, "磁盘挂载成功", nil)
}

func (app *App) handleHDDUnmount(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req model.SDConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	result, err := d.MountHDD(vmUUID, &req, false)
	if err != nil {
		utils.InternalError(c, "卸载磁盘失败")
		return
	}
	if result != nil && !result.Success {
		utils.Fail(c, 502, "卸载磁盘失败: "+result.Message)
		return
	}
	app.persistDriverResources(hsName, vmUUID, d, func(vm, live *model.VMConfig) {
		vm.HddAll = live.HddAll
	})
	utils.SuccessMsg(c, "磁盘卸载成功", nil)
}

func (app *App) handleHDDTransfer(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		HddName  string `json:"hdd_name"`
		TargetVM string `json:"target_vm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}

	// 取真实磁盘配置（含设备位 / 卷标识），否则平台驱动无法精确定位磁盘
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil || vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	hddConf, ok := vm.HddAll[req.HddName]
	if !ok {
		utils.BadRequest(c, "待移交磁盘不存在")
		return
	}
	hdd := hddConf

	// 移交前置校验（对应 Python 版 BasicServer.HDDCheck）：
	// 目标虚拟机需存在、磁盘必须处于「未挂载」状态，否则会出现两台虚拟机同时占用同一块盘。
	if check, checkErr := d.HDDCheck(vmUUID, &hdd, req.TargetVM); checkErr != nil {
		utils.InternalError(c, "移交校验失败")
		return
	} else if check != nil && !check.Success {
		utils.Fail(c, 400, check.Message)
		return
	}

	result, err := d.TransferHDD(vmUUID, &hdd, req.TargetVM)
	if err != nil {
		utils.InternalError(c, "移交磁盘失败")
		return
	}
	if result != nil && !result.Success {
		utils.Fail(c, 500, result.Message)
		return
	}
	utils.Success(c, result)
}

func (app *App) handleHDDDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		HddName string `json:"hdd_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	result, err := d.DeleteHDD(vmUUID, req.HddName)
	if err != nil {
		utils.InternalError(c, "删除磁盘失败")
		return
	}
	vm, _ := app.loadVMConfig(hsName, vmUUID)
	if vm != nil {
		_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)
	}
	utils.Success(c, result)
}

func (app *App) handleISOList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	utils.Success(c, vm.IsoAll)
}

// syncISOToBootList 在挂载/卸载光盘后增量维护 efi_all（启动顺序管理的数据源）。
//
// 只在需要时增删对应项，保留用户手工调整过的顺序；
// 全量重建会把用户排序冲掉。
func syncISOToBootList(vm *model.VMConfig, isoName string, add bool) {
	if vm == nil || strings.TrimSpace(isoName) == "" {
		return
	}
	if add {
		for _, b := range vm.EfiAll {
			if b.EfiType && b.EfiName == isoName {
				return
			}
		}
		vm.EfiAll = append(vm.EfiAll, model.BootOpts{EfiType: true, EfiName: isoName})
		return
	}

	kept := make([]model.BootOpts, 0, len(vm.EfiAll))
	for _, b := range vm.EfiAll {
		if b.EfiType && b.EfiName == isoName {
			continue
		}
		kept = append(kept, b)
	}
	vm.EfiAll = kept
}

// autoResourceName 生成自动资源名：<prefix>-<年月日时分秒>。
//
// current 非空时原样返回，兼容第三方直接指定名称的调用。
// 磁盘/光盘的名称只在本平台内作为标识（PVE 卷名由平台自己生成），
// 因此不需要用户理解命名规则。
func autoResourceName(prefix, current string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return fmt.Sprintf("%s-%s", prefix, time.Now().Format("20060102150405"))
}

// persistDriverResources 把驱动侧的资源清单（iso_all / hdd_all 等）写回数据库。
//
// loadVMConfig 以数据库为权威，只用驱动覆盖电源状态等运行时字段；
// MountISO / MountHDD 在驱动内存里更新的资源清单不会被带回来。
// 原实现直接 Upsert 数据库版本，等于把刚挂载成功的记录丢掉，
// 表现为「提示挂载成功，但列表里什么都没有」。
func (app *App) persistDriverResources(hsName, vmUUID string, d driver.HostDriver,
	apply func(vm, live *model.VMConfig)) {
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil || vm == nil {
		return
	}
	live, liveErr := d.GetVMDetail(vmUUID)
	if liveErr != nil || live == nil {
		return
	}
	apply(vm, live)
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		app.Logger.Log(hsName, "ERROR", "存储资源", "保存虚拟机配置失败: "+err.Error(), false)
	}
}

func (app *App) handleISOMount(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req model.IMConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	// 挂载名称由系统生成（前端已移除输入项）：IsoAll 以它作为 key，
	// 卸载接口也按它定位，空值会导致多张光盘互相覆盖。
	req.IsoName = autoResourceName("iso", req.IsoName)
	result, err := d.MountISO(vmUUID, &req, true)
	if err != nil {
		utils.InternalError(c, "挂载ISO失败")
		return
	}
	// 驱动返回 Success=false 也是失败：原实现无条件回 200，
	// 前端显示「成功」但对端根本没挂上。
	if result != nil && !result.Success {
		app.Logger.Log(hsName, "ERROR", "ISO挂载", result.Message, false)
		utils.Fail(c, 502, "挂载ISO失败: "+result.Message)
		return
	}
	app.persistDriverResources(hsName, vmUUID, d, func(vm, live *model.VMConfig) {
		vm.IsoAll = live.IsoAll
		// 「启动顺序管理」展示的是 efi_all：挂载光盘后要同步加进去，
		// 否则界面上永远看不到这张光盘（PVE 的 boot 里其实已经有 ide2）。
		syncISOToBootList(vm, req.IsoName, true)
	})
	utils.SuccessMsg(c, "ISO挂载成功", nil)
}

func (app *App) handleISOUnmount(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	isoName := c.Param("iso_name")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	iso := &model.IMConfig{IsoName: isoName}
	result, err := d.MountISO(vmUUID, iso, false)
	if err != nil {
		utils.InternalError(c, "卸载ISO失败")
		return
	}
	if result != nil && !result.Success {
		app.Logger.Log(hsName, "ERROR", "ISO卸载", result.Message, false)
		utils.Fail(c, 502, "卸载ISO失败: "+result.Message)
		return
	}
	app.persistDriverResources(hsName, vmUUID, d, func(vm, live *model.VMConfig) {
		vm.IsoAll = live.IsoAll
		syncISOToBootList(vm, isoName, false)
	})
	utils.SuccessMsg(c, "ISO卸载成功", nil)
}

// handleUSBMount POST /api/client/usb/mount/:hs_name/:vm_uuid
//
// 与 Python mount_vm_usb 一致：接受 {usb_key} 或 {usb_vid, usb_pid, usb_remark}；
// 只给 VID/PID 时由后端生成 usb_key（前端 apis.ts addUSB 发送的正是后者）。
func (app *App) handleUSBMount(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		UsbKey    string `json:"usb_key"`
		UsbVid    string `json:"usb_vid"`
		UsbPid    string `json:"usb_pid"`
		UsbRemark string `json:"usb_remark"`
		UsbHint   string `json:"usb_hint"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	usbKey := strings.TrimSpace(req.UsbKey)
	if usbKey == "" {
		if req.UsbVid == "" || req.UsbPid == "" {
			utils.BadRequest(c, "VID和PID不能为空")
			return
		}
		// 与 Python 一致：VID:PID:备注 派生稳定 key，重复挂载同一设备不会产生多条记录
		hint := req.UsbRemark
		if hint == "" {
			hint = req.UsbHint
		}
		usbKey = fmt.Sprintf("%s:%s:%s", req.UsbVid, req.UsbPid, hint)
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	result, err := d.MountUSB(vmUUID, usbKey, true)
	if err != nil {
		utils.InternalError(c, "挂载USB失败")
		return
	}
	utils.Success(c, result)
}

func (app *App) handleUSBDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	usbKey := c.Param("usb_key")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	result, err := d.MountUSB(vmUUID, usbKey, false)
	if err != nil {
		utils.InternalError(c, "卸载USB失败")
		return
	}
	utils.Success(c, result)
}

// ============================================================
// 备份管理 API
// ============================================================

func (app *App) handleBackupList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	// 平台能直接列出备份文件时以平台为准：
	// 备份是后台长任务，本地记录要等任务结束才写入，
	// 期间列表为空会让人误以为「没备份成功」。
	if bs, ok := d.(interface{ SyncBackups(string) }); ok {
		bs.SyncBackups(vmUUID)
		app.persistDriverResources(hsName, vmUUID, d, func(vm, live *model.VMConfig) {
			vm.Backups = live.Backups
		})
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	utils.Success(c, vm.Backups)
}

// backupRequest 备份相关入参。
// 同时接受前端字段（vm_tips / vm_back，见 apis.ts）与内部字段名（tips / backup_name）。
// 此前只认内部字段名，导致「还原/删除备份」必定 400（备份文件名不能为空），
// 创建备份的备注也总是丢失。
type backupRequest struct {
	Tips       string `json:"tips"`
	VMTips     string `json:"vm_tips"`
	BackupName string `json:"backup_name"`
	VMBack     string `json:"vm_back"`
}

// name 归一化备份文件名
func (r backupRequest) name() string {
	if r.BackupName != "" {
		return r.BackupName
	}
	return r.VMBack
}

// tipText 归一化备份备注
func (r backupRequest) tipText() string {
	if r.Tips != "" {
		return r.Tips
	}
	return r.VMTips
}

func (app *App) handleBackupCreate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req backupRequest
	_ = c.ShouldBindJSON(&req)
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	// 备份名由系统生成：<虚拟机名>-backup-<年月日时分秒>。
	// 前端已移除手填输入框，这里兜底（第三方调用不传时同样有可读名称）。
	tips := strings.TrimSpace(req.tipText())
	if tips == "" {
		tips = fmt.Sprintf("%s-backup-%s", vmUUID, time.Now().Format("20060102150405"))
	}

	// 备份是分钟级的长任务：同步等待会让浏览器请求超时，
	// 表现为「提示创建失败，但 PVE 上其实备份已成功」。
	// 这里立即返回受理结果，由后台协程等待完成后把记录写回配置。
	go func() {
		result, err := d.CreateBackup(vmUUID, tips)
		if err != nil {
			app.Logger.Log(hsName, "ERROR", "虚拟机备份", "创建失败: "+err.Error(), false)
			return
		}
		if result != nil && !result.Success {
			app.Logger.Log(hsName, "ERROR", "虚拟机备份", "创建失败: "+result.Message, false)
			return
		}
		app.persistDriverResources(hsName, vmUUID, d, func(vm, live *model.VMConfig) {
			vm.Backups = live.Backups
		})
		app.Logger.Log(hsName, "INFO", "虚拟机备份", "备份完成并已记录", false)
	}()

	utils.SuccessMsg(c, "备份任务已提交，正在后台执行，完成后可在列表查看", nil)
}

func (app *App) handleBackupRestore(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req backupRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.name() == "" {
		utils.BadRequest(c, "备份文件名不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	result, err := d.RestoreBackup(vmUUID, req.name())
	if err != nil {
		utils.InternalError(c, "还原备份失败")
		return
	}
	utils.Success(c, result)
}

func (app *App) handleBackupDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req backupRequest
	// DELETE 的 body 可能为空，先尝试解析 JSON，再回退到 query
	if err := c.ShouldBindJSON(&req); err != nil {
		req.BackupName = c.Query("backup_name")
		req.VMBack = c.Query("vm_back")
	}
	if req.name() == "" {
		utils.BadRequest(c, "备份文件名不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	result, err := d.DeleteBackup(vmUUID, req.name())
	if err != nil {
		utils.InternalError(c, "删除备份失败")
		return
	}
	utils.Success(c, result)
}

// ============================================================
// 设备直通 API
// ============================================================

// handleOSImages GET /api/client/os-images/:hs_name
// 返回结构与 Python get_os_images 一致：
//
//	{ host_name, server_type, filter_name, system_maps, images_maps,
//	  ban_init, ban_edit, messages, tab_lock, enable_host, ipaddr_maps, ipaddr_ddns, public_addr }
//
// 前端用它同时获取：虚拟机 UUID 前缀（filter_name）、系统镜像下拉（system_maps）、
// 表单禁用项（ban_init/ban_edit/tab_lock）、平台注意事项（messages）与启用状态（enable_host）。
// 直接读主机配置而非驱动，禁用主机同样可返回（与 Python 行为一致）。
func (app *App) handleOSImages(c *gin.Context) {
	hsName := c.Param("hs_name")

	uc := auth.GetUserContext(c)
	if uc != nil && !uc.IsAdmin && !uc.TempLogin {
		if !service.CheckHostAccess(hsName, uc.UserData) {
			utils.Forbidden(c)
			return
		}
	}

	cfg, err := app.HostSvc.GetHost(hsName)
	if err != nil || cfg == nil {
		utils.NotFound(c, "主机不存在")
		return
	}

	meta, _ := driver.EngineMetaOf(cfg.ServerType)

	utils.Success(c, map[string]interface{}{
		"host_name":    hsName,
		"server_type":  cfg.ServerType,
		"filter_name":  cfg.FilterName,
		"system_maps":  service.OSListToArray(cfg.SystemMaps),
		"images_maps":  service.OSListToArray(cfg.ImagesMaps),
		"ban_init":     meta.BanInit,
		"ban_edit":     meta.BanEdit,
		"messages":     meta.Messages,
		"tab_lock":     meta.TabLock,
		"enable_host":  cfg.EnableHost,
		"ipaddr_maps":  cfg.IPAddrMaps,
		"ipaddr_ddns":  cfg.IPAddrDDNS,
		"public_addr":  cfg.PublicAddr,
		"capabilities": service.PlatformCapabilities(cfg.ServerType),
	})
}

func (app *App) handleGPUList(c *gin.Context) {
	hsName := c.Param("hs_name")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	gpus, err := d.GetGPUList()
	if err != nil {
		utils.InternalError(c, "获取GPU列表失败")
		return
	}
	utils.Success(c, gpus)
}

func (app *App) handlePCIList(c *gin.Context) {
	hsName := c.Param("hs_name")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	pcis, err := d.GetPCIList()
	if err != nil {
		utils.InternalError(c, "获取PCI列表失败")
		return
	}
	utils.Success(c, pcis)
}

func (app *App) handlePCISetup(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		PciKey  string `json:"pci_key"`
		GpuUUID string `json:"gpu_uuid"`
		GpuMdev string `json:"gpu_mdev"`
		GpuHint string `json:"gpu_hint"`
		Action  string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	cfg := &model.VFConfig{
		PciKey: req.PciKey, GpuUUID: req.GpuUUID,
		GpuMdev: req.GpuMdev, GpuHint: req.GpuHint,
	}
	add := req.Action == "add"
	result, err := d.SetupPCI(vmUUID, cfg, req.PciKey, add)
	if err != nil {
		utils.InternalError(c, "PCI直通操作失败")
		return
	}
	utils.Success(c, result)
}

func (app *App) handleUSBList(c *gin.Context) {
	hsName := c.Param("hs_name")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	usbs, err := d.GetUSBList()
	if err != nil {
		utils.InternalError(c, "获取USB列表失败")
		return
	}
	utils.Success(c, usbs)
}

func (app *App) handleUSBSetup(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		UsbKey  string `json:"usb_key"`
		VidUUID string `json:"vid_uuid"`
		PidUUID string `json:"pid_uuid"`
		UsbHint string `json:"usb_hint"`
		Action  string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	info := &model.USBInfos{
		UsbKey: req.UsbKey, VidUUID: req.VidUUID,
		PidUUID: req.PidUUID, UsbHint: req.UsbHint,
	}
	add := req.Action == "add"
	result, err := d.SetupUSB(vmUUID, info, req.UsbKey, add)
	if err != nil {
		utils.InternalError(c, "USB直通操作失败")
		return
	}
	utils.Success(c, result)
}

// ============================================================
// EFI启动项 API
// ============================================================

func (app *App) handleEFIList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	// 历史虚拟机没有 efi_all：按当前磁盘/光驱/网卡生成一份默认启动项并落库，
	// 让前端首屏（含缓存）有内容可显示，而不是空白或占位。
	if len(vm.EfiAll) == 0 {
		vm.EfiAll = defaultBootOptions(vm)
		if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
			app.Logger.Log(hsName, "WARNING", "启动顺序", "保存默认启动项失败: "+err.Error(), false)
		}
	}
	utils.Success(c, vm.EfiAll)
}

// defaultBootOptions 按平台语义生成默认启动项：系统盘 → 数据盘 → 光驱 → 网卡。
//
// efi_name 必须使用业务名称（虚拟机名 / 虚拟机名-数据盘名 / 光盘名），
// 因为 proxmoxBootOrder 靠它映射到 ide0/scsiN/ideN；无法映射的项会被忽略，
// 网卡会由 boot 逻辑自动补齐，因此这里多出的"网卡"项只用于展示。
func defaultBootOptions(vm *model.VMConfig) []model.BootOpts {
	list := []model.BootOpts{{EfiType: false, EfiName: vm.VmUUID}}

	hddNames := make([]string, 0, len(vm.HddAll))
	for name := range vm.HddAll {
		hddNames = append(hddNames, name)
	}
	sort.Strings(hddNames)
	for _, name := range hddNames {
		if vm.HddAll[name].HddFlag == 0 {
			continue
		}
		list = append(list, model.BootOpts{EfiType: false, EfiName: vm.VmUUID + "-" + name})
	}

	isoNames := make([]string, 0, len(vm.IsoAll))
	for name := range vm.IsoAll {
		isoNames = append(isoNames, name)
	}
	sort.Strings(isoNames)
	for _, name := range isoNames {
		list = append(list, model.BootOpts{EfiType: true, EfiName: name})
	}

	if len(vm.NicAll) > 0 {
		list = append(list, model.BootOpts{EfiType: false, EfiName: "网卡(PXE)"})
	}
	return list
}

func (app *App) handleEFISetup(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		EfiList []model.BootOpts `json:"efi_list"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	// 用快照做 old：UpdateVM 以新旧对比决定下发内容，
	// 传同一个对象会让变更被判定为「无变化」而静默丢弃。
	oldVM := driver.CloneVMConfig(vm)
	vm.EfiAll = req.EfiList
	result, err := d.UpdateVM(vm, oldVM)
	if err != nil {
		utils.Fail(c, 502, "下发启动顺序失败: "+err.Error())
		return
	}
	if result != nil && !result.Success {
		utils.Fail(c, 502, "下发启动顺序失败: "+result.Message)
		return
	}
	if err := app.DB.UpsertVMConfig(hsName, vmUUID, vm); err != nil {
		utils.Fail(c, 500, "启动顺序已下发但保存配置失败，请联系管理员核对")
		return
	}
	utils.SuccessMsg(c, "启动顺序设置成功", nil)
}

// ============================================================
// 虚拟机所有者 API
// ============================================================

func (app *App) handleOwnerList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	utils.Success(c, vm.OwnAll)
}

func (app *App) handleOwnerAdd(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		utils.BadRequest(c, "用户名不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	vm.OwnAll[req.Username] = model.FullUserMask()
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)
	utils.SuccessMsg(c, "所有者添加成功", nil)
}

func (app *App) handleOwnerDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	var req struct {
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		utils.BadRequest(c, "用户名不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	delete(vm.OwnAll, req.Username)
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)
	utils.SuccessMsg(c, "所有者删除成功", nil)
}

func (app *App) handleOwnerPermission(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	// 兼容两种下发方式（与 Python update_vm_owner_permission 一致）：
	//   - permission: 16 位整型掩码（前端 updateVMOwnerPermission 发送的数字）
	//   - mask:       位字段对象（Go 内部/接口文档使用）
	var req struct {
		Username   string                 `json:"username"`
		Mask       *model.UserMask        `json:"mask"`
		Permission *int                   `json:"permission"`
		Extra      map[string]interface{} `json:"-"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	// 归一化出最终掩码
	var mask model.UserMask
	switch {
	case req.Permission != nil:
		mask = model.UserMaskFromBits(*req.Permission)
	case req.Mask != nil:
		mask = *req.Mask
	default:
		utils.BadRequest(c, "权限掩码不能为空")
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}
	if vm.OwnAll == nil {
		vm.OwnAll = map[string]model.UserMask{}
	}
	if _, ok := vm.OwnAll[req.Username]; !ok {
		utils.BadRequest(c, "用户不是所有者")
		return
	}
	vm.OwnAll[req.Username] = mask
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)
	utils.SuccessMsg(c, "权限更新成功", nil)
}

func (app *App) handleOwnerTransfer(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	// 兼容两套字段名（与 Python transfer_vm_ownership 一致）：
	//   keep_access      —— 前端 keepAccessChecked（Python 使用的名字）
	//   keep_old         —— Go 内部/接口文档使用的名字
	//   confirm_transfer —— 前端二次确认勾选；Python 未勾选直接 400
	var req struct {
		NewOwner        string `json:"new_owner"`
		KeepOld         bool   `json:"keep_old"`
		KeepAccess      bool   `json:"keep_access"`
		ConfirmTransfer *bool  `json:"confirm_transfer"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.NewOwner == "" {
		utils.BadRequest(c, "新所有者不能为空")
		return
	}
	// 前端会显式传 confirm_transfer，未勾选时应拒绝（与 Python 行为一致）
	if req.ConfirmTransfer != nil && !*req.ConfirmTransfer {
		utils.BadRequest(c, "必须确认移交所有权")
		return
	}
	keepOld := req.KeepOld || req.KeepAccess
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.NotFound(c, "主机不存在")
		return
	}
	vm, err := app.loadVMConfig(hsName, vmUUID)
	if err != nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	// 重建所有者map，新所有者排第一
	newOwners := map[string]model.UserMask{
		req.NewOwner: model.FullUserMask(),
	}
	if keepOld {
		for k, v := range vm.OwnAll {
			if k != req.NewOwner {
				newOwners[k] = v
			}
		}
	}
	vm.OwnAll = newOwners
	_, _ = d.UpdateVM(vm, vm)
	_ = app.DB.UpsertVMConfig(hsName, vmUUID, vm)
	utils.SuccessMsg(c, "所有权移交成功", nil)
}

// 确保time包被使用（handleVMUpload中使用了time.Now()）
var _ = time.Now
