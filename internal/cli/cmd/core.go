package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/service"
)

var coreInstallOpt service.Options

func newCoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "core",
		Short: "管理主控（OpenIDCS CloudHost）",
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "安装并启动主控",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm("即将安装/更新主控服务，继续？") {
				return nil
			}
			return service.Install(coreInstallOpt)
		},
	}
	install.Flags().StringVar(&coreInstallOpt.Version, "version", "", "指定版本，如 v1.0.0")
	install.Flags().StringVarP(&coreInstallOpt.Port, "port", "p", "", "HTTP 端口（默认 1881）")
	install.Flags().StringVar(&coreInstallOpt.Dir, "dir", "", "安装目录（默认 /opt/openidcs）")
	install.Flags().StringVar(&coreInstallOpt.DataDir, "data-dir", "", "数据目录（默认 /var/lib/openidcs）")
	install.Flags().StringVarP(&coreInstallOpt.File, "file", "f", "", "离线安装包")
	install.Flags().BoolVar(&coreInstallOpt.Firewall, "firewall", false, "自动放行 1881/6080/7681")

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "卸载主控",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm("确认卸载主控服务？") {
				return nil
			}
			return service.Uninstall(coreInstallOpt.Purge)
		},
	}
	uninstall.Flags().BoolVar(&coreInstallOpt.Purge, "purge", false, "同时删除数据")

	update := &cobra.Command{
		Use:   "update",
		Short: "更新主控到最新版本",
		RunE: func(cmd *cobra.Command, args []string) error {
			return service.Update(coreInstallOpt.Port)
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "查看主控服务状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := service.Status()
			fmt.Println(out)
			return err
		},
	}

	for _, action := range []struct {
		use string
		fn  func() error
	}{
		{"start", service.Start},
		{"stop", service.Stop},
		{"restart", service.Restart},
		{"enable", service.Enable},
		{"disable", service.Disable},
	} {
		fn := action.fn
		cmd.AddCommand(&cobra.Command{
			Use:   action.use,
			Short: "服务 " + action.use,
			RunE: func(cmd *cobra.Command, args []string) error {
				return fn()
			},
		})
	}

	cmd.AddCommand(install, uninstall, update, status)
	return cmd
}
