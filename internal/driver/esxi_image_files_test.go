package driver

import (
	"errors"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// TestESXiImageDir 校验 ESXi 镜像目录取哪个配置字段：
// 系统模板用 images_path，ISO 用 dvdrom_path（未配置时回退 images_path）。
func TestESXiImageDir(t *testing.T) {
	cases := []struct {
		name    string
		images  string
		dvdrom  string
		kind    string
		want    string
		wantErr bool
	}{
		{"系统模板", "[datastore1] images/", "", ImageKindSystem, "[datastore1] images/", false},
		{"ISO 独立目录", "[datastore1] images/", "[datastore1] iso/", ImageKindISO, "[datastore1] iso/", false},
		{"ISO 回退模板目录", "[datastore1] images/", "", ImageKindISO, "[datastore1] images/", false},
		{"系统模板未配置", "", "[datastore1] iso/", ImageKindSystem, "", true},
		{"ISO 均未配置", "", "", ImageKindISO, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := model.NewHSConfig()
			cfg.ImagesPath = tc.images
			cfg.DvdromPath = tc.dvdrom
			d := NewESXiDriver(cfg)

			got, err := d.ImageDir(tc.kind)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ImageDir(%q) 期望报错，实际返回 %q", tc.kind, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ImageDir(%q) 报错: %v", tc.kind, err)
			}
			if got != tc.want {
				t.Fatalf("ImageDir(%q) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestIsESXiLicenseBlocked 免费版许可证报错要能识别出来，其它错误不能误判。
func TestIsESXiLicenseBlocked(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"免费版限制", errors.New("ServerFaultCode: Current license or ESXi version prohibits execution of the requested operation."), true},
		{"许可证过期", errors.New("License has expired"), true},
		{"文件不存在", errors.New("FileNotFound"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isESXiLicenseBlocked(tc.err); got != tc.want {
				t.Fatalf("isESXiLicenseBlocked(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestESXiShellPath ESXi Shell 视角的数据存储绝对路径。
func TestESXiShellPath(t *testing.T) {
	d := NewESXiDriver(model.NewHSConfig())
	cases := []struct {
		ds, sub, name string
		want          string
	}{
		{"datastore1", "images", "a.vmdk", "/vmfs/volumes/datastore1/images/a.vmdk"},
		{"datastore1", "", "a.iso", "/vmfs/volumes/datastore1/a.iso"},
		{"My Datastore", "ISO", "x.iso", "/vmfs/volumes/My Datastore/ISO/x.iso"},
	}
	for _, tc := range cases {
		if got := d.esxiShellPath(tc.ds, tc.sub, tc.name); got != tc.want {
			t.Fatalf("esxiShellPath(%q,%q,%q) = %q, want %q", tc.ds, tc.sub, tc.name, got, tc.want)
		}
	}
}
