package data

import (
	"strings"
	"testing"
)

func TestRebindPostgres(t *testing.T) {
	got := rebindPostgres("SELECT * FROM t WHERE a=? AND b=? AND c='?' AND d=\"?\"")
	want := "SELECT * FROM t WHERE a=$1 AND b=$2 AND c='?' AND d=\"?\""
	if got != want {
		t.Fatalf("rebind 错误:\n got=%s\nwant=%s", got, want)
	}
}

func TestRewriteMySQLUpsert(t *testing.T) {
	in := "INSERT INTO vm_saving (hs_name, vm_uuid, vm_config) VALUES (?, ?, ?)\nON CONFLICT(hs_name, vm_uuid) DO UPDATE SET vm_config=excluded.vm_config, updated_at=CURRENT_TIMESTAMP"
	got := DialectMySQL.Rewrite(in)
	if !strings.Contains(got, "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("未转换为 MySQL upsert: %s", got)
	}
	if strings.Contains(got, "excluded.") || strings.Contains(got, "ON CONFLICT") {
		t.Fatalf("残留 PostgreSQL 语法: %s", got)
	}
	if !strings.Contains(got, "vm_config=VALUES(vm_config)") {
		t.Fatalf("excluded 未替换: %s", got)
	}
}

func TestRewriteMySQLInsertOrReplace(t *testing.T) {
	got := DialectMySQL.Rewrite("INSERT OR REPLACE INTO hs_global (id, data) VALUES (?, ?)")
	if !strings.HasPrefix(got, "REPLACE INTO") {
		t.Fatalf("未转换为 REPLACE: %s", got)
	}
}

func TestTranslateDDLMySQL(t *testing.T) {
	ddl := `CREATE TABLE IF NOT EXISTS hs_config (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    hs_name     TEXT NOT NULL UNIQUE,
    server_pass TEXT NOT NULL DEFAULT '',
    extend_data TEXT DEFAULT '{}'
);`
	out := DialectMySQL.TranslateDDL(ddl)
	if !strings.Contains(out, "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY") {
		t.Errorf("自增主键未转换: %s", out)
	}
	if !strings.Contains(out, "hs_name     VARCHAR(191)") {
		t.Errorf("索引列应使用 VARCHAR(191): %s", out)
	}
	if !strings.Contains(out, "server_pass VARCHAR(512)") {
		t.Errorf("普通文本应使用 VARCHAR(512): %s", out)
	}
	if !strings.Contains(out, "LONGTEXT DEFAULT ('{}')") {
		t.Errorf("长文本应使用 LONGTEXT 与表达式默认值: %s", out)
	}
}

func TestTranslateDDLPostgres(t *testing.T) {
	out := DialectPostgres.TranslateDDL("CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)")
	if !strings.Contains(out, "BIGSERIAL PRIMARY KEY") {
		t.Fatalf("PostgreSQL 自增主键未转换: %s", out)
	}
}

func TestSplitStatements(t *testing.T) {
	script := `-- 注释; 不应截断
CREATE TABLE a (id INTEGER);
CREATE INDEX i ON a (id);
INSERT INTO a VALUES ('x;y');`
	got := SplitStatements(script)
	if len(got) != 3 {
		t.Fatalf("语句数量错误: %d %#v", len(got), got)
	}
	if !strings.Contains(got[2], "'x;y'") {
		t.Fatalf("字符串内分号被截断: %s", got[2])
	}
	for _, stmt := range got {
		if strings.Contains(stmt, "不应截断") {
			t.Fatalf("注释未剔除: %s", stmt)
		}
	}
}

func TestParseDialect(t *testing.T) {
	cases := map[string]Dialect{"": DialectSQLite, "sqlite3": DialectSQLite, "mysql": DialectMySQL, "pg": DialectPostgres, "postgresql": DialectPostgres}
	for in, want := range cases {
		got, err := ParseDialect(in)
		if err != nil || got != want {
			t.Fatalf("%q => %s, %v", in, got, err)
		}
	}
	if _, err := ParseDialect("oracle"); err == nil {
		t.Fatal("未知驱动应报错")
	}
}
