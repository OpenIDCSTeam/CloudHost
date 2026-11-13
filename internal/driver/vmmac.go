package driver

import "strings"

// normalizeVMac 把平台返回的 MAC 统一成 aa:bb:cc:dd:ee:ff。
//
// Hyper-V 的 MacAddress 是 12 位无分隔符（00155DFF0A06），VMware 的 vmx 里
// 带引号，这里一并处理；格式不合法时返回空串，调用方跳过即可。
func normalizeVMac(raw string) string {
	clean := strings.ToLower(strings.TrimSpace(raw))
	clean = strings.NewReplacer(":", "", "-", "", " ", "", "\"", "").Replace(clean)
	if len(clean) != 12 {
		return ""
	}
	for _, ch := range clean {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return ""
		}
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, clean[i:i+2])
	}
	return strings.Join(parts, ":")
}
