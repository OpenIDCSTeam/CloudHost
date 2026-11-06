package cmd

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/appconfig"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/config"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/output"
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

	cmd.AddCommand(cores, port, token, admin, newDBConfCmd())
	return cmd
}

func newDBConfCmd() *cobra.Command {
	db := &cobra.Command{Use: "db", Short: "数据库配置（data.yaml）"}

	show := &cobra.Command{
		Use:   "show",
		Short: "查看当前数据库配置（密码遮罩）",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := readDataConfig()
			if err != nil {
				return err
			}
			d := cfg.Database
			row := map[string]interface{}{
				"driver":   appconfig.NormalizeDriver(d.Driver),
				"dsn":      maskDSN(d.DSN),
				"host":     dbHost(d),
				"port":     dbPort(d),
				"user":     dbUser(d),
				"database": dbName(d),
			}
			if output.Parse(flagOutput) == output.FormatTable {
				output.PrintKV(row)
				return nil
			}
			return printData(row)
		},
	}

	var driver, dsn, host, user, password, database, sslmode string
	var port int
	set := &cobra.Command{
		Use:   "set",
		Short: "修改数据库配置（重启后生效）",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := readDataConfig()
			if err != nil {
				return err
			}
			if driver != "" {
				cfg.Database.Driver = appconfig.NormalizeDriver(driver)
			}
			if dsn != "" {
				cfg.Database.DSN = dsn
			}
			switch appconfig.NormalizeDriver(cfg.Database.Driver) {
			case "mysql":
				if host != "" {
					cfg.Database.MySQL.Host = host
				}
				if port > 0 {
					cfg.Database.MySQL.Port = port
				}
				if user != "" {
					cfg.Database.MySQL.User = user
				}
				if password != "" {
					cfg.Database.MySQL.Password = password
				}
				if database != "" {
					cfg.Database.MySQL.Database = database
				}
			case "postgres":
				if host != "" {
					cfg.Database.Postgres.Host = host
				}
				if port > 0 {
					cfg.Database.Postgres.Port = port
				}
				if user != "" {
					cfg.Database.Postgres.User = user
				}
				if password != "" {
					cfg.Database.Postgres.Password = password
				}
				if database != "" {
					cfg.Database.Postgres.Database = database
				}
				if sslmode != "" {
					cfg.Database.Postgres.SSLMode = sslmode
				}
			case "sqlite":
				if database != "" {
					cfg.Database.SQLite.File = database
				}
			}
			path := filepath.Join(service.DataDir(), "data.yaml")
			if err := appconfig.Save(path, cfg); err != nil {
				return err
			}
			fmt.Printf("数据库配置已写入 %s，重启主控后生效\n", path)
			return nil
		},
	}
	f := set.Flags()
	f.StringVar(&driver, "driver", "", "sqlite|mysql|postgres")
	f.StringVar(&dsn, "dsn", "", "完整 DSN（优先于分段配置）")
	f.StringVar(&host, "host", "", "数据库主机")
	f.IntVar(&port, "port", 0, "数据库端口")
	f.StringVar(&user, "user", "", "数据库账号")
	f.StringVar(&password, "password", "", "数据库密码")
	f.StringVar(&database, "database", "", "数据库名（SQLite 为文件名）")
	f.StringVar(&sslmode, "sslmode", "", "PostgreSQL sslmode")

	db.AddCommand(show, set)
	return db
}

func readDataConfig() (*appconfig.Config, error) {
	path := filepath.Join(service.DataDir(), "data.yaml")
	cfg, err := appconfig.Read(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败（可用 --data-dir 指定运行目录）: %w", path, err)
	}
	return cfg, nil
}

func dbHost(d appconfig.DatabaseConfig) string {
	switch appconfig.NormalizeDriver(d.Driver) {
	case "mysql":
		return d.MySQL.Host
	case "postgres":
		return d.Postgres.Host
	default:
		return "local"
	}
}

func dbPort(d appconfig.DatabaseConfig) int {
	switch appconfig.NormalizeDriver(d.Driver) {
	case "mysql":
		return d.MySQL.Port
	case "postgres":
		return d.Postgres.Port
	default:
		return 0
	}
}

func dbUser(d appconfig.DatabaseConfig) string {
	switch appconfig.NormalizeDriver(d.Driver) {
	case "mysql":
		return d.MySQL.User
	case "postgres":
		return d.Postgres.User
	default:
		return ""
	}
}

func dbName(d appconfig.DatabaseConfig) string {
	switch appconfig.NormalizeDriver(d.Driver) {
	case "mysql":
		return d.MySQL.Database
	case "postgres":
		return d.Postgres.Database
	default:
		return d.SQLite.File
	}
}

func maskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if i := strings.Index(dsn, "://"); i >= 0 {
		return dsn[:i+3] + "****@" + dsn[i+3:]
	}
	if at := strings.Index(dsn, "@"); at > 0 {
		if colon := strings.Index(dsn[:at], ":"); colon > 0 {
			return dsn[:colon+1] + "****" + dsn[at:]
		}
	}
	return dsn
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
