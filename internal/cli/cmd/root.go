// Package cmd 实现 opidcs 的命令树。
package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/client"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/config"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/output"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/service"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/tui"
)

var (
	flagCore       string
	flagURL        string
	flagToken      string
	flagOutput     string
	flagDataDir    string
	flagYes        bool
	flagShowSecret bool
)

// Execute 运行根命令并返回退出码。
func Execute() int {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 1
	}
	return 0
}

// NewRootCmd 构建根命令（TUI 每次重建，避免 flag 状态残留）。
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "opidcs",
		Short: "OpenIDCS 命令行管理工具",
		Long: `opidcs 是 OpenIDCS 的官方命令行：管理主控、虚拟化平台、主机、实例、端口、反代、用户、任务、日志与系统设置。

不带参数运行会进入交互式菜单。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			service.SetDataDir(flagDataDir)
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(func(argv []string) error {
				r := NewRootCmd()
				r.SetArgs(argv)
				return r.Execute()
			})
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagCore, "core", "", "选择主控（名称或序号）")
	pf.StringVar(&flagURL, "url", "", "临时指定主控地址")
	pf.StringVar(&flagToken, "token", "", "临时指定 Token（- 表示从 stdin 读取）")
	pf.StringVar(&flagOutput, "output", "table", "输出格式：table|json|yaml")
	pf.StringVar(&flagDataDir, "data-dir", "", "主控运行目录（默认 OPIDCS_DATA_DIR 或 ./data）")
	pf.BoolVar(&flagYes, "yes", false, "跳过确认")
	pf.BoolVar(&flagShowSecret, "show-secret", false, "明文显示 Token")

	root.AddCommand(
		newCoreCmd(),
		newPlatCmd(),
		newHostCmd(),
		newVMCmd(),
		newPortCmd(),
		newWebxCmd(),
		newUserCmd(),
		newLogsCmd(),
		newTaskCmd(),
		newConfCmd(),
		newSelfCmd(),
	)
	root.AddCommand(newSelfAliases()...)
	return root
}

func apiClient() (*client.Client, error) {
	token := flagToken
	if token == "-" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("读取 stdin Token 失败: %w", err)
		}
		token = strings.TrimSpace(line)
	}
	if strings.TrimSpace(flagURL) != "" {
		return client.New(flagURL, token), nil
	}
	f, err := config.Load()
	if err != nil {
		return nil, err
	}
	core, err := f.Resolve(flagCore)
	if err != nil {
		return nil, err
	}
	return client.New(core.URL, core.Token), nil
}

func apiContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 45*time.Second)
}

func printData(data interface{}) error {
	return output.Print(data, output.Parse(flagOutput))
}

func confirm(prompt string) bool {
	if flagYes {
		return true
	}
	fmt.Printf("%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func requireArg(args []string, name string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("缺少参数：%s", name)
	}
	return args[0], nil
}
