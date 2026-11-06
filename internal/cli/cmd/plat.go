package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/platform"
)

func newPlatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plat",
		Aliases: []string{"platform"},
		Short:   "管理本机虚拟化平台",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "列出平台安装状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printData(platform.Detect())
		},
	}

	install := &cobra.Command{
		Use:   "install [docker|lxd|smolvm|qemu|virtualbox|hyperv|vmware|memu]",
		Short: "安装平台运行时",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			} else {
				name = pickPlatform()
				if name == "" {
					return nil
				}
			}
			if !confirm(fmt.Sprintf("即将安装 %s，继续？", name)) {
				return nil
			}
			return platform.Install(name)
		},
	}

	uninstall := &cobra.Command{
		Use:   "uninstall <name>",
		Short: "卸载平台运行时",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "平台名称")
			if err != nil {
				return err
			}
			if !confirm(fmt.Sprintf("确认卸载 %s？", name)) {
				return nil
			}
			return platform.Uninstall(name)
		},
	}

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "查看平台详情与回填参数",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "平台名称")
			if err != nil {
				return err
			}
			fields, err := platform.Credentials(name)
			if err != nil {
				return err
			}
			return printData(fields)
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "查看系统虚拟化状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printData(platform.Virtualization())
		},
	}

	cred := &cobra.Command{
		Use:   "credentials",
		Short: "查看或重置平台凭据",
	}
	credShow := &cobra.Command{
		Use:   "show <name>",
		Short: "查看回填主控的参数",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "平台名称")
			if err != nil {
				return err
			}
			fields, err := platform.Credentials(name)
			if err != nil {
				return err
			}
			return printData(fields)
		},
	}
	credReset := &cobra.Command{
		Use:   "reset <name>",
		Short: "重置平台凭据（打印操作指引）",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(args, "平台名称")
			if err != nil {
				return err
			}
			msg, err := platform.ResetCredentials(name)
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		},
	}
	cred.AddCommand(credShow, credReset)

	cmd.AddCommand(list, install, uninstall, show, status, cred)
	return cmd
}

func pickPlatform() string {
	options := platform.Supported()
	if len(options) == 0 {
		fmt.Println("当前系统没有可自动安装的平台")
		return ""
	}
	fmt.Println("可安装的平台：")
	for i, name := range options {
		fmt.Printf("  %d) %s\n", i+1, name)
	}
	fmt.Print("请选择序号：")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(options) {
		fmt.Println("已取消")
		return ""
	}
	return options[n-1]
}
