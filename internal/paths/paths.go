// Package paths 统一管理运行目录布局。
//
// data/
//
//	├── bins/        外部二进制（caddy 等）
//	├── logs/        日志文件
//	├── temp/        临时文件、Caddyfile、备份
//	├── legacy/      旧版本迁移过来的未知文件
//	├── data.db      SQLite 数据库（使用外部数据库时不生成）
//	├── data.yaml    运行配置
//	└── .credkey     凭据加密密钥
package paths

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// DefaultRoot 默认运行目录。
const DefaultRoot = "./data"

var root = DefaultRoot

// SetRoot 设置运行目录（应在启动最早期调用）。
func SetRoot(dir string) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultRoot
	}
	root = filepath.Clean(dir)
}

// Root 返回运行目录。
func Root() string { return root }

// Bins 外部二进制目录。
func Bins() string { return filepath.Join(root, "bins") }

// Logs 日志目录。
func Logs() string { return filepath.Join(root, "logs") }

// Temp 临时文件目录（含 Caddyfile 与备份）。
func Temp() string { return filepath.Join(root, "temp") }

// Legacy 旧文件归档目录。
func Legacy() string { return filepath.Join(Temp(), "legacy") }

// DB SQLite 数据库文件。
func DB() string { return filepath.Join(root, "data.db") }

// Config 运行配置文件。
func Config() string { return filepath.Join(root, "data.yaml") }

// CredentialKey 凭据加密密钥。
func CredentialKey() string { return filepath.Join(root, ".credkey") }

// Ensure 创建目录结构。
func Ensure() error {
	for _, dir := range []string{Root(), Bins(), Logs(), Temp()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

// MigrateLegacy 把旧版 DataSaving 目录迁移到新的 data 布局。
// 已存在 data.db 时视为已迁移，直接返回，避免覆盖线上数据。
func MigrateLegacy() (bool, error) {
	rootAbs, err := filepath.Abs(Root())
	if err != nil {
		return false, err
	}

	candidates := []string{filepath.Join(filepath.Dir(rootAbs), "DataSaving")}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "DataSaving"))
	}

	var legacy string
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if candidate == rootAbs {
			continue
		}
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			legacy = candidate
			break
		}
	}
	if legacy == "" {
		return false, nil
	}
	if _, err := os.Stat(DB()); err == nil {
		return false, nil
	}

	log.Printf("[paths] 检测到旧数据目录 %s，开始迁移到 %s", legacy, Root())

	if err := os.MkdirAll(Temp(), 0o750); err != nil {
		return false, err
	}

	// 1. 数据库主文件与 WAL/SHM
	for _, pair := range [][2]string{
		{"hostmanage.db", DB()},
		{"hostmanage.db-wal", DB() + "-wal"},
		{"hostmanage.db-shm", DB() + "-shm"},
	} {
		src := filepath.Join(legacy, pair[0])
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := moveFile(src, pair[1]); err != nil {
			return false, fmt.Errorf("迁移数据库文件失败: %w", err)
		}
	}

	entries, err := os.ReadDir(legacy)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		src := filepath.Join(legacy, name)
		switch {
		case name == "hostmanage.db" || strings.HasPrefix(name, "hostmanage.db-"):
			continue
		case name == "Caddyfile":
			_ = moveFile(src, filepath.Join(Temp(), "Caddyfile"))
		case name == "backups" && entry.IsDir():
			_ = moveDir(src, filepath.Join(Temp(), "backups"))
		case name == ".credkey":
			_ = moveFile(src, CredentialKey())
		case strings.HasSuffix(name, ".log") || strings.Contains(name, ".log.") || strings.HasSuffix(name, ".zip"):
			_ = moveFile(src, filepath.Join(Logs(), name))
		default:
			_ = moveFile(src, filepath.Join(Legacy(), name))
		}
	}

	log.Printf("[paths] 旧数据迁移完成，原目录保留在 %s（可人工确认后删除）", legacy)
	return true, nil
}

func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 跨分区时退化为复制
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

func moveDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := moveDir(from, to); err != nil {
				return err
			}
			continue
		}
		if err := moveFile(from, to); err != nil {
			return err
		}
	}
	_ = os.Remove(src)
	return nil
}
