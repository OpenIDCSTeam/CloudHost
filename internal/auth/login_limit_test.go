package auth

import (
	"testing"
	"time"
)

// resetLoginState 清理包级登录限流状态，避免用例间互相影响
func resetLoginState() {
	loginMu.Lock()
	loginAttempts = make(map[string]*loginAttempt)
	loginMu.Unlock()
}

// TestLoginLimitLocksAfterMaxAttempts 达到阈值后被锁定，成功后计数清零
func TestLoginLimitLocksAfterMaxAttempts(t *testing.T) {
	resetLoginState()
	defer resetLoginState()

	ip := "203.0.113.10"
	for i := 0; i < maxLoginAttempts; i++ {
		if CheckLoginLimit(ip) {
			t.Fatalf("第 %d 次失败后不应提前锁定", i+1)
		}
		RecordLoginFailure(ip)
	}
	if !CheckLoginLimit(ip) {
		t.Fatal("达到失败上限后应处于锁定状态")
	}

	ResetLoginAttempts(ip)
	if CheckLoginLimit(ip) {
		t.Fatal("登录成功后应解除锁定")
	}
}

// TestCleanupLoginAttemptsReleasesExpiredLock 锁定期过后清理
func TestCleanupLoginAttemptsReleasesExpiredLock(t *testing.T) {
	resetLoginState()
	defer resetLoginState()

	ip := "203.0.113.11"
	for i := 0; i < maxLoginAttempts; i++ {
		RecordLoginFailure(ip)
	}

	// 未到期：不应清理
	if removed := CleanupLoginAttempts(time.Now()); removed != 0 {
		t.Fatalf("锁定期内不应清理，实际清理 %d 条", removed)
	}

	// 到期后：锁定记录应被清理，且此后不再限流
	if removed := CleanupLoginAttempts(time.Now().Add(lockDuration + time.Second)); removed != 1 {
		t.Fatalf("过期的锁定记录应被清理，实际清理 %d 条", removed)
	}
	if CheckLoginLimit(ip) {
		t.Fatal("记录已清理，不应仍处于锁定状态")
	}
}

// TestCleanupLoginAttemptsDropsStaleEntries 未达阈值但长期未再失败的记录必须清理。
//
// 这是原实现的内存泄漏点：攻击者用大量不同 IP 各失败一次，
// map 只增不减，最终吃满内存。
func TestCleanupLoginAttemptsDropsStaleEntries(t *testing.T) {
	resetLoginState()
	defer resetLoginState()

	for i := 0; i < 1000; i++ {
		RecordLoginFailure("198.51.100." + itoa(i))
	}
	if got := LoginAttemptCount(); got != 1000 {
		t.Fatalf("记录数应为 1000，实际 %d", got)
	}

	// TTL 内保留
	if removed := CleanupLoginAttempts(time.Now()); removed != 0 {
		t.Fatalf("TTL 内不应清理，实际 %d", removed)
	}

	// 超过 TTL 后全部清理
	if removed := CleanupLoginAttempts(time.Now().Add(loginAttemptTTL + time.Second)); removed != 1000 {
		t.Fatalf("应清理 1000 条，实际 %d", removed)
	}
	if got := LoginAttemptCount(); got != 0 {
		t.Fatalf("清理后应为 0，实际 %d", got)
	}
}

// itoa 避免为本测试引入 strconv 依赖
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
