// Package api 主机镜像/文件管理处理器。
//
// 提供「主机侧系统模板与 ISO 目录」的浏览、上传、重命名、删除，
// 以及与镜像管理（hs_config.system_maps / images_maps）的绑定操作。
package api

import (
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/driver"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/pkg/utils"
)

// hostImageFileRow 列表返回项：主机文件 + 镜像管理绑定状态
type hostImageFileRow struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
	Bound   bool   `json:"bound"`
	SysName string `json:"sys_name"`
	SysSize string `json:"sys_size"`
	SysType string `json:"sys_type"`
	SysFlag bool   `json:"sys_flag"`
}

// hostImageManager 取主机的镜像文件管理能力
func (app *App) hostImageManager(hsName string) (driver.ImageFileManager, *model.HSConfig, error) {
	cfg, err := app.HostSvc.GetHost(hsName)
	if err != nil {
		return nil, nil, fmt.Errorf("查询主机失败: %w", err)
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("主机不存在")
	}
	if !cfg.EnableHost {
		return nil, nil, fmt.Errorf("主机已禁用，启用后才能管理镜像文件")
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		return nil, nil, fmt.Errorf("主机驱动不可用")
	}
	mgr, ok := d.(driver.ImageFileManager)
	if !ok {
		return nil, nil, fmt.Errorf("当前平台不支持镜像文件管理")
	}
	return mgr, cfg, nil
}

// imageFileKey 归一化镜像文件引用，用于「文件 ↔ 镜像清单」比对。
//
// 清单里的 sys_file 可能带目录或 PVE 卷前缀（local:iso/foo.iso），
// 统一取文件名小写比较，避免同一条目被判定为未绑定。
func imageFileKey(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if idx := strings.LastIndexAny(v, "/\\"); idx >= 0 {
		v = v[idx+1:]
	}
	return strings.ToLower(v)
}

// replaceImageFileRef 重命名时保留原有目录/存储前缀，只替换文件名部分
func replaceImageFileRef(raw, newName string) string {
	if idx := strings.LastIndexAny(raw, "/\\"); idx >= 0 {
		return raw[:idx+1] + newName
	}
	return newName
}

// imageMapsOf 返回镜像类型对应的清单（指针，便于原地修改）
func imageMapsOf(cfg *model.HSConfig, kind string) *[]model.OSConfig {
	if driver.NormalizeImageKind(kind) == driver.ImageKindISO {
		return &cfg.ImagesMaps
	}
	return &cfg.SystemMaps
}

// handleHostImageList GET /api/server/images/:hs_name?kind=system|iso
//
// 返回主机目录、目录内文件以及每个文件在镜像管理中的绑定状态。
func (app *App) handleHostImageList(c *gin.Context) {
	hsName := c.Param("hs_name")
	kind := driver.NormalizeImageKind(c.Query("kind"))

	mgr, cfg, err := app.hostImageManager(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	payload := gin.H{"kind": kind, "host_name": hsName, "files": []hostImageFileRow{}}
	if dir, dirErr := mgr.ImageDir(kind); dirErr == nil {
		payload["dir"] = dir
	}

	files, listErr := mgr.ListImageFiles(kind)
	if listErr != nil {
		payload["error"] = listErr.Error()
		utils.Success(c, payload)
		return
	}

	bound := make(map[string]*model.OSConfig)
	maps := imageMapsOf(cfg, kind)
	for i := range *maps {
		item := &(*maps)[i]
		if key := imageFileKey(item.SysFile); key != "" {
			bound[key] = item
		}
	}

	rows := make([]hostImageFileRow, 0, len(files))
	for _, f := range files {
		row := hostImageFileRow{Name: f.Name, Size: f.Size, ModTime: f.ModTime}
		if item, ok := bound[imageFileKey(f.Name)]; ok {
			row.Bound = true
			row.SysName = item.SysName
			row.SysSize = item.SysSize
			row.SysType = item.SysType
			row.SysFlag = item.SysFlag
		}
		rows = append(rows, row)
	}
	payload["files"] = rows
	utils.Success(c, payload)
}

// handleHostImageUpload POST /api/server/images/:hs_name/upload
//
// multipart/form-data，字段顺序需为 kind 在前、file 在后。
// 采用流式读取，大镜像不落地到控制端临时目录。
func (app *App) handleHostImageUpload(c *gin.Context) {
	hsName := c.Param("hs_name")
	mgr, _, err := app.hostImageManager(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	reader, err := c.Request.MultipartReader()
	if err != nil {
		utils.BadRequest(c, "请求必须是 multipart/form-data 格式")
		return
	}

	kind := driver.ImageKindSystem
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
		case "kind":
			buf, _ := io.ReadAll(io.LimitReader(part, 64))
			kind = driver.NormalizeImageKind(string(buf))
			_ = part.Close()
		case "file":
			// 浏览器可能带上客户端路径（Windows 反斜杠），统一取文件名
			rawName := strings.ReplaceAll(strings.TrimSpace(part.FileName()), "\\", "/")
			fileName := path.Base(rawName)
			if fileName == "" || fileName == "." || fileName == "/" {
				_ = part.Close()
				utils.BadRequest(c, "上传文件缺少文件名")
				return
			}
			size, upErr := mgr.UploadImageFile(kind, fileName, part)
			_ = part.Close()
			if upErr != nil {
				utils.BadRequest(c, upErr.Error())
				return
			}
			app.logEvent(c, "host", "上传镜像").Host(hsName).
				Detail("kind", kind).Detail("file", fileName).Detail("size", size).
				Info("上传主机镜像成功：%s/%s", hsName, fileName)
			utils.SuccessMsg(c, "上传完成", gin.H{"kind": kind, "name": fileName, "size": size})
			return
		default:
			_ = part.Close()
		}
	}

	utils.BadRequest(c, "未找到上传文件")
}

// handleHostImageRename POST /api/server/images/:hs_name/rename
func (app *App) handleHostImageRename(c *gin.Context) {
	hsName := c.Param("hs_name")
	var req struct {
		Kind    string `json:"kind"`
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	oldName := strings.TrimSpace(req.OldName)
	newName := strings.TrimSpace(req.NewName)
	if oldName == "" || newName == "" {
		utils.BadRequest(c, "原文件名与新文件名不能为空")
		return
	}
	kind := driver.NormalizeImageKind(req.Kind)

	mgr, cfg, err := app.hostImageManager(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := mgr.RenameImageFile(kind, oldName, newName); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	// 已绑定的条目跟随改名，避免镜像管理里留下失效引用
	changed := false
	maps := imageMapsOf(cfg, kind)
	for i := range *maps {
		item := &(*maps)[i]
		if imageFileKey(item.SysFile) != imageFileKey(oldName) {
			continue
		}
		item.SysFile = replaceImageFileRef(item.SysFile, newName)
		changed = true
	}
	if changed {
		if err := app.HostSvc.UpdateImageMaps(hsName, cfg.SystemMaps, cfg.ImagesMaps); err != nil {
			utils.InternalError(c, "文件已重命名，但更新镜像绑定失败: "+err.Error())
			return
		}
	}

	app.logEvent(c, "host", "重命名镜像").Host(hsName).
		Detail("kind", kind).Detail("old", oldName).Detail("new", newName).
		Info("重命名主机镜像：%s/%s → %s", hsName, oldName, newName)
	utils.SuccessMsg(c, "重命名成功", gin.H{"bound_updated": changed})
}

// handleHostImageDelete DELETE /api/server/images/:hs_name?kind=&name=
func (app *App) handleHostImageDelete(c *gin.Context) {
	hsName := c.Param("hs_name")
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		utils.BadRequest(c, "缺少文件名")
		return
	}
	kind := driver.NormalizeImageKind(c.Query("kind"))

	mgr, cfg, err := app.hostImageManager(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	if err := mgr.DeleteImageFile(kind, name); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	// 文件已删除，镜像清单中的对应绑定同步移除
	kept := make([]model.OSConfig, 0)
	maps := imageMapsOf(cfg, kind)
	target := imageFileKey(name)
	for _, item := range *maps {
		if imageFileKey(item.SysFile) == target {
			continue
		}
		kept = append(kept, item)
	}
	unbound := len(kept) != len(*maps)
	if unbound {
		*maps = kept
		if err := app.HostSvc.UpdateImageMaps(hsName, cfg.SystemMaps, cfg.ImagesMaps); err != nil {
			utils.InternalError(c, "文件已删除，但清理镜像绑定失败: "+err.Error())
			return
		}
	}

	app.logEvent(c, "host", "删除镜像").Host(hsName).
		Detail("kind", kind).Detail("file", name).
		Info("删除主机镜像文件：%s/%s", hsName, name)
	utils.SuccessMsg(c, "删除成功", gin.H{"unbound": unbound})
}

// handleHostImageBind POST /api/server/images/:hs_name/bind
//
// 把主机目录中的文件登记到镜像管理：system → system_maps，iso → images_maps。
func (app *App) handleHostImageBind(c *gin.Context) {
	hsName := c.Param("hs_name")
	var req struct {
		Kind    string `json:"kind"`
		File    string `json:"file"`
		SysName string `json:"sys_name"`
		SysSize string `json:"sys_size"`
		SysType string `json:"sys_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请求参数错误")
		return
	}
	file := strings.TrimSpace(req.File)
	if file == "" {
		utils.BadRequest(c, "缺少镜像文件")
		return
	}
	sysName := strings.TrimSpace(req.SysName)
	if sysName == "" {
		utils.BadRequest(c, "显示名称不能为空")
		return
	}
	kind := driver.NormalizeImageKind(req.Kind)
	sysType := strings.TrimSpace(req.SysType)
	if kind == driver.ImageKindSystem && sysType == "" {
		sysType = "Linux"
	}

	mgr, cfg, err := app.hostImageManager(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	// 校验文件确实存在于目标目录，避免登记出永远无法使用的条目
	files, listErr := mgr.ListImageFiles(kind)
	if listErr != nil {
		utils.BadRequest(c, "读取主机目录失败: "+listErr.Error())
		return
	}
	exists := false
	for _, f := range files {
		if imageFileKey(f.Name) == imageFileKey(file) {
			exists = true
			break
		}
	}
	if !exists {
		utils.BadRequest(c, "镜像文件不存在: "+file)
		return
	}

	maps := imageMapsOf(cfg, kind)
	entry := model.OSConfig{
		SysName: sysName,
		SysFile: file,
		SysSize: strings.TrimSpace(req.SysSize),
		SysType: sysType,
		SysFlag: true,
	}

	replaced := false
	for i := range *maps {
		if imageFileKey((*maps)[i].SysFile) != imageFileKey(file) {
			continue
		}
		(*maps)[i] = entry
		replaced = true
		break
	}
	if !replaced {
		*maps = append(*maps, entry)
	}

	if err := app.HostSvc.UpdateImageMaps(hsName, cfg.SystemMaps, cfg.ImagesMaps); err != nil {
		utils.InternalError(c, "绑定失败: "+err.Error())
		return
	}

	app.logEvent(c, "host", "绑定镜像").Host(hsName).
		Detail("kind", kind).Detail("file", file).Detail("sys_name", sysName).
		Info("绑定主机镜像到镜像管理：%s/%s", hsName, file)
	utils.SuccessMsg(c, "绑定成功", entry)
}

// handleHostPlatformInfo GET /api/server/platform/:hs_name
//
// 按主机平台返回可查看的配置项目（PVE 网络与存储、QEMU 网桥与 libvirt 网络、
// Hyper-V 虚拟交换机等）。未实现 PlatformInfoProvider 的平台返回空列表，
// 前端据此隐藏「平台配置」页签。
func (app *App) handleHostPlatformInfo(c *gin.Context) {
	hsName := c.Param("hs_name")
	cfg, err := app.HostSvc.GetHost(hsName)
	if err != nil || cfg == nil {
		utils.BadRequest(c, "主机不存在")
		return
	}
	payload := gin.H{
		"host_name":   hsName,
		"server_type": cfg.ServerType,
		"host_os":     driver.NewHostOps(cfg).HostOS(),
		"sections":    []driver.HostPlatformSection{},
	}
	if !cfg.EnableHost {
		utils.Success(c, payload)
		return
	}
	d := app.HostSvc.GetDriver(hsName)
	if d == nil {
		utils.Success(c, payload)
		return
	}
	provider, ok := d.(driver.PlatformInfoProvider)
	if !ok {
		utils.Success(c, payload)
		return
	}
	payload["sections"] = provider.PlatformSections()
	utils.Success(c, payload)
}

// handleHostImageUnbind DELETE /api/server/images/:hs_name/bind?kind=&name=
func (app *App) handleHostImageUnbind(c *gin.Context) {
	hsName := c.Param("hs_name")
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		utils.BadRequest(c, "缺少文件名")
		return
	}
	kind := driver.NormalizeImageKind(c.Query("kind"))

	_, cfg, err := app.hostImageManager(hsName)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	target := imageFileKey(name)
	maps := imageMapsOf(cfg, kind)
	kept := make([]model.OSConfig, 0, len(*maps))
	for _, item := range *maps {
		if imageFileKey(item.SysFile) == target {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == len(*maps) {
		utils.BadRequest(c, "该文件未绑定到镜像管理")
		return
	}
	*maps = kept

	if err := app.HostSvc.UpdateImageMaps(hsName, cfg.SystemMaps, cfg.ImagesMaps); err != nil {
		utils.InternalError(c, "解绑失败: "+err.Error())
		return
	}

	app.logEvent(c, "host", "解绑镜像").Host(hsName).
		Detail("kind", kind).Detail("file", name).
		Info("解除主机镜像绑定：%s/%s", hsName, name)
	utils.SuccessMsg(c, "解绑成功", nil)
}
