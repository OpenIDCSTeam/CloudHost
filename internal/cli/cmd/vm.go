package cmd

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/client"
)

var vmFlags struct {
	Host   string
	Name   string
	Image  string
	CPU    int
	Mem    int
	Disk   int
	Up     int
	Down   int
	User   string
	Pass   string
	OSName string
}

func newVMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "vm",
		Aliases: []string{"vbox"},
		Short:   "管理实例（虚拟机 / 容器）",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "列出实例",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			rows, err := listVMs(ctx, c, vmFlags.Host)
			if err != nil {
				return err
			}
			return printData(rows)
		},
	}
	list.Flags().StringVar(&vmFlags.Host, "host", "", "只列出该主机")

	show := &cobra.Command{
		Use:   "show <uuid>",
		Short: "查看实例详情",
		RunE: func(cmd *cobra.Command, args []string) error {
			uuid, err := requireArg(args, "实例 UUID")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			host, err := resolveHost(ctx, c, vmFlags.Host)
			if err != nil {
				return err
			}
			data, err := c.Get(ctx, "/api/client/detail/"+url.PathEscape(host)+"/"+url.PathEscape(uuid), nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	show.Flags().StringVar(&vmFlags.Host, "host", "", "实例所属主机")

	detail := &cobra.Command{
		Use:   "detail <uuid>",
		Short: "查看实例详情（同 show）",
		RunE:  show.RunE,
	}
	detail.Flags().StringVar(&vmFlags.Host, "host", "", "实例所属主机")

	add := &cobra.Command{
		Use:   "add",
		Short: "创建实例",
		RunE: func(cmd *cobra.Command, args []string) error {
			if vmFlags.Name == "" || vmFlags.Image == "" {
				return fmt.Errorf("--name 与 --image 为必填")
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			host, err := resolveHost(ctx, c, vmFlags.Host)
			if err != nil {
				return err
			}
			body := map[string]interface{}{
				"vm_uuid": vmFlags.Name,
				"os_name": vmFlags.Image,
				"cpu_num": vmFlags.CPU,
				"mem_num": vmFlags.Mem,
				"hdd_num": vmFlags.Disk,
			}
			if vmFlags.Up > 0 {
				body["speed_u"] = vmFlags.Up
			}
			if vmFlags.Down > 0 {
				body["speed_d"] = vmFlags.Down
			}
			if vmFlags.User != "" {
				body["os_user"] = vmFlags.User
			}
			if vmFlags.Pass != "" {
				body["os_pass"] = vmFlags.Pass
			}
			data, err := c.Post(ctx, "/api/client/create/"+url.PathEscape(host), body)
			if err != nil {
				return err
			}
			fmt.Printf("已提交创建任务（主机 %s）\n", host)
			return printData(data)
		},
	}
	af := add.Flags()
	af.StringVar(&vmFlags.Host, "host", "", "目标主机")
	af.StringVar(&vmFlags.Name, "name", "", "实例名称（vm_uuid）")
	af.StringVar(&vmFlags.Image, "image", "", "镜像/模板名（os_name）")
	af.IntVar(&vmFlags.CPU, "cpu", 1, "CPU 核数")
	af.IntVar(&vmFlags.Mem, "mem", 1024, "内存 MB")
	af.IntVar(&vmFlags.Disk, "disk", 20, "磁盘 GB")
	af.IntVar(&vmFlags.Up, "speed-up", 0, "上行带宽 Mbps")
	af.IntVar(&vmFlags.Down, "speed-down", 0, "下行带宽 Mbps")
	af.StringVar(&vmFlags.User, "os-user", "root", "初始用户名")
	af.StringVar(&vmFlags.Pass, "os-pass", "", "初始密码")

	del := &cobra.Command{
		Use:     "del <uuid>",
		Aliases: []string{"remove", "rm"},
		Short:   "删除实例",
		RunE: func(cmd *cobra.Command, args []string) error {
			uuid, err := requireArg(args, "实例 UUID")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			host, err := resolveHost(ctx, c, vmFlags.Host)
			if err != nil {
				return err
			}
			if !confirm(fmt.Sprintf("确认删除实例 %s（主机 %s）？", uuid, host)) {
				return nil
			}
			data, err := c.Delete(ctx, "/api/client/delete/"+url.PathEscape(host)+"/"+url.PathEscape(uuid))
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	del.Flags().StringVar(&vmFlags.Host, "host", "", "实例所属主机")

	power := &cobra.Command{
		Use:   "power <action> <uuid>",
		Short: "电源操作：start|stop|restart|on|off|reset|pause|resume",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("用法：opidcs vm power <action> <uuid>")
			}
			action := normalizeAction(args[0])
			if action == "" {
				return fmt.Errorf("不支持的电源操作: %s", args[0])
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			host, err := resolveHost(ctx, c, vmFlags.Host)
			if err != nil {
				return err
			}
			data, err := c.Post(ctx, "/api/client/powers/"+url.PathEscape(host)+"/"+url.PathEscape(args[1]),
				map[string]interface{}{"action": action})
			if err != nil {
				return err
			}
			fmt.Printf("实例 %s 已执行 %s\n", args[1], action)
			return printData(data)
		},
	}
	power.Flags().StringVar(&vmFlags.Host, "host", "", "实例所属主机")

	cmd.AddCommand(list, show, detail, add, del, power)
	return cmd
}

func resolveHost(ctx context.Context, c *client.Client, host string) (string, error) {
	if strings.TrimSpace(host) != "" {
		return host, nil
	}
	data, err := c.Get(ctx, "/api/server/detail", nil)
	if err != nil {
		return "", err
	}
	m, ok := data.(map[string]interface{})
	if !ok || len(m) == 0 {
		return "", fmt.Errorf("没有可用主机，请先在主控新增主机")
	}
	if len(m) == 1 {
		for name := range m {
			return name, nil
		}
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("存在多台主机，请用 --host 指定：%s", strings.Join(names, ", "))
}

func listVMs(ctx context.Context, c *client.Client, host string) ([]map[string]interface{}, error) {
	hosts := []string{}
	if host != "" {
		hosts = append(hosts, host)
	} else {
		data, err := c.Get(ctx, "/api/server/detail", nil)
		if err != nil {
			return nil, err
		}
		if m, ok := data.(map[string]interface{}); ok {
			for name := range m {
				hosts = append(hosts, name)
			}
		}
		sort.Strings(hosts)
	}

	rows := []map[string]interface{}{}
	for _, h := range hosts {
		data, err := c.Get(ctx, "/api/client/detail/"+url.PathEscape(h), nil)
		if err != nil {
			return nil, err
		}
		m, ok := data.(map[string]interface{})
		if !ok {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, uuid := range keys {
			vm, ok := m[uuid].(map[string]interface{})
			if !ok {
				continue
			}
			if _, exists := vm["uuid"]; !exists {
				vm["uuid"] = uuid
			}
			vm["host"] = h
			rows = append(rows, vm)
		}
	}
	return rows, nil
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "start", "on", "boot":
		return "start"
	case "stop", "shutdown":
		return "stop"
	case "restart", "reboot":
		return "restart"
	case "off", "poweroff":
		return "poweroff"
	case "reset":
		return "reset"
	case "pause":
		return "pause"
	case "resume":
		return "resume"
	default:
		return ""
	}
}
