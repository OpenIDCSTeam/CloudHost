package data

import (
	"fmt"
	"regexp"
	"strings"
)

// Dialect 数据库方言。
type Dialect string

const (
	// DialectSQLite 默认方言。
	DialectSQLite Dialect = "sqlite"
	// DialectMySQL MySQL / MariaDB。
	DialectMySQL Dialect = "mysql"
	// DialectPostgres PostgreSQL。
	DialectPostgres Dialect = "postgres"
)

// ParseDialect 解析方言名。
func ParseDialect(name string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "sqlite", "sqlite3":
		return DialectSQLite, nil
	case "mysql", "mariadb":
		return DialectMySQL, nil
	case "postgres", "postgresql", "pg":
		return DialectPostgres, nil
	default:
		return "", fmt.Errorf("不支持的数据库驱动: %s", name)
	}
}

var (
	upsertRe     = regexp.MustCompile(`(?is)\s+ON\s+CONFLICT\s*\([^)]*\)\s+DO\s+UPDATE\s+SET\s+(.+)$`)
	excludedRe   = regexp.MustCompile(`(?i)excluded\.([a-z0-9_]+)`)
	textWordRe   = regexp.MustCompile(`\bTEXT\b`)
	defaultStrRe = regexp.MustCompile(`DEFAULT\s+'([^']*)'`)
)

// Rewrite 在执行前改写 SQL。
//   - postgres：把 ? 占位符改写为 $1..$n
//   - mysql：把 SQLite/PostgreSQL 的 upsert 语法改写为 ON DUPLICATE KEY UPDATE
func (d Dialect) Rewrite(query string) string {
	switch d {
	case DialectPostgres:
		return rebindPostgres(query)
	case DialectMySQL:
		return rewriteMySQL(query)
	default:
		return query
	}
}

// TranslateDDL 把 SQLite 建表语句转换为目标方言。
func (d Dialect) TranslateDDL(stmt string) string {
	switch d {
	case DialectPostgres:
		return strings.ReplaceAll(stmt, "INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY")
	case DialectMySQL:
		return translateDDLMySQL(stmt)
	default:
		return stmt
	}
}

// indexedTextColumns 在 MySQL 下必须是短 VARCHAR 的列（参与主键/唯一键/索引）。
var indexedTextColumns = map[string]bool{
	"id": true, "hs_name": true, "vm_uuid": true, "username": true, "email": true,
	"web_name": true, "access_key": true, "log_level": true, "log_module": true,
	"operator": true, "status": true, "type": true, "created_by": true, "created_at": true,
}

// largeTextColumns 在 MySQL 下使用 LONGTEXT 的列（JSON / 长文本）。
var largeTextColumns = map[string]bool{
	"data": true, "log_data": true, "log_message": true, "log_action": true, "detail": true,
	"vm_config": true, "status_data": true, "payload": true, "result": true, "error": true,
	"last_error": true, "message": true, "system_maps": true, "images_maps": true,
	"public_addr": true, "extend_data": true, "ipaddr_maps": true, "server_plan": true,
	"plan": true, "body": true, "content": true, "tags": true,
}

func translateDDLMySQL(stmt string) string {
	lines := strings.Split(stmt, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		trimmed = strings.ReplaceAll(trimmed, "INTEGER PRIMARY KEY AUTOINCREMENT", "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY")
		trimmed = strings.ReplaceAll(trimmed, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX")

		if col, ok := columnName(trimmed); ok && strings.Contains(strings.ToUpper(trimmed), "TEXT") {
			typ := "VARCHAR(512)"
			switch {
			case indexedTextColumns[col]:
				typ = "VARCHAR(191)"
			case largeTextColumns[col]:
				typ = "LONGTEXT"
			}
			trimmed = textWordRe.ReplaceAllString(trimmed, typ)
			if typ == "LONGTEXT" {
				trimmed = defaultStrRe.ReplaceAllString(trimmed, "DEFAULT ('$1')")
			}
		}
		lines[i] = trimmed
	}
	return strings.Join(lines, "\n")
}

// columnName 提取列定义行首的列名；表级约束返回 false。
func columnName(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false
	}
	first := strings.ToLower(fields[0])
	switch first {
	case "unique", "primary", "foreign", "constraint", "index", "key", "check":
		return "", false
	}
	if strings.HasPrefix(first, "create") || strings.HasPrefix(first, "--") {
		return "", false
	}
	return first, true
}

func rewriteMySQL(query string) string {
	q := strings.ReplaceAll(query, "INSERT OR REPLACE INTO", "REPLACE INTO")
	q = strings.ReplaceAll(q, "INSERT OR IGNORE INTO", "INSERT IGNORE INTO")

	if m := upsertRe.FindStringSubmatchIndex(q); m != nil {
		assignments := q[m[2]:m[3]]
		assignments = excludedRe.ReplaceAllString(assignments, "VALUES($1)")
		return q[:m[0]] + " ON DUPLICATE KEY UPDATE " + assignments
	}
	return q
}

// rebindPostgres 把 ? 依次替换为 $1..$n，跳过字符串与标识符内的问号。
func rebindPostgres(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte(ch)
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(ch)
		case ch == '?' && !inSingle && !inDouble:
			n++
			b.WriteString(fmt.Sprintf("$%d", n))
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// SplitStatements 按分号拆分 SQL，忽略字符串与注释中的分号。
func SplitStatements(script string) []string {
	var out []string
	var b strings.Builder
	inSingle, inDouble := false, false
	inLineComment := false
	for i := 0; i < len(script); i++ {
		ch := script[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				b.WriteByte(ch)
			}
			continue
		}
		if !inSingle && !inDouble && ch == '-' && i+1 < len(script) && script[i+1] == '-' {
			inLineComment = true
			i++
			continue
		}
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte(ch)
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(ch)
		case ch == ';' && !inSingle && !inDouble:
			if stmt := strings.TrimSpace(b.String()); stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		default:
			b.WriteByte(ch)
		}
	}
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}
