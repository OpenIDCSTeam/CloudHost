package api

import (
	"sync"
	"testing"
	"time"
)

// resetScreenshotCache 清空包级截图缓存
func resetScreenshotCache() {
	screenshotCache.mu.Lock()
	screenshotCache.data = make(map[string]screenshotCacheEntry)
	screenshotCache.mu.Unlock()
}

// TestScreenshotCacheHit 写入后应命中
func TestScreenshotCacheHit(t *testing.T) {
	resetScreenshotCache()
	defer resetScreenshotCache()

	key := "pve-1/vm-100"
	setCachedScreenshot(key, "BASE64DATA")

	got, ok := getCachedScreenshot(key)
	if !ok || got != "BASE64DATA" {
		t.Fatalf("缓存未命中: ok=%v value=%q", ok, got)
	}
}

// TestScreenshotCacheExpires 超过 30 秒必须失效，
// 否则控制台会一直显示旧画面。
func TestScreenshotCacheExpires(t *testing.T) {
	resetScreenshotCache()
	defer resetScreenshotCache()

	key := "pve-1/vm-101"
	setCachedScreenshot(key, "OLD")

	// 手动把过期时间提前，模拟 30 秒后
	screenshotCache.mu.Lock()
	entry := screenshotCache.data[key]
	entry.expires = time.Now().Add(-time.Second)
	screenshotCache.data[key] = entry
	screenshotCache.mu.Unlock()

	if _, ok := getCachedScreenshot(key); ok {
		t.Fatal("过期条目不应命中")
	}
}

// TestInvalidateScreenshotCache 电源操作后必须能主动失效
func TestInvalidateScreenshotCache(t *testing.T) {
	resetScreenshotCache()
	defer resetScreenshotCache()

	setCachedScreenshot("pve-1/vm-102", "OLD")
	invalidateScreenshotCache("pve-1", "vm-102")

	if _, ok := getCachedScreenshot("pve-1/vm-102"); ok {
		t.Fatal("失效后不应命中")
	}
}

// TestScreenshotCacheConcurrentAccess 并发读写不能 panic（map 竞态）
func TestScreenshotCacheConcurrentAccess(t *testing.T) {
	resetScreenshotCache()
	defer resetScreenshotCache()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "hs/vm"
			setCachedScreenshot(key, "DATA")
			_, _ = getCachedScreenshot(key)
			invalidateScreenshotCache("hs", "vm")
		}(i)
	}
	wg.Wait()
}
