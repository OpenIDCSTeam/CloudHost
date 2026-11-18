package data

import (
	"errors"
	"testing"
)

// 初始化页面依赖「是否存在管理员」判断系统是否已初始化：
// 首次初始化后必须变为已初始化，且不允许再次初始化（否则等同于重置口令）。
func TestSystemInitializedLifecycle(t *testing.T) {
	db, err := NewDB(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	initialized, err := db.SystemInitialized()
	if err != nil {
		t.Fatalf("查询初始化状态失败: %v", err)
	}
	if initialized {
		t.Fatal("全新数据库不应处于已初始化状态")
	}

	if err := db.CreateInitialAdmin("hashed-password"); err != nil {
		t.Fatalf("创建初始管理员失败: %v", err)
	}

	initialized, err = db.SystemInitialized()
	if err != nil {
		t.Fatalf("查询初始化状态失败: %v", err)
	}
	if !initialized {
		t.Fatal("创建管理员后应处于已初始化状态")
	}

	user, err := db.GetUserByUsername("admin")
	if err != nil || user == nil {
		t.Fatalf("初始管理员账号不存在: %v", err)
	}
	if !isAdminValue(user["is_admin"]) || !isActiveValue(user["is_active"]) {
		t.Fatalf("初始管理员应启用且为管理员: %+v", user)
	}

	// 二次初始化必须被拒绝，避免把初始化接口变成口令重置入口。
	if err := db.CreateInitialAdmin("another-password"); !errors.Is(err, ErrSystemInitialized) {
		t.Fatalf("重复初始化应返回 ErrSystemInitialized，实际: %v", err)
	}
}

func isAdminValue(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func isActiveValue(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

// 初始化前已被注册的同名普通账号（admin）应被提权并重置口令，
// 否则唯一约束会让系统永久无法完成初始化。
func TestCreateInitialAdminPromotesExistingUsername(t *testing.T) {
	db, err := NewDB(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	if _, err := db.CreateUser("admin", "old-password", "squatter@example.com", false); err != nil {
		t.Fatalf("创建同名普通用户失败: %v", err)
	}

	if err := db.CreateInitialAdmin("new-password-hash"); err != nil {
		t.Fatalf("初始化应提权同名账号，实际失败: %v", err)
	}

	user, err := db.GetUserByUsername("admin")
	if err != nil || user == nil {
		t.Fatalf("管理员账号不存在: %v", err)
	}
	if !isAdminValue(user["is_admin"]) || !isActiveValue(user["is_active"]) {
		t.Fatalf("同名账号应被提权并启用: %+v", user)
	}
	if got := user["password"]; got != "new-password-hash" {
		t.Fatalf("初始化应重置为新的口令哈希，实际: %v", got)
	}
}
