package output

import "testing"

func TestNormalizeHostMap(t *testing.T) {
	data := map[string]interface{}{
		"pve-lab": map[string]interface{}{"type": "PromoxSetup", "status": "active"},
		"lxd-01":  map[string]interface{}{"type": "LxContainer", "status": "inactive"},
	}
	rows, _ := normalize(data)
	if len(rows) != 2 {
		t.Fatalf("行数错误: %d", len(rows))
	}
	if rows[0]["name"] == nil || rows[1]["name"] == nil {
		t.Fatal("map 键应注入 name")
	}
}

func TestNormalizeListEnvelope(t *testing.T) {
	data := map[string]interface{}{
		"list": []interface{}{
			map[string]interface{}{"name": "a"},
			map[string]interface{}{"name": "b"},
		},
	}
	rows, _ := normalize(data)
	if len(rows) != 2 {
		t.Fatalf("list 应展开: %d", len(rows))
	}
}

func TestPickColumnsPrefersKnownKeys(t *testing.T) {
	row := map[string]interface{}{
		"name": "pve", "status": "active", "type": "PromoxSetup",
		"addr": "1.2.3.4", "vm_count": 3, "enable_host": true, "config": map[string]interface{}{},
	}
	cols := pickColumns([]map[string]interface{}{row})
	if len(cols) == 0 || cols[0] != "name" {
		t.Fatalf("首列应为 name: %v", cols)
	}
	for _, c := range cols {
		if c == "config" {
			t.Fatal("config 不应进入表格")
		}
	}
}

func TestCellTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := cell(long, 10)
	if displayWidth(got) > 11 {
		t.Fatalf("截断失败: %q", got)
	}
}

func TestParseFormat(t *testing.T) {
	if Parse("JSON") != FormatJSON || Parse("yaml") != FormatYAML || Parse("x") != FormatTable {
		t.Fatal("格式解析错误")
	}
}
