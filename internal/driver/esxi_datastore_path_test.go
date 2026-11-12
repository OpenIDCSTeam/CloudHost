package driver

import "testing"

// TestESXiDatastorePath 覆盖 ESXi 存储路径拼接。
//
// 主机表单里 images_path / dvdrom_path 按「目录」语义填写（如 [datastore1] images/），
// 文件名由 os_name / iso_file 提供；此前只处理了不含方括号的数据存储名，
// 填目录会被当成完整文件路径，重装/挂载 ISO 直接失败。
func TestESXiDatastorePath(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		file string
		want string
	}{
		{"仅数据存储名", "datastore1", "a.vmdk", "[datastore1] a.vmdk"},
		{"数据存储根目录", "[datastore1]", "a.vmdk", "[datastore1] a.vmdk"},
		{"子目录带斜杠", "[datastore1] images/", "a.vmdk", "[datastore1] images/a.vmdk"},
		{"子目录不带斜杠", "[datastore1] images", "a.vmdk", "[datastore1] images/a.vmdk"},
		{"配置已是完整文件", "[datastore1] images/a.vmdk", "a.vmdk", "[datastore1] images/a.vmdk"},
		{"文件名自带子目录不重复拼接", "[datastore1] images/", "images/a.vmdk", "[datastore1] images/a.vmdk"},
		{"目录为空时原样返回文件名", "", "a.vmdk", "a.vmdk"},
		{"无文件名时返回目录", "[datastore1] images/", "", "[datastore1] images"},
		{"数据存储名含空格", "[My Datastore] ISO/", "a.iso", "[My Datastore] ISO/a.iso"},
		{"实际模板文件", "[datastore1] images/", "windows10ltsc21x64img-1.vmdk", "[datastore1] images/windows10ltsc21x64img-1.vmdk"},
		{"实际 ISO 文件", "[datastore1] images/", "FirPE-64-V1.9.1.iso", "[datastore1] images/FirPE-64-V1.9.1.iso"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := esxiDatastorePath(tc.dir, tc.file); got != tc.want {
				t.Fatalf("esxiDatastorePath(%q, %q) = %q, want %q", tc.dir, tc.file, got, tc.want)
			}
		})
	}
}
