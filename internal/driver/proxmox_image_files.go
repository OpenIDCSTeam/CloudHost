package driver

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// ImageDir 覆盖基类实现：Proxmox 的 images_path / dvdrom_path 通常是「存储名」
// （如 local、local-lvm、local:iso），需要换算成节点上的物理目录。
func (d *ProxmoxDriver) ImageDir(kind string) (string, error) {
	if d.Config == nil {
		return "", fmt.Errorf("主机配置为空")
	}
	kind = NormalizeImageKind(kind)

	if kind == ImageKindISO {
		storage := strings.TrimSpace(d.Config.DvdromPath)
		if storage == "" {
			storage = strings.TrimSpace(d.Config.ImagesPath)
		}
		if storage == "" {
			return "", fmt.Errorf("主机未配置光盘镜像存储")
		}
		// local:iso → local
		if idx := strings.Index(storage, ":"); idx >= 0 {
			storage = strings.TrimSpace(storage[:idx])
		}
		// 已经填了绝对路径时直接使用
		if strings.Contains(storage, "/") {
			return storage, nil
		}
		return path.Join(d.nodeStoragePath(storage), "template", "iso"), nil
	}

	images := strings.TrimSpace(d.Config.ImagesPath)
	if images == "" {
		return "", fmt.Errorf("主机未配置系统镜像存储")
	}
	if strings.Contains(images, "/") {
		return images, nil
	}
	base := d.nodeStoragePath(images)
	// 与 proxmox_install.go 的导入路径保持一致：优先 template/import，其次 import
	if d.nodeDirExists(path.Join(base, "template", "import")) {
		return path.Join(base, "template", "import"), nil
	}
	if d.nodeDirExists(path.Join(base, "import")) {
		return path.Join(base, "import"), nil
	}
	return path.Join(base, "template", "import"), nil
}

// nodeDirExists 判断节点上目录是否存在
func (d *ProxmoxDriver) nodeDirExists(remotePath string) bool {
	cmd := fmt.Sprintf("test -d %s && echo exists || echo not_exists", shellQuote(remotePath))
	res, err := d.runOnNode(cmd, 30*time.Second)
	if err != nil || res == nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == "exists"
}
