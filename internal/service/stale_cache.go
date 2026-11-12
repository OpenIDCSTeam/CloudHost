package service

import (
	"fmt"
	"sync"
	"time"
)

// staleValue 通用「优先返回旧值 + 后台刷新 + 并发合并」缓存。
//
// 面向「列出状态」类接口：这类数据允许略旧，但绝不能因为平台慢或卡住
// 把请求一起挂死。三条约束：
//
//  1. 有缓存立刻返回（即使已过期），刷新放到后台
//  2. 首次访问最多等 timeout，超时返回零值，调用方自行回退数据库
//  3. 同一 key 同时只有一个加载在飞，其余请求复用同一个结果
//
// 加载一旦超过 timeout 仍未返回，后续请求直接放弃等待（立即返回旧值或零值），
// 避免每次请求都白等一轮超时。
type staleValue[T any] struct {
	mu        sync.Mutex
	val       T
	has       bool
	at        time.Time
	loading   bool
	startedAt time.Time
	done      chan struct{}
	// invalidated 写操作显式失效：下一次读取必须等新数据，
	// 不能像「自然过期」那样继续返回旧值。
	invalidated bool
}

// Get 返回缓存值，必要时触发后台刷新。
//
// 第二个返回值表示是否拿到了可用数据（false 时调用方应回退到其它数据源）。
func (c *staleValue[T]) Get(ttl, timeout time.Duration, load func() (T, error)) (T, bool) {
	c.mu.Lock()

	// 命中新鲜缓存
	if c.has && !c.invalidated && time.Since(c.at) < ttl {
		v := c.val
		c.mu.Unlock()
		return v, true
	}

	// 自然过期：后台刷新，立刻返回旧值（stale-while-revalidate）。
	// 显式失效不算自然过期 —— 写操作刚改过数据，必须让调用方看到新值。
	if c.has && !c.invalidated {
		c.startLocked(load)
		v := c.val
		c.mu.Unlock()
		return v, true
	}

	// 无旧值，且已有一次加载跑了超过 timeout：不再陪等
	if c.loading && time.Since(c.startedAt) >= timeout {
		c.mu.Unlock()
		var zero T
		return zero, false
	}

	// 无旧值：等一次加载，最多 timeout
	ch := c.startLocked(load)
	c.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		var zero T
		return zero, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.has {
		var zero T
		return zero, false
	}
	return c.val, true
}

// Invalidate 让缓存显式失效，下次读取会等待重新加载。
// 写操作（创建 / 删除 / 电源 / 改配）完成后调用，避免列表短暂显示旧状态。
func (c *staleValue[T]) Invalidate() {
	c.mu.Lock()
	c.at = time.Time{}
	c.invalidated = true
	c.mu.Unlock()
}

// vmReadTimeout 单次读取驱动内存的等待上限。
// 这类读取本应是内存操作，一旦卡住说明驱动的锁被平台调用占用，
// 详情类接口应立即回退数据库，而不是陪着一起等。
const vmReadTimeout = 2 * time.Second

// callWithTimeout 在 timeout 内等待一次调用返回，超时返回错误。
//
// 结果通道缓冲 1：超时返回后 goroutine 仍能写入并退出，不会泄漏。
func callWithTimeout[T any](timeout time.Duration, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}

	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v: v, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-timer.C:
		var zero T
		return zero, fmt.Errorf("读取超时（%s）", timeout)
	}
}

// startLocked 启动一次加载（若已有在飞则复用），返回完成通知。调用方需持有 c.mu。
func (c *staleValue[T]) startLocked(load func() (T, error)) chan struct{} {
	if c.loading && c.done != nil {
		return c.done
	}

	ch := make(chan struct{})
	c.loading = true
	c.startedAt = time.Now()
	c.done = ch

	go func() {
		v, err := load()

		c.mu.Lock()
		if err == nil {
			c.val = v
			c.has = true
			c.at = time.Now()
			c.invalidated = false
		}
		c.loading = false
		done := c.done
		c.done = nil
		c.mu.Unlock()

		if done != nil {
			close(done)
		}
	}()

	return ch
}
