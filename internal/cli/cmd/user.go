package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "用户管理",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "列出用户",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			q := url.Values{}
			q.Set("limit", "1000")
			data, err := c.Get(ctx, "/api/users", q)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	cmd.AddCommand(list)
	return cmd
}

func newLogsCmd() *cobra.Command {
	var level, keyword, host string
	var limit int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "系统日志",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "查询日志",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			q := url.Values{}
			if level != "" {
				q.Set("level", level)
			}
			if keyword != "" {
				q.Set("keyword", keyword)
			}
			if host != "" {
				q.Set("hs_name", host)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprint(limit))
			}
			data, err := c.Get(ctx, "/api/system/logger/detail", q)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	list.Flags().StringVar(&level, "level", "", "级别：debug|info|warning|error")
	list.Flags().StringVar(&keyword, "keyword", "", "关键字")
	list.Flags().StringVar(&host, "host", "", "主机名称")
	list.Flags().IntVar(&limit, "limit", 50, "条数")
	cmd.AddCommand(list)
	return cmd
}

func newTaskCmd() *cobra.Command {
	var status string
	var limit int
	cmd := &cobra.Command{
		Use:   "task",
		Short: "异步任务",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "列出任务",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprint(limit))
			}
			data, err := c.Get(ctx, "/api/tasks", q)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	list.Flags().StringVar(&status, "status", "", "状态：pending|running|success|failed|canceled")
	list.Flags().IntVar(&limit, "limit", 20, "条数")

	cancelCmd := &cobra.Command{
		Use:   "cancel <task_id>",
		Short: "取消任务",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireArg(args, "任务 ID")
			if err != nil {
				return err
			}
			c, err := apiClient()
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			data, err := c.Post(ctx, "/api/tasks/"+url.PathEscape(id)+"/cancel", nil)
			if err != nil {
				return err
			}
			return printData(data)
		},
	}
	cmd.AddCommand(list, cancelCmd)
	return cmd
}
