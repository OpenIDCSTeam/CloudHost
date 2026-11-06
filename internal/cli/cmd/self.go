package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/config"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/version"
)

const releaseAPI = "https://api.github.com/repos/OpenIDCSTeam/CloudHost/releases/latest"

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func newSelfCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "self",
		Short: "opidcs 自身维护",
	}
	cmd.AddCommand(newVersionCmd(), newUpdateCmd(), newUninstallCmd())
	return cmd
}

// newSelfAliases 提供顶层 version/update/uninstall 快捷命令。
func newSelfAliases() []*cobra.Command {
	return []*cobra.Command{newVersionCmd(), newUpdateCmd(), newUninstallCmd()}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "显示 opidcs 版本",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(version.String())
			return nil
		},
	}
}

func newUpdateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "更新 opidcs 到最新版本",
		RunE: func(cmd *cobra.Command, args []string) error {
			rel, err := fetchRelease()
			if err != nil {
				return err
			}
			fmt.Printf("最新版本: %s（当前 %s）\n", rel.TagName, version.Version)
			if checkOnly {
				return nil
			}
			asset := assetName()
			var url string
			for _, a := range rel.Assets {
				if a.Name == asset {
					url = a.URL
					break
				}
			}
			if url == "" {
				return fmt.Errorf("发布中未找到 %s，请手动下载", asset)
			}
			return downloadAndReplace(url)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "只检查不下载")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "卸载 opidcs 命令行（含本地配置）",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm("确认卸载 opidcs 命令行并删除本地配置？") {
				return nil
			}
			if path, err := config.Path(); err == nil {
				_ = os.Remove(path)
				fmt.Println("已删除配置:", path)
			}
			exe, err := os.Executable()
			if err != nil {
				return nil
			}
			if err := os.Remove(exe); err != nil {
				fmt.Printf("请手动删除可执行文件: %s\n", exe)
				return nil
			}
			fmt.Println("已删除:", exe)
			return nil
		},
	}
}

func fetchRelease() (*releaseInfo, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, releaseAPI, nil)
	req.Header.Set("User-Agent", "opidcs/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("查询版本失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查询版本失败: HTTP %d", resp.StatusCode)
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func assetName() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return fmt.Sprintf("opidcs-%s-%s%s", runtime.GOOS, runtime.GOARCH, suffix)
}

func downloadAndReplace(url string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	tmp := exe + ".new"

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
		_ = os.Rename(exe, exe+".old")
	}
	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("替换可执行文件失败（是否正在运行？）: %w", err)
	}
	fmt.Println("已更新到", exe)
	return nil
}
