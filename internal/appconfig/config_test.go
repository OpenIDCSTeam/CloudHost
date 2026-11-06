package appconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 1881 || cfg.Database.Driver != "sqlite" {
		t.Fatalf("默认配置错误: %+v", cfg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("默认配置未落盘: %v", err)
	}
	if !strings.Contains(string(data), "driver: sqlite") {
		t.Fatalf("配置文件内容异常: %s", data)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("配置权限应为 0600，实际 %o", st.Mode().Perm())
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.yaml")
	cfg := Default()
	cfg.Server.Port = 9000
	cfg.Database.Driver = "mysql"
	cfg.Database.MySQL.Host = "db.internal"
	cfg.Database.MySQL.Database = "openidcs"
	cfg.Log.DB = "warn"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Port != 9000 || loaded.Database.Driver != "mysql" || loaded.Database.MySQL.Host != "db.internal" || loaded.Log.DB != "warn" {
		t.Fatalf("往返读取不一致: %+v", loaded)
	}
}

func TestDSNString(t *testing.T) {
	sqlite := DatabaseConfig{Driver: "sqlite"}
	driver, dsn, err := sqlite.DSNString("/var/lib/openidcs")
	if err != nil || driver != "sqlite" {
		t.Fatalf("sqlite dsn: %s %s %v", driver, dsn, err)
	}
	if !strings.HasSuffix(dsn, filepath.Join("var/lib/openidcs", "data.db")) && !strings.HasSuffix(dsn, "data.db") {
		t.Fatalf("sqlite 路径异常: %s", dsn)
	}

	mysql := DatabaseConfig{Driver: "mysql", MySQL: MySQLConfig{
		Host: "127.0.0.1", Port: 3306, User: "u", Password: "p", Database: "d", Params: "charset=utf8mb4",
	}}
	driver, dsn, err = mysql.DSNString(".")
	if err != nil || driver != "opidcs-mysql" {
		t.Fatalf("mysql dsn: %s %v", driver, err)
	}
	if dsn != "u:p@tcp(127.0.0.1:3306)/d?charset=utf8mb4" {
		t.Fatalf("mysql dsn 内容异常: %s", dsn)
	}

	pg := DatabaseConfig{Driver: "postgres", Postgres: PostgresConfig{
		Host: "db", Port: 5432, User: "u", Password: "p", Database: "d", SSLMode: "require",
	}}
	driver, dsn, err = pg.DSNString(".")
	if err != nil || driver != "opidcs-postgres" {
		t.Fatalf("pg dsn: %s %v", driver, err)
	}
	if !strings.Contains(dsn, "postgres://u:p@db:5432/d") || !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("pg dsn 内容异常: %s", dsn)
	}

	if _, _, err := (&DatabaseConfig{Driver: "oracle"}).DSNString("."); err == nil {
		t.Fatal("未知驱动应报错")
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("OPIDCS_PORT", "1999")
	t.Setenv("OPIDCS_DB_DRIVER", "pg")
	cfg := Default()
	cfg.ApplyEnv()
	if cfg.Server.Port != 1999 || cfg.Database.Driver != "postgres" {
		t.Fatalf("环境变量未生效: %+v", cfg)
	}
}
