// Package task 任务执行器
package task

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TaskExecutor 任务执行器接口
type TaskExecutor interface {
	Execute(ctx context.Context, task *Task) error
}

// Executor 任务执行引擎
type Executor struct {
	manager   *Manager                   // 任务管理器
	workers   int                        // 工作线程数
	taskQueue chan *taskJob              // 任务队列
	contexts  sync.Map                   // 任务上下文 map[string]context.CancelFunc
	wg        sync.WaitGroup             // 等待所有worker完成
	stopCh    chan struct{}              // 停止信号
}

// taskJob 任务作业
type taskJob struct {
	task     *Task
	executor TaskExecutor
}

// NewExecutor 创建任务执行器
func NewExecutor(manager *Manager, workers int) *Executor {
	if workers <= 0 {
		workers = 4
	}
	return &Executor{
		manager:   manager,
		workers:   workers,
		taskQueue: make(chan *taskJob, 100),
		stopCh:    make(chan struct{}),
	}
}

// Start 启动执行器
func (e *Executor) Start() {
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}
}

// Stop 停止执行器
func (e *Executor) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

// Submit 提交任务
func (e *Executor) Submit(task *Task, executor TaskExecutor) {
	job := &taskJob{
		task:     task,
		executor: executor,
	}
	e.taskQueue <- job
}

// Cancel 取消任务
func (e *Executor) Cancel(taskID string) {
	if cancelFunc, ok := e.contexts.Load(taskID); ok {
		cancelFunc.(context.CancelFunc)()
	}
}

// worker 工作线程
func (e *Executor) worker(id int) {
	defer e.wg.Done()

	for {
		select {
		case job := <-e.taskQueue:
			e.executeJob(job)
		case <-e.stopCh:
			return
		}
	}
}

// 任务超时配置：
//   - 默认 30 分钟，覆盖装系统、删盘等分钟级操作；
//   - 备份/还原类放宽到 24 小时（大磁盘 vzdump 可能跑数小时）；
//   - 运维可通过 OPENIDCS_TASK_TIMEOUT_MINUTES 统一调整默认值。
const (
	defaultTaskTimeoutMinutes = 30
	backupTaskTimeout         = 24 * time.Hour
)

// timeoutForTask 返回指定任务类型的执行超时
func timeoutForTask(taskType string) time.Duration {
	switch strings.ToLower(taskType) {
	case "backup", "restore", "backup_restore", "vm_backup", "vm_restore":
		return backupTaskTimeout
	}
	if v := strings.TrimSpace(os.Getenv("OPENIDCS_TASK_TIMEOUT_MINUTES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return defaultTaskTimeoutMinutes * time.Minute
}

// executeJob 执行任务
func (e *Executor) executeJob(job *taskJob) {
	task := job.task
	executor := job.executor

	// 创建可取消的上下文（超时按任务类型区分，可用环境变量覆盖默认值）
	ctx, cancel := context.WithTimeout(context.Background(), timeoutForTask(string(task.Type)))
	defer cancel()

	// 保存取消函数
	e.contexts.Store(task.ID, cancel)
	defer e.contexts.Delete(task.ID)

	// 标记为运行中
	if err := e.manager.MarkTaskRunning(task.ID); err != nil {
		e.manager.CompleteTask(task.ID, nil, fmt.Errorf("标记任务运行失败: %w", err))
		return
	}

	// 执行任务
	err := executor.Execute(ctx, task)

	// 完成任务：结果由 manager 在锁内读取，
	// 避免执行器与状态写入并发读写同一 *Task
	if err != nil {
		_ = e.manager.CompleteTask(task.ID, nil, err)
	} else {
		_ = e.manager.CompleteTask(task.ID, nil, nil)
	}
}

// GetQueueSize 获取队列大小
func (e *Executor) GetQueueSize() int {
	return len(e.taskQueue)
}
