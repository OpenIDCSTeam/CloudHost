package service

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestStaleValueServeStaleImmediately 缓存过期后仍立即返回旧值，刷新在后台。
func TestStaleValueServeStaleImmediately(t *testing.T) {
	var c staleValue[int]

	if v, ok := c.Get(time.Millisecond, time.Second, func() (int, error) { return 1, nil }); !ok || v != 1 {
		t.Fatalf("首次加载 = %d/%v，期望 1/true", v, ok)
	}

	slow := func() (int, error) {
		time.Sleep(200 * time.Millisecond)
		return 2, nil
	}

	start := time.Now()
	v, ok := c.Get(time.Millisecond, time.Second, slow)
	elapsed := time.Since(start)

	if !ok || v != 1 {
		t.Fatalf("过期后应返回旧值，实际 %d/%v", v, ok)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("返回旧值耗时 %v，应当立即返回", elapsed)
	}
}

// TestStaleValueFirstLoadTimeout 首次加载超时返回 false，且后续请求不再陪等。
func TestStaleValueFirstLoadTimeout(t *testing.T) {
	var c staleValue[int]

	load := func() (int, error) {
		time.Sleep(time.Second)
		return 7, nil
	}

	start := time.Now()
	if _, ok := c.Get(time.Millisecond, 100*time.Millisecond, load); ok {
		t.Fatal("首次加载应超时失败")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}

	// 加载仍在飞且已超过等待上限：后续请求应立即放弃
	time.Sleep(150 * time.Millisecond)
	start = time.Now()
	if _, ok := c.Get(time.Millisecond, 100*time.Millisecond, load); ok {
		t.Fatal("不应有可用值")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("后续请求不应继续等待，耗时 %v", elapsed)
	}
}

// TestStaleValueSingleFlight 并发读取只触发一次加载。
func TestStaleValueSingleFlight(t *testing.T) {
	var c staleValue[int]
	var calls int32

	load := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(80 * time.Millisecond)
		return 5, nil
	}

	done := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		go func() {
			c.Get(time.Minute, time.Second, load)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("加载次数 = %d，期望 1", got)
	}
}

// TestStaleValueInvalidate 失效后重新加载新值。
func TestStaleValueInvalidate(t *testing.T) {
	var c staleValue[int]
	var next int32

	load := func() (int, error) { return int(atomic.LoadInt32(&next)), nil }

	if got, _ := c.Get(time.Minute, time.Second, load); got != 0 {
		t.Fatalf("首次 = %d，期望 0", got)
	}

	atomic.StoreInt32(&next, 9)
	c.Invalidate()

	if got, _ := c.Get(time.Minute, time.Second, load); got != 9 {
		t.Fatalf("失效后 = %d，期望 9", got)
	}
}
