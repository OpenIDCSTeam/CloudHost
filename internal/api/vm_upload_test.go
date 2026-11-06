package api

import "testing"

// MAC 参数是虚拟机上报的唯一标识入口，格式校验必须严格：
// 放行非法值会让后续 MAC 比对失效，误拒合法值则整条上报链路中断。
func TestLooksLikeMAC(t *testing.T) {
	valid := []string{
		"10:f6:0a:02:14:64",
		"aa:bb:cc:dd:ee:ff",
		"00:1c:0a:02:14:64",
	}
	for _, mac := range valid {
		if !looksLikeMAC(mac) {
			t.Errorf("%q 应被识别为合法 MAC", mac)
		}
	}

	invalid := []string{
		"",
		"10-f6-0a-02-14-64",   // 分隔符错误
		"10:f6:0a:02:14",       // 段数不足
		"10:f6:0a:02:14:64:",   // 多一个分隔符
		"gg:bb:cc:dd:ee:ff",    // 非十六进制
		"10:f6:0a:02:14:64 ",   // 尾部空格
		"10:f6:0a:02:14:64\n",  // 换行
	}
	for _, mac := range invalid {
		if looksLikeMAC(mac) {
			t.Errorf("%q 不应被识别为合法 MAC", mac)
		}
	}
}
