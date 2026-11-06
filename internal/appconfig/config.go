// Package appconfig 读取与写入 data/data.yaml。
package appconfig

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 运行配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Log      LogConfig      `yaml:"log"`
}

// ServerConfig HTTP 与内置服务端口。
type ServerConfig struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	VNCPort int    `yaml:"vnc_port"`
	SSHPort int    `yaml:"ssh_port"`
}

// DatabaseConfig 数据库配置。
type DatabaseConfig struct {
	Driver   string         `yaml:"driver"`
	DSN      string         `yaml:"dsn"`
	SQLite   SQLiteConfig   `yaml:"sqlite"`
	MySQL    MySQLConfig    `yaml:"mysql"`
	Postgres PostgresConfig `yaml:"postgres"`
}

// SQLiteConfig SQLite 文件配置。
type SQLiteConfig struct {
	File string `yaml:"file"`
}

// MySQLConfig MySQL 连接配置。
type MySQLConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	Params   string `yaml:"params"`
}

// PostgresConfig PostgreSQL 连接配置。
type PostgresConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	SSLMode  string `yaml:"sslmode"`
}

// LogConfig 日志级别。
type LogConfig struct {
	Console string `yaml:"console"`
	File    string `yaml:"file"`
	DB      string `yaml:"db"`
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 1881, VNCPort: 6080, SSHPort: 7681},
		Database: DatabaseConfig{
			Driver:   "sqlite",
			SQLite:   SQLiteConfig{File: "data.db"},
			MySQL:    MySQLConfig{Host: "127.0.0.1", Port: 3306, User: "openidcs", Database: "openidcs", Params: "charset=utf8mb4&parseTime=true&loc=Local"},
			Postgres: PostgresConfig{Host: "127.0.0.1", Port: 5432, User: "openidcs", Database: "openidcs", SSLMode: "disable"},
		},
		Log: LogConfig{Console: "info", File: "debug", DB: "info"},
	}
}

// Load 读取配置；文件不存在时写入默认配置。
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("配置路径为空")
	}
	cfg, err := Read(path)
	if err == nil {
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	cfg = Default()
	if saveErr := Save(path, cfg); saveErr != nil {
		return nil, saveErr
	}
	return cfg, nil
}

// Read 只读加载配置；文件不存在时返回 os.ErrNotExist。
func Read(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("配置路径为空")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	cfg.normalize()
	return cfg, nil
}

// Save 写入配置文件（0600）。
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	cfg.normalize()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	header := "# OpenIDCS 运行配置（修改后重启生效）\n# database.driver: sqlite | mysql | postgres\n"
	if err := os.WriteFile(path, append([]byte(header), data...), 0o600); err != nil {
		return err
	}
	return nil
}

func (c *Config) normalize() {
	if c.Server.Port <= 0 {
		c.Server.Port = 1881
	}
	if c.Server.VNCPort <= 0 {
		c.Server.VNCPort = 6080
	}
	if c.Server.SSHPort <= 0 {
		c.Server.SSHPort = 7681
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	c.Database.Driver = NormalizeDriver(c.Database.Driver)
	if c.Database.SQLite.File == "" {
		c.Database.SQLite.File = "data.db"
	}
	if c.Database.MySQL.Port == 0 {
		c.Database.MySQL.Port = 3306
	}
	if c.Database.Postgres.Port == 0 {
		c.Database.Postgres.Port = 5432
	}
	if c.Database.Postgres.SSLMode == "" {
		c.Database.Postgres.SSLMode = "disable"
	}
	if c.Log.Console == "" {
		c.Log.Console = "info"
	}
	if c.Log.File == "" {
		c.Log.File = "debug"
	}
	if c.Log.DB == "" {
		c.Log.DB = "info"
	}
}

// NormalizeDriver 归一化驱动名。
func NormalizeDriver(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "", "sqlite", "sqlite3":
		return "sqlite"
	case "mysql", "mariadb":
		return "mysql"
	case "postgres", "postgresql", "pg":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(driver))
	}
}

// ApplyEnv 用环境变量覆盖配置。
func (c *Config) ApplyEnv() {
	if v := os.Getenv("OPIDCS_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("OPIDCS_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.Port = n
		}
	}
	if v := os.Getenv("OPIDCS_DB_DRIVER"); v != "" {
		c.Database.Driver = NormalizeDriver(v)
	}
	if v := os.Getenv("OPIDCS_DB_DSN"); v != "" {
		c.Database.DSN = v
	}
	c.normalize()
}

// DSNString 生成数据库连接串。
func (c *DatabaseConfig) DSNString(dataDir string) (string, string, error) {
	driver := NormalizeDriver(c.Driver)
	if strings.TrimSpace(c.DSN) != "" {
		switch driver {
		case "sqlite":
			return "sqlite", c.DSN, nil
		case "mysql":
			return "opidcs-mysql", c.DSN, nil
		case "postgres":
			return "opidcs-postgres", c.DSN, nil
		default:
			return "", "", fmt.Errorf("不支持的数据库驱动: %s", c.Driver)
		}
	}

	switch driver {
	case "sqlite":
		file := c.SQLite.File
		if file == "" {
			file = "data.db"
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(dataDir, file)
		}
		return "sqlite", file, nil
	case "mysql":
		m := c.MySQL
		params := m.Params
		if params == "" {
			params = "charset=utf8mb4&parseTime=true&loc=Local"
		}
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s", m.User, m.Password, m.Host, m.Port, m.Database, params)
		return "opidcs-mysql", dsn, nil
	case "postgres":
		p := c.Postgres
		sslmode := p.SSLMode
		if sslmode == "" {
			sslmode = "disable"
		}
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(p.User, p.Password),
			Host:   fmt.Sprintf("%s:%d", p.Host, p.Port),
			Path:   "/" + p.Database,
		}
		q := u.Query()
		q.Set("sslmode", sslmode)
		u.RawQuery = q.Encode()
		return "opidcs-postgres", u.String(), nil
	default:
		return "", "", fmt.Errorf("不支持的数据库驱动: %s", c.Driver)
	}
}
