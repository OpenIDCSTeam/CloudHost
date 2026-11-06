// Package config 管理 opidcs 的多主控配置。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Core 一个主控连接。
type Core struct {
	Name  string `yaml:"name"`
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// File 配置文件结构。
type File struct {
	Default string `yaml:"default,omitempty"`
	Cores   []Core `yaml:"cores"`
}

// Path 返回配置文件路径。
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户配置目录: %w", err)
	}
	return filepath.Join(dir, "opidcs", "config.yaml"), nil
}

// Load 读取配置，文件不存在时返回空配置。
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return &File{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	return &f, nil
}

// Save 写入配置（目录 0700，文件 0600）。
func Save(f *File) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}

// Add 新增或更新一个主控。
func Add(f *File, core Core) {
	core.Name = strings.TrimSpace(core.Name)
	core.URL = strings.TrimRight(strings.TrimSpace(core.URL), "/")
	for i := range f.Cores {
		if f.Cores[i].Name == core.Name {
			f.Cores[i] = core
			return
		}
	}
	f.Cores = append(f.Cores, core)
	if f.Default == "" {
		f.Default = core.Name
	}
}

// Remove 删除主控。
func Remove(f *File, name string) bool {
	for i := range f.Cores {
		if f.Cores[i].Name == name {
			f.Cores = append(f.Cores[:i], f.Cores[i+1:]...)
			if f.Default == name {
				f.Default = ""
				if len(f.Cores) > 0 {
					f.Default = f.Cores[0].Name
				}
			}
			return true
		}
	}
	return false
}

// Resolve 按名称/序号/默认值解析主控。
func (f *File) Resolve(name string) (Core, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = f.Default
	}
	if name == "" && len(f.Cores) == 1 {
		return f.Cores[0], nil
	}
	if name == "" {
		return Core{}, errors.New("未指定主控：请使用 --core、--url/--token，或先执行 opidcs host add --name <name> --addr <url> --token <token>")
	}
	if n, err := strconv.Atoi(name); err == nil {
		if n < 0 || n >= len(f.Cores) {
			return Core{}, fmt.Errorf("主控序号 %d 超出范围（共 %d 个）", n, len(f.Cores))
		}
		return f.Cores[n], nil
	}
	for _, c := range f.Cores {
		if c.Name == name {
			return c, nil
		}
	}
	return Core{}, fmt.Errorf("未找到主控 %q", name)
}

// Mask 遮挡 Token，仅保留首尾 4 位。
func Mask(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "****" + token[len(token)-4:]
}

// List 返回主控的展示行。
func (f *File) List() []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(f.Cores))
	for i, c := range f.Cores {
		rows = append(rows, map[string]interface{}{
			"index":   i,
			"name":    c.Name,
			"url":     c.URL,
			"token":   Mask(c.Token),
			"default": c.Name == f.Default,
		})
	}
	return rows
}
