package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var portFlags struct {
	Host    string
	VM      string
	Public  int
	Private int
	Addr    string
	Tips    string
}

func newPortCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "端口映射（NAT）",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "列出端口映射",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			if portFlags.VM != "" {
				if portFlags.Host == "" {
					return fmt.Errorf("使用 --vm 时需要同时指定 --host")
				}
				data, err := c.Get(ctx, "/api/client/natget/"+url.PathEscape(portFlags.Host)+"/"+url.PathEscape(portFlags.VM), nil)
				if err != nil {
					return err
				}
				return printData(data)
			}
			host, err := resolveHost(ctx, c, portFlags.Host)
			if err != nil {
				return err
			}
			rows, err := listVMs(ctx, c, host)
			if err != nil {
				return err
			}
			out := []map[string]interface{}{}
			for _, vm := range rows {
				uuid, _ := vm["uuid"].(string)
				if uuid == "" {
					continue
				}
				data, err := c.Get(ctx, "/api/client/natget/"+url.PathEscape(host)+"/"+url.PathEscape(uuid), nil)
				if err != nil {
					continue
				}
				rules, ok := data.([]interface{})
				if !ok {
					continue
				}
				for i, rule := range rules {
					m, ok := rule.(map[string]interface{})
					if !ok {
						continue
					}
					m["host"] = host
					m["uuid"] = uuid
					m["index"] = i
					out = append(out, m)
				}
			}
			return printData(out)
		},
	}
	list.Flags().StringVar(&portFlags.Host, "host", "", "主机名称")
	list.Flags().StringVar(&portFlags.VM, "vm", "", "实例 UUID")

	add := &cobra.Command{
		Use:   "add",
		Short: "新增端口映射",
		RunE: func(cmd *cobra.Command, args []string) error {
			if portFlags.Host == "" || portFlags.VM == "" {
				return fmt.Errorf("--host 与 --vm 为必填")
			}
			if portFlags.Private == 0 {
				return fmt.Errorf("--private 为必填")
			}
			body := map[string]interface{}{
				"wan_port": portFlags.Public,
				"lan_port": portFlags.Private,
				"lan_addr": portFlags.Addr,
				"nat_tips": portFlags.Tips,
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/client/natadd/"+url.PathEscape(portFlags.Host)+"/"+url.PathEscape(portFlags.VM), body)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	add.Flags().StringVar(&portFlags.Host, "host", "", "主机名称")
	add.Flags().StringVar(&portFlags.VM, "vm", "", "实例 UUID")
	add.Flags().IntVar(&portFlags.Public, "public", 0, "公网端口（0 自动分配）")
	add.Flags().IntVar(&portFlags.Private, "private", 0, "内网端口")
	add.Flags().StringVar(&portFlags.Addr, "addr", "", "内网地址（默认取实例 IP）")
	add.Flags().StringVar(&portFlags.Tips, "tips", "", "备注")

	del := &cobra.Command{
		Use:   "del <index>",
		Short: "删除端口映射（索引来自 port list）",
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := requireArg(args, "规则索引")
			if err != nil {
				return err
			}
			if portFlags.Host == "" || portFlags.VM == "" {
				return fmt.Errorf("--host 与 --vm 为必填")
			}
			if !confirm(fmt.Sprintf("确认删除规则 #%s？", idx)) {
				return nil
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			path := "/api/client/natdel/" + url.PathEscape(portFlags.Host) + "/" + url.PathEscape(portFlags.VM) + "/" + url.PathEscape(idx)
			data, err := c.Delete(ctx, path)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	del.Flags().StringVar(&portFlags.Host, "host", "", "主机名称")
	del.Flags().StringVar(&portFlags.VM, "vm", "", "实例 UUID")

	cmd.AddCommand(list, add, del)
	return cmd
}

func newWebxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webx",
		Aliases: []string{"proxy"},
		Short:   "反向代理",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "列出反向代理",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Get(ctx, "/api/client/proxys/list", nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	cmd.AddCommand(list)
	return cmd
}
