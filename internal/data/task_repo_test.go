package data

import (
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := NewDB(t.TempDir())
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func saveTestTask(t *testing.T, db *DB, id, status string, progress int) {
	t.Helper()
	now := time.Now()
	err := db.SaveTask(
		id, "任务-"+id, "update_vm", status,
		progress, "msg", "{}", "", "{}", "tester",
		"host-1", "vm-1",
		now, nil, nil,
	)
	if err != nil {
		t.Fatalf("保存任务失败: %v", err)
	}
}

// TestSaveTaskProgressMonotonic 迟到的低进度写入不能覆盖高进度。
//
// 并发上报（TaskA 50、TaskB 60 乱序到达）时，进度条不能往回跳。
func TestSaveTaskProgressMonotonic(t *testing.T) {
	db := newTestDB(t)

	saveTestTask(t, db, "task-1", string(TaskStatusRunning), 60)
	saveTestTask(t, db, "task-1", string(TaskStatusRunning), 30)

	task, err := db.GetTask("task-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Progress != 60 {
		t.Fatalf("进度不应回退，期望 60，实际 %d", task.Progress)
	}
}

// TestSaveTaskVersionIncrements 每次成功写入 version 自增（乐观锁版本号）
func TestSaveTaskVersionIncrements(t *testing.T) {
	db := newTestDB(t)

	saveTestTask(t, db, "task-2", string(TaskStatusRunning), 10)
	first, err := db.GetTask("task-2")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("首次写入 version 应为 1，实际 %d", first.Version)
	}

	saveTestTask(t, db, "task-2", string(TaskStatusRunning), 20)
	second, err := db.GetTask("task-2")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("第二次写入后 version 应为 2，实际 %d", second.Version)
	}
}

// TestSaveTaskTerminalStateProtected 已进入终态的任务不能被迟到的 running 覆盖
func TestSaveTaskTerminalStateProtected(t *testing.T) {
	db := newTestDB(t)

	saveTestTask(t, db, "task-3", string(TaskStatusCompleted), 100)
	saveTestTask(t, db, "task-3", string(TaskStatusRunning), 100)

	task, err := db.GetTask("task-3")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusCompleted) {
		t.Fatalf("终态被覆盖，期望 completed，实际 %s", task.Status)
	}
}

// TestSaveTaskPendingResetsTerminal 重试会把任务重置为 pending，必须允许
func TestSaveTaskPendingResetsTerminal(t *testing.T) {
	db := newTestDB(t)

	saveTestTask(t, db, "task-4", string(TaskStatusCompleted), 100)
	saveTestTask(t, db, "task-4", string(TaskStatusPending), 0)

	task, err := db.GetTask("task-4")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusPending) {
		t.Fatalf("重试应允许重置为 pending，实际 %s", task.Status)
	}
	if task.Progress != 0 {
		t.Fatalf("重试后进度应归零，实际 %d", task.Progress)
	}
}

// TestGetAllUsersPage 分页与总数统计
func TestGetAllUsersPage(t *testing.T) {
	db := newTestDB(t)

	for _, name := range []string{"u1", "u2", "u3"} {
		if _, err := db.CreateUser(name, "hash", name+"@example.com", false); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	total, err := db.CountUsers()
	if err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if total != 3 {
		t.Fatalf("用户总数应为 3，实际 %d", total)
	}

	page, err := db.GetAllUsersPage(2, 0)
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("第一页应有 2 条，实际 %d", len(page))
	}

	page2, err := db.GetAllUsersPage(2, 2)
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("第二页应有 1 条，实际 %d", len(page2))
	}

	all, err := db.GetAllUsers()
	if err != nil {
		t.Fatalf("全量查询失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("不分页时应返回全部 3 条，实际 %d", len(all))
	}
}
