// Package driver ESXi 镜像目录（数据存储）文件管理。
//
// ESXi 的 images_path / dvdrom_path 是数据存储路径（如 [datastore1] images/），
// 不能走基类的本机 / SSH 文件通道：server_addr 是 vSphere API 地址（默认 443），
// 用 SSH 连接会 handshake 失败（ssh: handshake failed: EOF）。
// 这里统一改用 vSphere Datastore / FileManager API。
package driver

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// ImageDir 返回镜像类型对应的数据存储目录（保留主机配置里的原始写法）。
func (d *ESXiDriver) ImageDir(kind string) (string, error) {
	dir := strings.TrimSpace(d.Config.ImagesPath)
	if NormalizeImageKind(kind) == ImageKindISO {
		dir = strings.TrimSpace(d.Config.DvdromPath)
		if dir == "" {
			dir = strings.TrimSpace(d.Config.ImagesPath)
		}
	}
	if dir == "" {
		return "", fmt.Errorf("主机未配置%s目录", imageKindLabel(kind))
	}
	return dir, nil
}

// esxiImageDatastore 解析镜像目录配置，返回数据存储对象与目录内相对路径。
func (d *ESXiDriver) esxiImageDatastore(kind string) (*object.Datastore, string, error) {
	dir, err := d.ImageDir(kind)
	if err != nil {
		return nil, "", err
	}
	if err := d.apiConn(); err != nil {
		return nil, "", fmt.Errorf("连接 vSphere 失败: %w", err)
	}

	dsName, sub, _ := esxiSplitDatastore(dir)
	if dsName == "" {
		return nil, "", fmt.Errorf("存储路径无效: %s", dir)
	}

	ds, err := d.finder.Datastore(d.Ctx(""), dsName)
	if err != nil {
		// 配置里的数据存储名写错时，把实际可用的名称带出来，避免用户反复猜
		if names := d.esxiDatastoreNames(); len(names) > 0 {
			return nil, "", fmt.Errorf("数据存储不存在: %s（当前可用：%s）",
				dsName, strings.Join(names, "、"))
		}
		return nil, "", fmt.Errorf("数据存储不存在: %s", dsName)
	}

	// 配置误填成具体文件时，取所在目录
	if sub != "" && path.Ext(sub) != "" {
		sub = path.Dir(sub)
		if sub == "." {
			sub = ""
		}
	}
	return ds, sub, nil
}

// ListImageFiles 列出数据存储目录内的文件（不递归）。
func (d *ESXiDriver) ListImageFiles(kind string) ([]HostImageFile, error) {
	ds, sub, err := d.esxiImageDatastore(kind)
	if err != nil {
		return nil, err
	}

	ctx := d.Ctx("")
	browser, err := ds.Browser(ctx)
	if err != nil {
		return nil, fmt.Errorf("打开数据存储浏览器失败: %w", err)
	}

	spec := types.HostDatastoreBrowserSearchSpec{
		Details: &types.FileQueryFlags{FileType: true, FileSize: true, Modification: true},
	}
	task, err := browser.SearchDatastore(ctx, ds.Path(sub), &spec)
	if err != nil {
		return nil, fmt.Errorf("读取数据存储目录失败: %w", err)
	}
	ti, err := task.WaitForResult(ctx, nil)
	if err != nil {
		if types.IsFileNotFound(err) {
			return nil, fmt.Errorf("数据存储目录不存在: %s", ds.Path(sub))
		}
		return nil, fmt.Errorf("读取数据存储目录失败: %w", err)
	}

	var results []types.HostDatastoreBrowserSearchResults
	switch r := ti.Result.(type) {
	case types.HostDatastoreBrowserSearchResults:
		results = []types.HostDatastoreBrowserSearchResults{r}
	case types.ArrayOfHostDatastoreBrowserSearchResults:
		results = r.HostDatastoreBrowserSearchResults
	default:
		return nil, fmt.Errorf("数据存储返回非预期类型 %T", ti.Result)
	}

	files := make([]HostImageFile, 0, 8)
	for _, res := range results {
		for _, fi := range res.File {
			if _, ok := fi.(*types.FolderFileInfo); ok {
				continue
			}
			info := fi.GetFileInfo()
			if info == nil {
				continue
			}
			item := HostImageFile{Name: path.Base(info.Path), Size: info.FileSize}
			if info.Modification != nil {
				item.ModTime = info.Modification.Unix()
			}
			files = append(files, item)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	// images_path 与 dvdrom_path 可能是同一个目录，按后缀区分系统模板与 ISO
	return filterImageFilesByKind(kind, files), nil
}

// countingReader 统计上传字节数（vSphere Upload 不返回写入长度）。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// UploadImageFile 通过数据存储 HTTP 通道上传文件（同名覆盖）。
func (d *ESXiDriver) UploadImageFile(kind, name string, r io.Reader) (int64, error) {
	name = strings.TrimSpace(name)
	if err := validateImageFileName(name); err != nil {
		return 0, err
	}
	kind = NormalizeImageKind(kind)
	// 系统模板与 ISO 可能共用一个目录，按后缀拒绝放错页签的文件
	if !imageFileExtAllowed(kind, name) {
		if kind == ImageKindISO {
			return 0, fmt.Errorf("ISO 页签只接受 .iso 文件")
		}
		return 0, fmt.Errorf("该文件是 ISO，请切换到 ISO 页签上传")
	}

	ds, sub, err := d.esxiImageDatastore(kind)
	if err != nil {
		return 0, err
	}

	ctx := d.Ctx("")
	if sub != "" {
		// 目录已存在时会报错，忽略即可；后续上传失败会给出明确错误
		fm := object.NewFileManager(d.client.Client)
		_ = fm.MakeDirectory(ctx, ds.Path(sub), d.datacenter, true)
	}

	// 必须用 DefaultUpload（Method=PUT）；零值 Upload 的 Method 为空，
	// http.NewRequest 会退化成 GET，表现为 404 Not Found。
	param := soap.DefaultUpload
	cr := &countingReader{r: r}
	if err := ds.Upload(ctx, cr, path.Join(sub, name), &param); err != nil {
		return cr.n, fmt.Errorf("上传到数据存储失败: %w", err)
	}
	return cr.n, nil
}

// DeleteImageFile 删除数据存储目录内的文件。
func (d *ESXiDriver) DeleteImageFile(kind, name string) error {
	name = strings.TrimSpace(name)
	if err := validateImageFileName(name); err != nil {
		return err
	}
	ds, sub, err := d.esxiImageDatastore(kind)
	if err != nil {
		return err
	}

	ctx := d.Ctx("")
	fm := object.NewFileManager(d.client.Client)
	task, callErr := fm.DeleteDatastoreFile(ctx, ds.Path(path.Join(sub, name)), d.datacenter)
	var opErr error
	if callErr != nil {
		opErr = callErr
	} else if err := task.Wait(ctx); err != nil {
		if types.IsFileNotFound(err) {
			return fmt.Errorf("文件不存在: %s", name)
		}
		opErr = err
	}
	if opErr == nil {
		return nil
	}
	if !isESXiLicenseBlocked(opErr) {
		return fmt.Errorf("删除文件失败: %w", opErr)
	}

	// 免费版 ESXi 禁止 FileManager 删除数据存储文件，退回 SSH（22 端口）
	if _, sshErr := d.esxiRunShell(fmt.Sprintf("rm -f %s",
		shellQuote(d.esxiShellPath(ds.Name(), sub, name)))); sshErr != nil {
		return fmt.Errorf("删除文件失败：ESXi 免费版许可证禁止数据存储文件操作，SSH 兜底也不可用（%v）", sshErr)
	}
	return nil
}

// isESXiLicenseBlocked 判断错误是否由 ESXi 免费版许可证限制引起。
//
// 典型报错：Current license or ESXi version prohibits execution of the requested operation.
func isESXiLicenseBlocked(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "prohibits execution") || strings.Contains(msg, "license")
}

// esxiDatastoreNames 列出当前可用的数据存储名称（用于错误提示）
func (d *ESXiDriver) esxiDatastoreNames() []string {
	ctx, cancel := context.WithTimeout(d.Ctx(""), 15*time.Second)
	defer cancel()
	list, err := d.finder.DatastoreList(ctx, "*")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(list))
	for _, ds := range list {
		if ds != nil && strings.TrimSpace(ds.Name()) != "" {
			names = append(names, ds.Name())
		}
	}
	sort.Strings(names)
	return names
}

// esxiShellPath 返回文件在 ESXi Shell 下的绝对路径（/vmfs/volumes/<数据存储名>/...）。
func (d *ESXiDriver) esxiShellPath(dsName, sub, name string) string {
	p := path.Join("/vmfs/volumes", dsName)
	if sub != "" {
		p = path.Join(p, sub)
	}
	return path.Join(p, name)
}

// esxiRunShell 通过 SSH（默认 22 端口）在 ESXi 上执行命令。
//
// 注意 server_addr/server_port 指向的是 vSphere API（443），
// 不能直接复用，否则会像基类那样出现「SSH 连接 :443 handshake failed」。
func (d *ESXiDriver) esxiRunShell(cmd string) (string, error) {
	host := stripSSHScheme(d.Config.ServerAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	res, err := RunSSHCommand(SSHExecConfig{
		Host:     host,
		Port:     22,
		User:     d.Config.ServerUser,
		Password: d.Config.ServerPass,
		Timeout:  20 * time.Second,
	}, cmd)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return strings.TrimSpace(res.Stderr), fmt.Errorf("命令退出码 %d", res.ExitCode)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// RenameImageFile 重命名数据存储目录内的文件。
func (d *ESXiDriver) RenameImageFile(kind, oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if err := validateImageFileName(oldName); err != nil {
		return err
	}
	if err := validateImageFileName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}

	ds, sub, err := d.esxiImageDatastore(kind)
	if err != nil {
		return err
	}

	ctx := d.Ctx("")
	fm := object.NewFileManager(d.client.Client)
	task, callErr := fm.MoveDatastoreFile(ctx,
		ds.Path(path.Join(sub, oldName)), d.datacenter,
		ds.Path(path.Join(sub, newName)), d.datacenter, false)
	var opErr error
	if callErr != nil {
		opErr = callErr
	} else if err := task.Wait(ctx); err != nil {
		if types.IsFileNotFound(err) {
			return fmt.Errorf("文件不存在: %s", oldName)
		}
		opErr = err
	}
	if opErr == nil {
		return nil
	}
	if !isESXiLicenseBlocked(opErr) {
		return fmt.Errorf("重命名失败: %w", opErr)
	}

	// 免费版 ESXi 禁止 FileManager 移动数据存储文件，退回 SSH（22 端口）
	if _, sshErr := d.esxiRunShell(fmt.Sprintf("mv -f %s %s",
		shellQuote(d.esxiShellPath(ds.Name(), sub, oldName)),
		shellQuote(d.esxiShellPath(ds.Name(), sub, newName)))); sshErr != nil {
		return fmt.Errorf("重命名失败：ESXi 免费版许可证禁止数据存储文件操作，SSH 兜底也不可用（%v）", sshErr)
	}
	return nil
}
