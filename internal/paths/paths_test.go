package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	SetRoot(root)
	defer SetRoot(DefaultRoot)

	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{Root(), Bins(), Logs(), Temp()} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("目录未创建: %s", dir)
		}
	}
	if DB() != filepath.Join(root, "data.db") || Config() != filepath.Join(root, "data.yaml") {
		t.Fatalf("路径常量错误: %s %s", DB(), Config())
	}
}

func TestMigrateLegacy(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "DataSaving")
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("hostmanage.db", "db")
	write("hostmanage.db-wal", "wal")
	write("hostagent.log", "log")
	write("Caddyfile", "caddy")
	write(".credkey", "key")
	if err := os.MkdirAll(filepath.Join(legacy, "backups"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "backups", "a.ova"), []byte("ova"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(base, "data")
	SetRoot(root)
	defer SetRoot(DefaultRoot)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateLegacy()
	if err != nil || !migrated {
		t.Fatalf("迁移失败: %v %v", migrated, err)
	}
	for _, file := range []string{DB(), DB() + "-wal", filepath.Join(Logs(), "hostagent.log"), filepath.Join(Temp(), "Caddyfile"), CredentialKey(), filepath.Join(Temp(), "backups", "a.ova")} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("迁移后文件缺失: %s", file)
		}
	}

	// 已存在 data.db 时不再迁移
	migrated, err = MigrateLegacy()
	if err != nil || migrated {
		t.Fatalf("重复迁移应跳过: %v %v", migrated, err)
	}
}
