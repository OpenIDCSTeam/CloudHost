package api

import (
	"strings"

	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
	"github.com/gin-gonic/gin"
)

// publicIPRequest 公网地址分配请求体。
//
// 三个字段都可缺省：
//   - public_addr 为空 → 从主机 public_addr 池中自动挑选未占用的地址；
//   - lan_addr 为空    → 取虚拟机第一块已分配 IPv4 的网卡；
//   - comment 为空     → 使用虚拟机 UUID，便于在爱快上定位归属。
type publicIPRequest struct {
	PublicAddr string `json:"public_addr"`
	LanAddr    string `json:"lan_addr"`
	NicName    string `json:"nic_name"`
	Comment    string `json:"comment"`
}

// handlePublicIPPool GET /api/hosts/:hs_name/public_ips
//
// 返回主机配置的公网地址池与当前全部绑定关系，
// 便于前端展示「哪些公网 IP 空闲 / 已被哪台虚拟机占用」。
func (app *App) handlePublicIPPool(c *gin.Context) {
	hsName := c.Param("hs_name")
	if app.NATService == nil {
		utils.InternalError(c, "NAT服务未初始化")
		return
	}

	pool, err := app.NATService.PublicIPPool(hsName)
	if err != nil {
		utils.Fail(c, 400, err.Error())
		return
	}
	bindings, err := app.NATService.ListPublicIPs(hsName)
	if err != nil {
		utils.InternalError(c, err.Error())
		return
	}
	utils.Success(c, gin.H{"pool": pool, "bindings": bindings})
}

// handlePublicIPList GET /api/hosts/:hs_name/vms/:vm_uuid/public_ips
//
// 返回指定虚拟机已分配的公网地址。
func (app *App) handlePublicIPList(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")
	if app.NATService == nil {
		utils.InternalError(c, "NAT服务未初始化")
		return
	}
	bindings, err := app.NATService.ListVMPublicIPs(hsName, vmUUID)
	if err != nil {
		utils.InternalError(c, err.Error())
		return
	}
	utils.Success(c, bindings)
}

// handlePublicIPAllocate POST /api/hosts/:hs_name/vms/:vm_uuid/public_ips
//
// 为虚拟机分配公网 IPv4，并在爱快上下发地址转换（1:1 NAT）规则。
// 同一虚拟机的同一个内网地址重复分配是幂等的：地址已经归属该虚拟机时直接更新。
func (app *App) handlePublicIPAllocate(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	var req publicIPRequest
	// 空请求体表示「全部自动分配」，因此忽略绑定错误
	_ = c.ShouldBindJSON(&req)

	if app.NATService == nil {
		utils.InternalError(c, "NAT服务未初始化")
		return
	}
	vm := app.HostSvc.MustGetVMConfig(hsName, vmUUID)
	if vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	// 指定网卡时以该网卡的 IPv4 作为转换目标（多网卡场景下不能默认取第一张）
	lanAddr := req.LanAddr
	if strings.TrimSpace(lanAddr) == "" && strings.TrimSpace(req.NicName) != "" {
		nic, ok := vm.NicAll[req.NicName]
		if !ok {
			utils.BadRequest(c, "网卡 "+req.NicName+" 不存在")
			return
		}
		lanAddr = nic.IP4Addr
	}

	binding, err := app.NATService.AllocatePublicIP(hsName, vmUUID, req.PublicAddr, lanAddr, req.Comment)
	if err != nil {
		app.Logger.Log(hsName, "ERROR", "公网地址分配", err.Error(), false)
		utils.Fail(c, 502, "公网地址分配失败: "+err.Error())
		return
	}

	app.Logger.Log(hsName, "INFO", "公网地址分配",
		"公网 "+binding.PublicAddr+" → 内网 "+binding.LanAddr+"（虚拟机 "+vmUUID+"）", true)
	utils.SuccessMsg(c, "公网地址分配成功", binding)
}

// handlePublicIPRelease DELETE /api/hosts/:hs_name/vms/:vm_uuid/public_ips/:public_addr
//
// 释放公网地址：撤销爱快地址转换并删除本地记录。
// 未指定地址时释放该虚拟机的全部公网地址。
func (app *App) handlePublicIPRelease(c *gin.Context) {
	hsName := c.Param("hs_name")
	vmUUID := c.Param("vm_uuid")

	publicAddr := strings.TrimSpace(c.Param("public_addr"))
	if publicAddr == "" {
		publicAddr = strings.TrimSpace(c.Query("public_addr"))
	}

	if app.NATService == nil {
		utils.InternalError(c, "NAT服务未初始化")
		return
	}
	if vm := app.HostSvc.MustGetVMConfig(hsName, vmUUID); vm == nil {
		utils.NotFound(c, "虚拟机不存在")
		return
	}

	if err := app.NATService.ReleasePublicIP(hsName, vmUUID, publicAddr); err != nil {
		app.Logger.Log(hsName, "ERROR", "公网地址释放", err.Error(), false)
		utils.Fail(c, 502, "公网地址释放失败: "+err.Error())
		return
	}

	app.Logger.Log(hsName, "INFO", "公网地址释放",
		"已释放 "+publicAddr+"（虚拟机 "+vmUUID+"）", true)
	utils.SuccessMsg(c, "公网地址已释放", nil)
}
