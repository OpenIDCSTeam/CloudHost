package api

import (
	"sync"
	"time"
)

// screenshotCacheTTL 截图缓存有效期。
//
// 前端控制台/列表会以秒级频率轮询截图，而每次截图都要走平台的
// 远程调用（PVE 的 vncproxy、ESXi 的 CreateScreenshot_Task 等），
// 既慢又可能把平台打满。Python 版按 30 秒缓存，这里保持一致。
const screenshotCacheTTL = 30 * time.Second

// screenshotCacheTTL 过期后条目会在写入新值时被顺带清理，
// 因此缓存大小与「活跃虚拟机数」同阶，不会无限增长。
type screenshotCacheEntry struct {
	value   string
	expires time.Time
}

var screenshotCache = struct {
	mu   sync.RWMutex
	data map[string]screenshotCacheEntry
}{
	data: make(map[string]screenshotCacheEntry),
}

// getCachedScreenshot 命中未过期缓存时返回值与 true
func getCachedScreenshot(key string) (string, bool) {
	screenshotCache.mu.RLock()
	entry, ok := screenshotCache.data[key]
	screenshotCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expires) {
		return "", false
	}
	return entry.value, true
}

// setCachedScreenshot 写入缓存并清理已过期条目
func setCachedScreenshot(key, value string) {
	now := time.Now()
	screenshotCache.mu.Lock()
	defer screenshotCache.mu.Unlock()

	for k, entry := range screenshotCache.data {
		if now.After(entry.expires) {
			delete(screenshotCache.data, k)
		}
	}
	screenshotCache.data[key] = screenshotCacheEntry{
		value:   value,
		expires: now.Add(screenshotCacheTTL),
	}
}

// invalidateScreenshotCache 失效指定虚拟机的缓存（电源/配置变更后调用）
func invalidateScreenshotCache(hsName, vmUUID string) {
	screenshotCache.mu.Lock()
	delete(screenshotCache.data, hsName+"/"+vmUUID)
	screenshotCache.mu.Unlock()
}
