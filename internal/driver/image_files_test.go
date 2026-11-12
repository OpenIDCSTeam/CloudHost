package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// newLocalImageDriver 构造一台「本机」主机驱动（server_addr 为空 → 本地通道）
func newLocalImageDriver(t *testing.T) *BaseDriver {
	t.Helper()
	home := t.TempDir()
	cfg := model.NewHSConfig()
	cfg.ServerAddr = ""
	cfg.ImagesPath = filepath.Join(home, "images")
	cfg.DvdromPath = filepath.Join(home, "iso")
	return NewBaseDriver(cfg)
}

func TestImageFileLocalLifecycle(t *testing.T) {
	d := newLocalImageDriver(t)

	if _, err := d.UploadImageFile(ImageKindSystem, "ubuntu.qcow2", strings.NewReader("disk-image")); err != nil {
		t.Fatalf("上传系统模板失败: %v", err)
	}
	if _, err := d.UploadImageFile(ImageKindISO, "netinst.iso", strings.NewReader("iso-data")); err != nil {
		t.Fatalf("上传 ISO 失败: %v", err)
	}

	systemFiles, err := d.ListImageFiles(ImageKindSystem)
	if err != nil {
		t.Fatalf("列出系统模板失败: %v", err)
	}
	if len(systemFiles) != 1 || systemFiles[0].Name != "ubuntu.qcow2" || systemFiles[0].Size != int64(len("disk-image")) {
		t.Fatalf("系统模板列表不符合预期: %+v", systemFiles)
	}

	isoFiles, err := d.ListImageFiles(ImageKindISO)
	if err != nil {
		t.Fatalf("列出 ISO 失败: %v", err)
	}
	if len(isoFiles) != 1 || isoFiles[0].Name != "netinst.iso" {
		t.Fatalf("ISO 列表不符合预期: %+v", isoFiles)
	}

	if err := d.RenameImageFile(ImageKindSystem, "ubuntu.qcow2", "ubuntu-24.04.qcow2"); err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	systemFiles, _ = d.ListImageFiles(ImageKindSystem)
	if len(systemFiles) != 1 || systemFiles[0].Name != "ubuntu-24.04.qcow2" {
		t.Fatalf("重命名后列表不符合预期: %+v", systemFiles)
	}

	if err := d.DeleteImageFile(ImageKindISO, "netinst.iso"); err != nil {
		t.Fatalf("删除 ISO 失败: %v", err)
	}
	isoFiles, _ = d.ListImageFiles(ImageKindISO)
	if len(isoFiles) != 0 {
		t.Fatalf("删除后 ISO 目录应为空: %+v", isoFiles)
	}
}

// 系统模板与 ISO 指向同一目录时，必须按后缀区分，不能两边都列出全部文件
func TestImageFilesSplitByExtensionInSameDir(t *testing.T) {
	home := t.TempDir()
	cfg := model.NewHSConfig()
	cfg.ImagesPath = home
	cfg.DvdromPath = home
	d := NewBaseDriver(cfg)

	if err := os.WriteFile(filepath.Join(home, "base.qcow2"), []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "netinst.iso"), []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	systemFiles, err := d.ListImageFiles(ImageKindSystem)
	if err != nil {
		t.Fatalf("列出系统模板失败: %v", err)
	}
	// .txt 不属于 ISO，按「排除 .iso」规则仍会出现在系统模板里
	if len(systemFiles) != 2 {
		t.Fatalf("系统模板应只排除 ISO: %+v", systemFiles)
	}
	for _, f := range systemFiles {
		if strings.EqualFold(filepath.Ext(f.Name), ".iso") {
			t.Fatalf("系统模板列表不应包含 ISO: %+v", f)
		}
	}

	isoFiles, err := d.ListImageFiles(ImageKindISO)
	if err != nil {
		t.Fatalf("列出 ISO 失败: %v", err)
	}
	if len(isoFiles) != 1 || isoFiles[0].Name != "netinst.iso" {
		t.Fatalf("ISO 列表应只包含 .iso: %+v", isoFiles)
	}

	if _, err := d.UploadImageFile(ImageKindSystem, "wrong.iso", strings.NewReader("x")); err == nil {
		t.Fatal("系统模板页签应拒绝 .iso 上传")
	}
	if _, err := d.UploadImageFile(ImageKindISO, "wrong.qcow2", strings.NewReader("x")); err == nil {
		t.Fatal("ISO 页签应拒绝非 .iso 上传")
	}
}

func TestImageFileRejectsPathTraversal(t *testing.T) {
	d := newLocalImageDriver(t)

	for _, name := range []string{"../evil.qcow2", "sub/dir.iso", `sub\dir.iso`, "..", ""} {
		if _, err := d.UploadImageFile(ImageKindSystem, name, strings.NewReader("x")); err == nil {
			t.Fatalf("文件名 %q 应被拒绝", name)
		}
	}
	if err := d.DeleteImageFile(ImageKindISO, "../x.iso"); err == nil {
		t.Fatal("删除时应拒绝路径穿越文件名")
	}
	if err := d.RenameImageFile(ImageKindISO, "a.iso", "../b.iso"); err == nil {
		t.Fatal("重命名时应拒绝路径穿越文件名")
	}
}

func TestImageDirFallbacks(t *testing.T) {
	cfg := model.NewHSConfig()
	cfg.ImagesPath = "/var/lib/libvirt/images"
	d := NewBaseDriver(cfg)

	if dir, err := d.ImageDir(ImageKindSystem); err != nil || dir != "/var/lib/libvirt/images" {
		t.Fatalf("系统模板目录解析异常: %q %v", dir, err)
	}
	// dvdrom_path 为空时回退到 images_path
	if dir, err := d.ImageDir(ImageKindISO); err != nil || dir != "/var/lib/libvirt/images" {
		t.Fatalf("ISO 目录回退异常: %q %v", dir, err)
	}

	empty := NewBaseDriver(model.NewHSConfig())
	if _, err := empty.ImageDir(ImageKindSystem); err == nil {
		t.Fatal("未配置目录时应返回错误")
	}
}

func TestParseFindOutput(t *testing.T) {
	out := "ubuntu.qcow2\t1024\t1699999999.1234567890\nwin 10.iso\t2048\t1700000000.0000000000\n"
	files := parseFindOutput(out)
	if len(files) != 2 {
		t.Fatalf("应解析出 2 个文件: %+v", files)
	}
	if files[0].Name != "ubuntu.qcow2" || files[0].Size != 1024 || files[0].ModTime != 1699999999 {
		t.Fatalf("解析结果异常: %+v", files[0])
	}
	// 文件名中的空格必须保留
	if files[1].Name != "win 10.iso" || files[1].Size != 2048 {
		t.Fatalf("含空格文件名解析异常: %+v", files[1])
	}
}
