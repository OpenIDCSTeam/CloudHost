package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/config"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/service"
)

func newConfCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conf",
		Short: "系统设置（端口 / Token / 管理员）",
	}

	// 主控列表（本地配置）
	cores := &cobra.Command{
		Use:   "cores",
		Short: "查看已保存的主控",
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			return printData(f.List())
		},
	}

	port := &cobra.Command{Use: "port", Short: "主控端口"}
	portShow := &cobra.Command{
		Use:   "show",
		Short: "查看当前监听端口",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(service.Port())
			return nil
		},
	}
	portSet := &cobra.Command{
		Use:   "set <port>",
		Short: "修改主控监听端口并重启",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := requireArg(args, "端口")
			if err != nil {
				return err
			}
			if !confirm(fmt.Sprintf("确认把主控端口改为 %s 并重启服务？", p)) {
				return nil
			}
			return service.SetPort(p)
		},
	}
	port.AddCommand(portShow, portSet)

	token := &cobra.Command{Use: "token", Short: "Bearer Token"}
	tokenShow := &cobra.Command{
		Use:   "show",
		Short: "查看当前 Token（默认遮罩）",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Get(ctx, "/api/token/current", nil)
			if err != nil {
				return err
			}
			if !flagShowSecret {
				if m, ok := data.(map[string]interface{}); ok {
					if t, ok := m["token"].(string); ok {
						m["token"] = config.Mask(t)
					}
				}
			}
			return printData(data)
		},
	}
	tokenReset := &cobra.Command{
		Use:   "reset",
		Short: "重置 Token（所有插件与旧配置将失效）",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm("重置后所有财务插件与已保存的 CLI 配置都会失效，继续？") {
				return nil
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/token/reset", nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	token.AddCommand(tokenShow, tokenReset)

	admin := &cobra.Command{Use: "admin", Short: "管理员账号"}
	var newPassword, adminUser string
	adminReset := &cobra.Command{
		Use:   "reset",
		Short: "重置管理员密码",
		RunE: func(cmd *cobra.Command, args []string) error {
			if newPassword == "" {
				return fmt.Errorf("缺少 --password")
			}
			if len(newPassword) < 8 {
				return fmt.Errorf("密码长度不能少于 8 位")
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			list, err := c.Get(ctx, "/api/users", url.Values{"limit": []string{"1000"}})
			if err != nil {
				return err
			}
			id, err := findUserID(list, adminUser)
			if err != nil {
				return err
			}
			if _, err := c.Put(ctx, fmt.Sprintf("/api/users/%d", id), map[string]interface{}{"password": newPassword}); err != nil {
				return err
			}
			fmt.Printf("已重置用户 %s 的密码\n", adminUser)
			return nil
		},
	}
	adminReset.Flags().StringVar(&newPassword, "password", "", "新密码")
	adminReset.Flags().StringVar(&adminUser, "user", "admin", "目标用户")
	admin.AddCommand(adminReset)

	cmd.AddCommand(cores, port, token, admin)
	return cmd
}

func findUserID(data interface{}, username string) (int, error) {
	var rows []interface{}
	switch v := data.(type) {
	case []interface{}:
		rows = v
	case map[string]interface{}:
		for _, key := range []string{"list", "items", "users"} {
			if list, ok := v[key].([]interface{}); ok {
				rows = list
				break
			}
		}
		if rows == nil {
			for _, item := range v {
				if m, ok := item.(map[string]interface{}); ok {
					rows = append(rows, m)
				}
			}
		}
	}
	for _, item := range rows {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["username"].(string)
		if !strings.EqualFold(name, username) {
			continue
		}
		switch id := m["id"].(type) {
		case float64:
			return int(id), nil
		case int:
			return id, nil
		case string:
			var n int
			fmt.Sscanf(id, "%d", &n)
			return n, nil
		}
	}
	return 0, fmt.Errorf("未找到用户 %s", username)
}
