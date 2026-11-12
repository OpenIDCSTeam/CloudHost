// Package driver 通用命令执行辅助
package driver

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// jsonUnmarshalLoose 宽松解析外部命令输出的 JSON。
//
// 某些平台（如 PowerShell 的 ConvertTo-Json）在输出单对象时字段大小写、
// 以及 [] 与 {} 的差异会与预期不符，这里统一收敛入口，失败时返回错误由调用方降级处理。
func jsonUnmarshalLoose(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}

// psUTF8Prelude 让 PowerShell 以 UTF-8 写出标准输出与错误流。
//
// 中文版 Windows 的控制台默认代码页是 936（GBK），子进程写出的中文错误
// 被 Go 按 UTF-8 解码后会变成乱码（例如 New-VM 的“你没有权限创建虚拟机”
// 会退化成不可读的方块），排障时直接丢失关键信息。
// PowerShell 5.1 在输出被重定向时按 [Console]::OutputEncoding 写字节，
// 因此把设置前置到脚本开头即可；设置失败时静默回退，不影响命令本身。
const psUTF8Prelude = "try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false; " +
	"$OutputEncoding = [Console]::OutputEncoding } catch {}; "

// psPermissionDeniedMarkers Hyper-V 权限不足时 PowerShell 错误文本的特征串。
//
// Hyper-V 的拒绝有两套措辞：
//   - UAC 未提升：You do not have the required permission to complete this task.
//     Contact the administrator of the authorization policy for the computer ...
//     （中文：你没有完成此任务所需的权限……）
//   - 驱动层拒绝访问：Access is denied
//
// 中英双语都覆盖，避免受系统语言影响漏判。
var psPermissionDeniedMarkers = []string{
	"没有权限", "权限不足", "拒绝访问", "所需的权限",
	"Access is denied", "required permission", "authorization policy",
	"Hyper-V Administrators",
}

// psHasPermissionDenied 判断命令输出是否为“权限不足”类错误
func psHasPermissionDenied(out string) bool {
	text := strings.ToLower(out)
	for _, m := range psPermissionDeniedMarkers {
		if strings.Contains(text, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// copyLocalFile 本地文件复制，用于把镜像模板复制成实例系统盘。
//
// 不走 PowerShell：模板动辄数 GB，execPS 的 2 分钟超时不够，
// 而且 Copy-Item 不返回进度，失败时难以判断卡在哪一步。
func copyLocalFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// 上一次创建失败可能留下同名的半成品盘
	_ = os.Remove(dst)

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// runWithStdin 执行命令并通过 stdin 传入数据（用于 chpasswd 等需要标准输入的场景）
func runWithStdin(args []string, stdin string, timeout time.Duration) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
