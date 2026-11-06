package config

import "testing"

func TestResolveByNameAndIndex(t *testing.T) {
	f := &File{
		Default: "local",
		Cores: []Core{
			{Name: "local", URL: "http://127.0.0.1:1881", Token: "a"},
			{Name: "prod", URL: "https://idc.example.com", Token: "b"},
		},
	}
	if c, err := f.Resolve(""); err != nil || c.Name != "local" {
		t.Fatalf("默认主控解析失败: %+v %v", c, err)
	}
	if c, err := f.Resolve("1"); err != nil || c.Name != "prod" {
		t.Fatalf("序号解析失败: %+v %v", c, err)
	}
	if c, err := f.Resolve("prod"); err != nil || c.URL != "https://idc.example.com" {
		t.Fatalf("名称解析失败: %+v %v", c, err)
	}
	if _, err := f.Resolve("missing"); err == nil {
		t.Fatal("不存在的主控应报错")
	}
	if _, err := f.Resolve("9"); err == nil {
		t.Fatal("越界序号应报错")
	}
}

func TestAddUpdateRemove(t *testing.T) {
	f := &File{}
	Add(f, Core{Name: "a", URL: "http://a", Token: "1"})
	Add(f, Core{Name: "b", URL: "http://b", Token: "2"})
	if len(f.Cores) != 2 || f.Default != "a" {
		t.Fatalf("新增失败: %+v", f)
	}
	Add(f, Core{Name: "a", URL: "http://a2", Token: "3"})
	if len(f.Cores) != 2 || f.Cores[0].URL != "http://a2" {
		t.Fatalf("更新失败: %+v", f.Cores)
	}
	if !Remove(f, "a") || f.Default != "b" {
		t.Fatalf("删除后默认值应切换: %+v", f)
	}
	if Remove(f, "nope") {
		t.Fatal("删除不存在的条目应返回 false")
	}
}

func TestMask(t *testing.T) {
	if got := Mask(""); got != "" {
		t.Fatalf("空 token: %q", got)
	}
	if got := Mask("12345678"); got != "****" {
		t.Fatalf("短 token: %q", got)
	}
	got := Mask("1234567890abcdef")
	if got != "1234****cdef" {
		t.Fatalf("遮罩结果错误: %q", got)
	}
}

func TestListOmitsSecrets(t *testing.T) {
	f := &File{Default: "a", Cores: []Core{{Name: "a", URL: "http://a", Token: "secret-token"}}}
	rows := f.List()
	if len(rows) != 1 {
		t.Fatalf("行数错误: %d", len(rows))
	}
	if rows[0]["token"] == "secret-token" {
		t.Fatal("列表不应输出明文 Token")
	}
}
