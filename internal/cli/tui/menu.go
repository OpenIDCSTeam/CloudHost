// Package tui 提供 opidcs 的交互式菜单。
package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Run 启动菜单；run 负责执行一次命令。
func Run(run func(args []string) error) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(`
OpenIDCS 管理工具
  1) 主控服务
  2) 虚拟化平台
  3) 主机
  4) 实例
  5) 端口映射
  6) 反向代理
  7) 用户 / 任务 / 日志
  8) 系统设置
  0) 退出
请选择: `)
		choice := strings.TrimSpace(read(reader))
		var err error
		switch choice {
		case "1":
			err = coreMenu(run, reader)
		case "2":
			err = platMenu(run, reader)
		case "3":
			err = hostMenu(run, reader)
		case "4":
			err = vmMenu(run, reader)
		case "5":
			host := ask(reader, "主机名称（留空自动）")
			args := []string{"port", "list"}
			if host != "" {
				args = append(args, "--host", host)
			}
			err = run(args)
		case "6":
			err = run([]string{"webx", "list"})
		case "7":
			err = opsMenu(run, reader)
		case "8":
			err = confMenu(run, reader)
		case "0", "q", "exit":
			return nil
		default:
			fmt.Println("无效选项")
		}
		if err != nil {
			fmt.Println("执行失败:", err)
		}
		fmt.Print("\n按回车继续…")
		read(reader)
	}
}

func coreMenu(run func([]string) error, r *bufio.Reader) error {
	action := choose(r, "主控服务", []string{"status", "start", "stop", "restart", "enable", "disable", "install", "update"})
	if action == "" {
		return nil
	}
	args := []string{"core", action}
	if action == "install" {
		if port := ask(r, "端口（默认 1881）"); port != "" {
			args = append(args, "--port", port)
		}
		if dataDir := ask(r, "数据目录（默认 /var/lib/openidcs）"); dataDir != "" {
			args = append(args, "--data-dir", dataDir)
		}
	}
	return run(args)
}

func platMenu(run func([]string) error, r *bufio.Reader) error {
	action := choose(r, "虚拟化平台", []string{"list", "status", "install", "uninstall", "show", "credentials show"})
	if action == "" {
		return nil
	}
	parts := strings.Fields(action)
	args := append([]string{"plat"}, parts...)
	if action == "install" || action == "uninstall" || action == "show" || parts[0] == "credentials" {
		name := ask(r, "平台名称（docker/lxd/qemu/virtualbox/smolvm/hyperv/vmware/memu）")
		if name == "" && action == "install" {
			return run(args)
		}
		if name != "" {
			args = append(args, name)
		}
	}
	return run(args)
}

func hostMenu(run func([]string) error, r *bufio.Reader) error {
	action := choose(r, "主机", []string{"list", "add", "status", "scanvm", "scanbackup"})
	if action == "" {
		return nil
	}
	args := []string{"host", action}
	switch action {
	case "add":
		name := ask(r, "主机名称")
		typ := ask(r, "类型（如 PromoxSetup / OCInterface / LxContainer）")
		addr := ask(r, "地址 host[:port]")
		user := ask(r, "账号")
		pass := ask(r, "密码")
		if name == "" || typ == "" || addr == "" {
			fmt.Println("名称/类型/地址不能为空")
			return nil
		}
		args = append(args, "--name", name, "--type", typ, "--addr", addr)
		if user != "" {
			args = append(args, "--user", user)
		}
		if pass != "" {
			args = append(args, "--pass", pass)
		}
	case "status", "scanvm", "scanbackup":
		if name := ask(r, "主机名称"); name != "" {
			args = append(args, name)
		}
	}
	return run(args)
}

func vmMenu(run func([]string) error, r *bufio.Reader) error {
	action := choose(r, "实例", []string{"list", "show", "add", "power", "del"})
	if action == "" {
		return nil
	}
	args := []string{"vm", action}
	switch action {
	case "list":
		if host := ask(r, "主机（留空=全部）"); host != "" {
			args = append(args, "--host", host)
		}
	case "show", "del":
		host := ask(r, "主机")
		uuid := ask(r, "实例 UUID")
		if host != "" {
			args = append(args, "--host", host)
		}
		if uuid != "" {
			args = append(args, uuid)
		}
	case "add":
		host := ask(r, "主机")
		name := ask(r, "实例名称")
		image := ask(r, "镜像")
		if host != "" {
			args = append(args, "--host", host)
		}
		if name != "" {
			args = append(args, "--name", name)
		}
		if image != "" {
			args = append(args, "--image", image)
		}
	case "power":
		act := choose(r, "电源动作", []string{"start", "stop", "restart", "off", "reset", "pause", "resume"})
		host := ask(r, "主机")
		uuid := ask(r, "实例 UUID")
		if act == "" || uuid == "" {
			return nil
		}
		if host != "" {
			args = append(args, "--host", host)
		}
		args = append(args, act, uuid)
	}
	return run(args)
}

func opsMenu(run func([]string) error, r *bufio.Reader) error {
	action := choose(r, "用户 / 任务 / 日志", []string{"user list", "task list", "task cancel", "logs list"})
	if action == "" {
		return nil
	}
	args := strings.Fields(action)
	if action == "task cancel" {
		if id := ask(r, "任务 ID"); id != "" {
			args = append(args, id)
		}
	}
	return run(args)
}

func confMenu(run func([]string) error, r *bufio.Reader) error {
	action := choose(r, "系统设置", []string{"port show", "port set", "token show", "token reset", "admin reset"})
	if action == "" {
		return nil
	}
	args := append([]string{"conf"}, strings.Fields(action)...)
	switch action {
	case "port set":
		if p := ask(r, "新端口"); p != "" {
			args = append(args, p)
		}
	case "admin reset":
		args = append(args, "--yes")
		if p := ask(r, "新密码（至少 8 位）"); p != "" {
			args = append(args, "--password", p)
		}
	}
	return run(args)
}

func choose(r *bufio.Reader, title string, options []string) string {
	fmt.Printf("\n%s\n", title)
	for i, opt := range options {
		fmt.Printf("  %d) %s\n", i+1, opt)
	}
	fmt.Print("请选择（0 返回）: ")
	line := strings.TrimSpace(read(r))
	if line == "0" || line == "" {
		return ""
	}
	var n int
	if _, err := fmt.Sscanf(line, "%d", &n); err != nil || n < 1 || n > len(options) {
		fmt.Println("无效选项")
		return ""
	}
	return options[n-1]
}

func ask(r *bufio.Reader, label string) string {
	fmt.Printf("%s: ", label)
	return strings.TrimSpace(read(r))
}

func read(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return line
}
