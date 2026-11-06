package api

import "testing"

// ToDesk 上报并非每次都带临时密码：后一次不带时不能把旧值清空。
func TestMergeNonEmptyMapKeepsExistingPassword(t *testing.T) {
	dst := map[string]interface{}{
		"todesk": map[string]interface{}{
			"code":     "123456789",
			"password": "old-temp-pass",
		},
	}

	// 只上报设备代码，没带密码
	mergeNonEmptyMap(dst, map[string]interface{}{
		"todesk": map[string]interface{}{
			"code": "123456789",
		},
	})

	todesk, _ := dst["todesk"].(map[string]interface{})
	if todesk["password"] != "old-temp-pass" {
		t.Errorf("未上报的临时密码不应被清空，实际 %v", todesk["password"])
	}
}

func TestMergeNonEmptyMapIgnoresEmptyValues(t *testing.T) {
	dst := map[string]interface{}{
		"todesk": map[string]interface{}{
			"code":     "code-1",
			"password": "pass-1",
		},
	}

	mergeNonEmptyMap(dst, map[string]interface{}{
		"todesk": map[string]interface{}{
			"code":     "",
			"password": nil,
		},
		"empty_str":  "",
		"empty_obj":  map[string]interface{}{},
		"empty_list": []interface{}{},
		"nil_value":  nil,
	})

	todesk, _ := dst["todesk"].(map[string]interface{})
	if todesk["code"] != "code-1" || todesk["password"] != "pass-1" {
		t.Errorf("空值不应覆盖已有值，实际 %v", todesk)
	}
	for _, k := range []string{"empty_str", "empty_obj", "empty_list", "nil_value"} {
		if _, exists := dst[k]; exists {
			t.Errorf("空值字段 %s 不应写入", k)
		}
	}
}

func TestMergeNonEmptyMapUpdatesNonEmpty(t *testing.T) {
	dst := map[string]interface{}{
		"todesk": map[string]interface{}{"password": "old"},
	}
	mergeNonEmptyMap(dst, map[string]interface{}{
		"todesk": map[string]interface{}{"password": "new"},
	})
	todesk, _ := dst["todesk"].(map[string]interface{})
	if todesk["password"] != "new" {
		t.Errorf("新密码应覆盖旧密码，实际 %v", todesk["password"])
	}
}
