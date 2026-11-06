// Package output 负责 table / json / yaml 三种输出。
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Format 输出格式。
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
)

// Parse 解析格式，非法值回退 table。
func Parse(v string) Format {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "json":
		return FormatJSON
	case "yaml", "yml":
		return FormatYAML
	default:
		return FormatTable
	}
}

// Print 输出数据。
func Print(data interface{}, format Format) error {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(data)
	case FormatYAML:
		b, err := yaml.Marshal(data)
		if err != nil {
			return err
		}
		fmt.Print(string(b))
		return nil
	default:
		PrintTable(data)
		return nil
	}
}

// PrintKV 打印单条记录。
func PrintKV(data interface{}) {
	rows, _ := normalize(data)
	if len(rows) == 0 {
		fmt.Println("(空)")
		return
	}
	row := rows[0]
	keys := sortedKeys(row)
	width := 0
	for _, k := range keys {
		if n := displayWidth(k); n > width {
			width = n
		}
	}
	for _, k := range keys {
		fmt.Printf("%-*s  %s\n", width, k, cell(row[k], 120))
	}
}

// PrintTable 打印表格。
func PrintTable(data interface{}) {
	rows, _ := normalize(data)
	if len(rows) == 0 {
		fmt.Println("(空)")
		return
	}
	cols := pickColumns(rows)
	if len(cols) == 0 {
		PrintKV(data)
		return
	}

	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = displayWidth(c)
	}
	cells := make([][]string, len(rows))
	for r, row := range rows {
		cells[r] = make([]string, len(cols))
		for i, c := range cols {
			v := cell(row[c], 48)
			cells[r][i] = v
			if n := displayWidth(v); n > widths[i] {
				widths[i] = n
			}
		}
	}

	for i, c := range cols {
		fmt.Printf("%-*s", widths[i], c)
		if i < len(cols)-1 {
			fmt.Print("  ")
		}
	}
	fmt.Println()
	for _, row := range cells {
		for i, v := range row {
			fmt.Printf("%-*s", widths[i], v)
			if i < len(cols)-1 {
				fmt.Print("  ")
			}
		}
		fmt.Println()
	}
}

func normalize(data interface{}) ([]map[string]interface{}, []string) {
	switch data.(type) {
	case nil, []interface{}, map[string]interface{}:
	default:
		// 结构体 / 结构体切片等：先转成通用 JSON 结构再处理
		if b, err := json.Marshal(data); err == nil {
			var generic interface{}
			if json.Unmarshal(b, &generic) == nil {
				return normalize(generic)
			}
		}
	}

	switch v := data.(type) {
	case nil:
		return nil, nil
	case []map[string]interface{}:
		rows := make([]map[string]interface{}, 0, len(v))
		rows = append(rows, v...)
		return rows, nil
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				rows = append(rows, m)
			}
		}
		return rows, nil
	case map[string]interface{}:
		if list, ok := v["list"].([]interface{}); ok {
			return normalize(list)
		}
		if list, ok := v["items"].([]interface{}); ok {
			return normalize(list)
		}
		rows := make([]map[string]interface{}, 0, len(v))
		keys := sortedKeys(v)
		allMaps := true
		for _, k := range keys {
			if _, ok := v[k].(map[string]interface{}); !ok {
				allMaps = false
				break
			}
		}
		if allMaps && len(keys) > 0 {
			for _, k := range keys {
				row := v[k].(map[string]interface{})
				if _, exists := row["name"]; !exists {
					row["name"] = k
				}
				rows = append(rows, row)
			}
			return rows, keys
		}
		return []map[string]interface{}{v}, nil
	default:
		return []map[string]interface{}{{"value": data}}, nil
	}
}

var preferred = []string{
	"name", "server_name", "username", "id", "uuid",
	"status", "state", "enable_host", "type", "server_type",
	"addr", "server_addr", "ip", "ipv4", "vm_count", "vms_count",
	"level", "module", "msg", "message", "progress",
	"created_at", "updated_at", "time",
}

func pickColumns(rows []map[string]interface{}) []string {
	present := map[string]bool{}
	for _, k := range sortedKeys(rows[0]) {
		present[k] = true
	}
	cols := []string{}
	for _, k := range preferred {
		if present[k] {
			cols = append(cols, k)
			delete(present, k)
		}
	}
	if len(cols) >= 6 {
		return cols[:6]
	}
	rest := make([]string, 0, len(present))
	for k := range present {
		if k == "config" {
			continue
		}
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		if len(cols) >= 6 {
			break
		}
		cols = append(cols, k)
	}
	return cols
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cell(v interface{}, max int) string {
	var s string
	switch t := v.(type) {
	case nil:
		s = "-"
	case string:
		s = t
	case bool:
		if t {
			s = "true"
		} else {
			s = "false"
		}
	case float64:
		if t == float64(int64(t)) {
			s = fmt.Sprintf("%d", int64(t))
		} else {
			s = fmt.Sprintf("%.2f", t)
		}
	default:
		b, err := json.Marshal(t)
		if err != nil {
			s = fmt.Sprint(t)
		} else {
			s = string(b)
		}
	}
	s = strings.ReplaceAll(s, "\n", " ")
	if displayWidth(s) > max {
		out := []rune{}
		w := 0
		for _, r := range s {
			if w >= max-1 {
				break
			}
			out = append(out, r)
			w += runeWidth(r)
		}
		s = string(out) + "…"
	}
	return s
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
		return 2
	}
	return 1
}
