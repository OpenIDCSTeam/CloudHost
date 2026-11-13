// Package api 虚拟机响应视图构造
//
// 前端（FrontPage）按 Python 版的响应结构渲染虚拟机：
//
//	GET /api/client/detail/:hs_name        → {uuid: {uuid, config, status, user_permissions}}
//	GET /api/client/detail/:hs_name/:uuid  → {uuid, config, status, user_permissions, is_admin, current_user}
//
// 前端通过 vm.config.own_all 判断所有者、通过 vm.status[0] 展示流量与监控
// （见 DockManage.tsx / UserPanels.tsx / PortManage.tsx）。
// 此前 Go 直接返回扁平 VMConfig，导致 vm.config 为 undefined：
// 所有者过滤拿不到数据、列表始终为空 —— 表现为「扫描不到虚拟机」。
package api

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/auth"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// vmStatusHistoryMax 返回给前端的虚拟机状态条数（前端取第 0 条展示）
const vmStatusHistoryMax = 10

// hasVMMetrics 判断一条状态记录是否带有资源指标（即由虚拟机内 Agent 上报）。
//
// vm_status 有两个写入方：虚拟机内 Agent（带 cpu/mem/hdd/flu 指标）和调度器
// （每分钟一条只含 ac_status/vm_state 的合成记录）。两者的区分标志就是指标字段。
func hasVMMetrics(item map[string]interface{}) bool {
	for _, key := range []string{"cpu_total", "mem_total", "hdd_total", "flu_total"} {
		if _, ok := item[key]; ok {
			return true
		}
	}
	return false
}

// toMap 将结构体转为 map（用于拼接响应）
func toMap(v interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	if v == nil {
		return result
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return result
	}
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return make(map[string]interface{})
	}
	return result
}

// buildVMStatusList 读取虚拟机的历史状态（最近若干条，最新在前）。
func (app *App) buildVMStatusList(hsName, vmUUID string) []interface{} {
	rawList, err := app.DB.GetVMStatus(hsName, vmUUID, 0, 0)
	if err != nil || len(rawList) == 0 {
		return []interface{}{}
	}
	return composeVMStatusList(rawList, vmStatusHistoryMax)
}

// composeVMStatusList 将按 on_update 倒序的原始状态记录合成为前端消费的数组。
//
// 首条固定为「当前状态」：电源态取最新一条记录（调度器每分钟更新的稳定态），
// 资源指标取最新一条带指标的 Agent 上报。
//
// 之所以要合成：调度器每分钟也会往 vm_status 写一条只含电源态的记录，它与 Agent
// 上报混在同一张历史表里。前端列表页只取 status[0]，一旦最新的那条是调度器记录，
// CPU/内存/硬盘/流量就会退化成配置值（显示为 0），表现为「上报了但看不到状态」。
func composeVMStatusList(rawList []string, max int) []interface{} {
	if max <= 0 {
		return []interface{}{}
	}

	items := make([]map[string]interface{}, 0, len(rawList))
	var newest, newestMetric map[string]interface{}
	for _, raw := range rawList {
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &item); err != nil || item == nil {
			continue
		}
		// 丢弃既无电源态又无指标的无效记录
		if _, ok := item["ac_status"]; !ok && !hasVMMetrics(item) {
			continue
		}
		items = append(items, item)
		if newest == nil {
			newest = item // rawList 按 on_update 倒序，首条即最新
		}
		if newestMetric == nil && hasVMMetrics(item) {
			newestMetric = item
		}
	}
	if len(items) == 0 {
		return []interface{}{}
	}

	current := make(map[string]interface{}, 16)
	base := newestMetric
	if base == nil {
		base = newest
	}
	for k, v := range base {
		current[k] = v
	}
	// 电源态始终以最新记录为准：Agent 上报的是采样时刻的电源态，
	// 调度器记录的是平台侧实时状态，后者更新。
	for _, key := range []string{"ac_status", "vm_state", "on_update"} {
		if v, ok := newest[key]; ok {
			current[key] = v
		}
	}

	result := make([]interface{}, 0, max)
	result = append(result, current)
	for i, item := range items {
		if len(result) >= max {
			break
		}
		if i == 0 {
			continue // 首条已并入当前状态
		}
		result = append(result, item)
	}
	return result
}

// splitVMStatusHistory 从按 on_update 倒序的原始记录中分离出最新电源态与监控历史。
//
// history 只保留带资源指标的 Agent 上报（调度器记录混进来会让图表出现大量 0 值点），
// 并按时间升序返回（前端绘图与 DockDetail 取末条作为最新）。电源态单独从全部记录
// 中取最新一条，不受过滤规则影响。
func splitVMStatusHistory(rawList []string) (powerStatus string, history []interface{}) {
	powerStatus = string(model.VMPowerUnknown)
	history = make([]interface{}, 0, len(rawList))
	for _, s := range rawList {
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(s), &item); err != nil || item == nil {
			continue
		}
		if powerStatus == string(model.VMPowerUnknown) {
			if v, ok := item["ac_status"].(string); ok && strings.TrimSpace(v) != "" {
				powerStatus = v
			}
		}
		if !hasVMMetrics(item) {
			continue
		}
		history = append(history, item)
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	return powerStatus, history
}

// vmUserMask 计算当前用户对该虚拟机的权限掩码。
// 管理员 / Token 登录视为全权限（与 Python 版 _calc_user_vm_permission 一致）。
func vmUserMask(vm *model.VMConfig, uc *auth.UserContext) model.UserMask {
	if uc == nil {
		return model.UserMask{}
	}
	if uc.IsAdmin || uc.IsToken {
		return model.FullUserMask()
	}
	if vm == nil || vm.OwnAll == nil {
		return model.UserMask{}
	}
	if mask, ok := vm.OwnAll[uc.Username]; ok {
		return mask
	}
	return model.UserMask{}
}

// buildVMView 构造虚拟机视图（对应 Python 版 get_vms / get_vm_detail 的单机结构）
func (app *App) buildVMView(hsName, vmUUID string, vm *model.VMConfig, uc *auth.UserContext) map[string]interface{} {
	config := toMap(vm)
	config["vm_uuid"] = vmUUID

	isAdmin := uc != nil && (uc.IsAdmin || uc.IsToken)
	mask := vmUserMask(vm, uc)

	// 无密码 / VNC 权限时屏蔽敏感字段（与 Python 版一致）
	if !isAdmin {
		if !mask.PwdEdits {
			config["os_pass"] = "******"
		}
		if !mask.VncEdits {
			config["vc_pass"] = "******"
		}
	}

	statusList := app.buildVMStatusList(hsName, vmUUID)
	if len(statusList) == 0 && vm != nil && vm.VmFlag != "" {
		// 没有任何有效历史时补一条：前端列表页按 status[0].ac_status 展示电源态，
		// 空数组会退化成「未知」，即使驱动里已经有准确状态。
		statusList = []interface{}{map[string]interface{}{
			"ac_status": string(vm.VmFlag),
			"vm_state":  string(vm.VmFlag),
			"on_update": time.Now().Unix(),
		}}
	} else if vm != nil && vm.VmFlag != "" && vm.VmFlag != model.VMPowerUnknown {
		// 电源态以驱动内存为准（调度器每 60 秒写回）：状态历史里的 ac_status 是
		// Agent 采样时刻的值，虚拟机关机后 Agent 不再上报，历史会永远停在「运行中」。
		// 只覆盖首条电源态，资源指标仍取最新一次上报。
		if first, ok := statusList[0].(map[string]interface{}); ok {
			first["ac_status"] = string(vm.VmFlag)
		}
	}

	return map[string]interface{}{
		"uuid":             vmUUID,
		"config":           config,
		"status":           statusList,
		"user_permissions": mask.ToBits(),
		"is_admin":         isAdmin,
	}
}
