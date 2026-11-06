package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

var hostAddFlags struct {
	Name       string
	Type       string
	Addr       string
	User       string
	Pass       string
	Port       string
	ImagePath  string
	DataPath   string
	Prefix     string
	PublicAddr string
	ProxyMode  string
}

func newHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "host",
		Aliases: []string{"hosts"},
		Short:   "管理主控中的主机",
	}

	list := &cobra.Command{
		Use:   "list [name]",
		Short: "列出主机（或查看指定主机）",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			if len(args) > 0 {
				data, err := c.Get(ctx, "/api/server/detail/"+url.PathEscape(args[0]), nil)
				if err != nil {
					return err
				}
				return printData(data)
			}
			data, err := c.Get(ctx, "/api/server/detail", nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}

	add := &cobra.Command{
		Use:   "add",
		Short: "新增主机",
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostAddFlags.Name == "" || hostAddFlags.Type == "" || hostAddFlags.Addr == "" {
				return fmt.Errorf("--name、--type、--addr 为必填")
			}
			body := map[string]interface{}{
				"name":        hostAddFlags.Name,
				"type":        hostAddFlags.Type,
				"server_name": hostAddFlags.Name,
				"server_type": hostAddFlags.Type,
				"server_addr": hostAddFlags.Addr,
				"server_user": hostAddFlags.User,
				"server_pass": hostAddFlags.Pass,
			}
			if hostAddFlags.Port != "" {
				body["server_port"] = hostAddFlags.Port
			}
			if hostAddFlags.ImagePath != "" {
				body["images_path"] = hostAddFlags.ImagePath
			}
			if hostAddFlags.DataPath != "" {
				body["extern_path"] = hostAddFlags.DataPath
			}
			if hostAddFlags.Prefix != "" {
				body["filter_name"] = hostAddFlags.Prefix
			}
			if hostAddFlags.PublicAddr != "" {
				body["public_addr"] = hostAddFlags.PublicAddr
			}
			if hostAddFlags.ProxyMode != "" {
				body["proxy_mode"] = hostAddFlags.ProxyMode
			}

			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/server/create", body)
			if err != nil {
				return err
			}
			fmt.Println("主机已创建")
			return printData(data)
		},
	}
	f := add.Flags()
	f.StringVar(&hostAddFlags.Name, "name", "", "主机名称（hs_name）")
	f.StringVar(&hostAddFlags.Type, "type", "", "引擎类型，如 PromoxSetup")
	f.StringVar(&hostAddFlags.Addr, "addr", "", "地址 host[:port]")
	f.StringVar(&hostAddFlags.User, "user", "", "账号")
	f.StringVar(&hostAddFlags.Pass, "pass", "", "密码")
	f.StringVar(&hostAddFlags.Port, "port", "", "服务端口")
	f.StringVar(&hostAddFlags.ImagePath, "image-path", "", "模板存储路径")
	f.StringVar(&hostAddFlags.DataPath, "data-path", "", "数据存储路径")
	f.StringVar(&hostAddFlags.Prefix, "prefix", "", "只纳管该前缀的实例")
	f.StringVar(&hostAddFlags.PublicAddr, "public-addr", "", "公网 IP")
	f.StringVar(&hostAddFlags.ProxyMode, "proxy-mode", "", "direct|relay")

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "查看主机详情",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "主机名称")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Get(ctx, "/api/server/detail/"+url.PathEscape(name), nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}

	status := &cobra.Command{
		Use:   "status <name>",
		Short: "查看主机状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "主机名称")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Get(ctx, "/api/server/status/"+url.PathEscape(name), nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}

	toggle := func(enable bool, use, short string) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <name>",
			Short: short,
			RunE: func(cmd *cobra.Command, args []string) error {
				name, err := requireArg(args, "主机名称")
				if err != nil {
					return err
				}
				c, err := apiClient()
				if err != nil {
					return err
				}
				ctx, cancel := apiContext()
				defer cancel()
				if _, err := c.Put(ctx, "/api/server/update/"+url.PathEscape(name), map[string]interface{}{
					"enable_host": enable,
				}); err != nil {
					return err
				}
				fmt.Printf("主机 %s 已%s\n", name, map[bool]string{true: "启用", false: "禁用"}[enable])
				return nil
			},
		}
	}

	del := &cobra.Command{
		Use:     "del <name>",
		Aliases: []string{"remove", "rm"},
		Short:   "删除主机",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "主机名称")
			if err != nil {
				return err
			}
			if !confirm(fmt.Sprintf("确认删除主机 %s？", name)) {
				return nil
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			if _, err := c.Delete(ctx, "/api/server/delete/"+url.PathEscape(name)); err != nil {
				return err
			}
			fmt.Println("主机已删除")
			return nil
		},
	}

	scanvm := &cobra.Command{
		Use:   "scanvm <name>",
		Short: "扫描主机上的实例并纳管",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "主机名称")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/client/scaner/"+url.PathEscape(name), nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}

	scanbackup := &cobra.Command{
		Use:   "scanbackup <name>",
		Short: "扫描主机上的备份",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "主机名称")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/server/backup/scan/"+url.PathEscape(name), nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}

	var exportFile string
	export := &cobra.Command{
		Use:   "export <name>",
		Short: "导出主机配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "主机名称")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Get(ctx, "/api/server/config/export", nil)
			if err != nil {
				return err
			}
			raw, err := json.MarshalIndent(data, "", "  ")
			if err != nil {
				return err
			}
			if exportFile == "" {
				fmt.Println(string(raw))
				return nil
			}
			if err := os.WriteFile(exportFile, raw, 0o600); err != nil {
				return err
			}
			fmt.Printf("已导出到 %s（主机 %s）\n", exportFile, name)
			return nil
		},
	}
	export.Flags().StringVarP(&exportFile, "file", "f", "", "导出文件（默认标准输出）")

	var importFile string
	importCmd := &cobra.Command{
		Use:   "import",
		Short: "导入主机配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			if importFile == "" {
				return fmt.Errorf("缺少 --file")
			}
			raw, err := os.ReadFile(importFile)
			if err != nil {
				return err
			}
			var body interface{}
			if err := json.Unmarshal(raw, &body); err != nil {
				return fmt.Errorf("解析 JSON 失败: %w", err)
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/server/config/restore", body)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	importCmd.Flags().StringVarP(&importFile, "file", "f", "", "导入文件")

	cmd.AddCommand(list, add, show, status,
		toggle(true, "enable", "启用主机"),
		toggle(false, "disable", "禁用主机"),
		del, scanvm, scanbackup, export, importCmd)
	return cmd
}
