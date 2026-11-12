// Package driver 主机镜像目录文件管理。
//
// 「镜像/文件管理」面向主机侧的真实文件：系统模板（images_path）与
// 光盘镜像（dvdrom_path）。基类提供本地文件系统与 SSH 两条通道的默认实现，
// 平台驱动可按需覆盖目录解析（见 ProxmoxDriver.ImageDir）。
package driver

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// ImageKindSystem 系统模板（磁盘镜像）
	ImageKindSystem = "system"
	// ImageKindISO 光盘镜像（ISO）
	ImageKindISO = "iso"
)

// HostImageFile 主机镜像目录中的一个文件
type HostImageFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

// ImageFileManager 可选接口：管理主机侧镜像目录（系统模板 / ISO）。
//
// BaseDriver 已实现全部方法，平台驱动只需在目录语义不同时覆盖 ImageDir。
type ImageFileManager interface {
	// ImageDir 返回该镜像类型对应的主机目录
	ImageDir(kind string) (string, error)
	// ListImageFiles 列出目录内的普通文件（不递归）
	ListImageFiles(kind string) ([]HostImageFile, error)
	// UploadImageFile 将 r 写入目录内指定文件名，返回写入字节数（同名覆盖）
	UploadImageFile(kind, name string, r io.Reader) (int64, error)
	// DeleteImageFile 删除目录内文件
	DeleteImageFile(kind, name string) error
	// RenameImageFile 重命名目录内文件
	RenameImageFile(kind, oldName, newName string) error
}

// NormalizeImageKind 归一化镜像类型，未知值按系统模板处理
func NormalizeImageKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), ImageKindISO) {
		return ImageKindISO
	}
	return ImageKindSystem
}

// validateImageFileName 校验文件名：必须是纯文件名，禁止路径穿越
func validateImageFileName(name string) error {
	name = strings.TrimSpace(name)
	switch name {
	case "", ".", "..":
		return fmt.Errorf("文件名不能为空")
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("文件名不能包含路径分隔符")
	}
	return nil
}

// imageKindLabel 返回镜像类型的中文名，用于错误提示
func imageKindLabel(kind string) string {
	if NormalizeImageKind(kind) == ImageKindISO {
		return "光盘镜像"
	}
	return "系统模板"
}

// ImageDir 返回镜像类型对应的主机目录（基类实现：直接使用主机配置里的路径）。
func (b *BaseDriver) ImageDir(kind string) (string, error) {
	if b.Config == nil {
		return "", fmt.Errorf("主机配置为空")
	}
	kind = NormalizeImageKind(kind)

	dir := strings.TrimSpace(b.Config.ImagesPath)
	if kind == ImageKindISO {
		dir = strings.TrimSpace(b.Config.DvdromPath)
		if dir == "" {
			dir = strings.TrimSpace(b.Config.ImagesPath)
		}
	}
	if dir == "" {
		return "", fmt.Errorf("主机未配置%s目录", imageKindLabel(kind))
	}
	return dir, nil
}

// resolvedImageDir 派发到最外层驱动的 ImageDir（模拟虚方法），
// 让 BaseDriver 的通用文件操作能用到平台覆盖后的目录。
func (b *BaseDriver) resolvedImageDir(kind string) (string, error) {
	if self := b.SelfDriver(); self != nil {
		if p, ok := self.(ImageFileManager); ok {
			return p.ImageDir(kind)
		}
	}
	return b.ImageDir(kind)
}

// localOnlyImageHost 判断该引擎的镜像目录是否只能由控制端本机访问。
//
// Hyper-V / Workstation / VirtualBox / 逍遥安卓的驱动实现本身是
// 在控制端本机执行命令行，server_addr 即使填了地址也不代表可以 SSH 过去。
func localOnlyImageHost(serverType string) bool {
	switch serverType {
	case "HyperVSetup", "VMWareSetup", "VirtualBoxs", "MemuAndroid":
		return true
	}
	return false
}

// imageHostIsRemote 判断镜像目录是否需要通过 SSH 访问
func (b *BaseDriver) imageHostIsRemote() bool {
	if b.Config == nil {
		return false
	}
	if localOnlyImageHost(b.Config.ServerType) {
		return false
	}
	return newHostCommandRunner(b.Config).isRemote()
}

// ListImageFiles 列出主机镜像目录内的文件
func (b *BaseDriver) ListImageFiles(kind string) ([]HostImageFile, error) {
	dir, err := b.resolvedImageDir(kind)
	if err != nil {
		return nil, err
	}

	if b.imageHostIsRemote() {
		// -printf 依赖 GNU find（PVE/Debian/CentOS 均满足）；
		// 用 \t 分隔避免文件名中的空格干扰解析。
		cmd := fmt.Sprintf("find %s -maxdepth 1 -type f -printf '%%f\\t%%s\\t%%T@\\n'",
			shellQuote(dir))
		out, err := newHostCommandRunner(b.Config).runShell(cmd, 60*time.Second)
		if err != nil {
			return nil, fmt.Errorf("读取主机目录失败: %w", err)
		}
		return parseFindOutput(out), nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取主机目录失败: %w", err)
	}
	files := make([]HostImageFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		files = append(files, HostImageFile{
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		})
	}
	return files, nil
}

// parseFindOutput 解析 find -printf '%f\t%s\t%T@\n' 的输出
func parseFindOutput(out string) []HostImageFile {
	files := []HostImageFile{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if name == "" {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		var modTime int64
		if len(parts) == 3 {
			// %T@ 形如 1699999999.1234567890
			ts := strings.TrimSpace(parts[2])
			if idx := strings.Index(ts, "."); idx >= 0 {
				ts = ts[:idx]
			}
			modTime, _ = strconv.ParseInt(ts, 10, 64)
		}
		files = append(files, HostImageFile{Name: name, Size: size, ModTime: modTime})
	}
	return files
}

// UploadImageFile 上传文件到主机镜像目录（先写临时文件，成功后改名，避免半截文件）
func (b *BaseDriver) UploadImageFile(kind, name string, r io.Reader) (int64, error) {
	name = strings.TrimSpace(name)
	if err := validateImageFileName(name); err != nil {
		return 0, err
	}
	dir, err := b.resolvedImageDir(kind)
	if err != nil {
		return 0, err
	}
	tmpName := name + ".uploading"

	if b.imageHostIsRemote() {
		// PVE 的 template/import 等目录默认不存在，首次上传时按需创建
		if _, err := newHostCommandRunner(b.Config).runShell(
			fmt.Sprintf("mkdir -p %s", shellQuote(dir)), 60*time.Second); err != nil {
			return 0, fmt.Errorf("创建主机目录失败: %w", err)
		}
		target := path.Join(dir, name)
		tmp := path.Join(dir, tmpName)
		n, stderr, err := runSSHStream(sshConfigFromHS(b.Config),
			fmt.Sprintf("cat > %s", shellQuote(tmp)), r)
		if err != nil {
			_, _ = newHostCommandRunner(b.Config).runShell(
				fmt.Sprintf("rm -f %s", shellQuote(tmp)), 30*time.Second)
			msg := strings.TrimSpace(stderr)
			if msg == "" {
				msg = err.Error()
			}
			return n, fmt.Errorf("上传到主机失败: %s", msg)
		}
		if _, err := newHostCommandRunner(b.Config).runShell(
			fmt.Sprintf("mv -f %s %s", shellQuote(tmp), shellQuote(target)), 60*time.Second); err != nil {
			return n, fmt.Errorf("保存文件失败: %w", err)
		}
		return n, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("创建目录失败: %w", err)
	}
	tmp := filepath.Join(dir, tmpName)
	f, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("创建文件失败: %w", err)
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("写入文件失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("写入文件失败: %w", closeErr)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("保存文件失败: %w", err)
	}
	return n, nil
}

// DeleteImageFile 删除主机镜像目录内的文件
func (b *BaseDriver) DeleteImageFile(kind, name string) error {
	name = strings.TrimSpace(name)
	if err := validateImageFileName(name); err != nil {
		return err
	}
	dir, err := b.resolvedImageDir(kind)
	if err != nil {
		return err
	}

	if b.imageHostIsRemote() {
		if _, err := newHostCommandRunner(b.Config).runShell(
			fmt.Sprintf("rm -f %s", shellQuote(path.Join(dir, name))), 60*time.Second); err != nil {
			return fmt.Errorf("删除文件失败: %w", err)
		}
		return nil
	}

	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("文件不存在: %s", name)
		}
		return fmt.Errorf("删除文件失败: %w", err)
	}
	return nil
}

// RenameImageFile 重命名主机镜像目录内的文件
func (b *BaseDriver) RenameImageFile(kind, oldName, newName string) error {
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
	dir, err := b.resolvedImageDir(kind)
	if err != nil {
		return err
	}

	if b.imageHostIsRemote() {
		if _, err := newHostCommandRunner(b.Config).runShell(
			fmt.Sprintf("mv -f %s %s",
				shellQuote(path.Join(dir, oldName)), shellQuote(path.Join(dir, newName))),
			60*time.Second); err != nil {
			return fmt.Errorf("重命名失败: %w", err)
		}
		return nil
	}

	if err := os.Rename(filepath.Join(dir, oldName), filepath.Join(dir, newName)); err != nil {
		return fmt.Errorf("重命名失败: %w", err)
	}
	return nil
}

// runSSHStream 通过 SSH 执行命令并把 r 写入其 stdin（用于大文件上传）。
//
// 与 RunSSHCommand 的区别：不设整体超时，传输时长取决于文件大小与链路，
// 调用方（HTTP 请求）断开时读 r 会出错，从而中止传输。
func runSSHStream(cfg SSHExecConfig, cmd string, r io.Reader) (int64, string, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return 0, "", fmt.Errorf("SSH 主机地址为空")
	}
	if cfg.Port <= 0 {
		cfg.Port = 22
	}
	if strings.TrimSpace(cfg.User) == "" {
		cfg.User = "root"
	}

	clientCfg := &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(cfg.Password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = cfg.Password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return 0, "", fmt.Errorf("SSH 连接 %s 失败: %w", addr, err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		return 0, "", fmt.Errorf("SSH 创建会话失败: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stderr = &stderr

	stdin, err := session.StdinPipe()
	if err != nil {
		return 0, "", fmt.Errorf("SSH 打开写入通道失败: %w", err)
	}
	if err := session.Start(cmd); err != nil {
		return 0, "", fmt.Errorf("SSH 启动命令失败: %w", err)
	}

	n, copyErr := io.Copy(stdin, r)
	closeErr := stdin.Close()
	waitErr := session.Wait()

	if copyErr != nil {
		return n, stderr.String(), fmt.Errorf("读取上传数据失败: %w", copyErr)
	}
	if closeErr != nil {
		return n, stderr.String(), fmt.Errorf("关闭写入通道失败: %w", closeErr)
	}
	if waitErr != nil {
		return n, stderr.String(), fmt.Errorf("远端写入失败: %w", waitErr)
	}
	return n, stderr.String(), nil
}
