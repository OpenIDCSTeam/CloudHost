package data

import (
	"database/sql"
	"time"
)

// PublicIPRule 公网 IPv4 地址转换（1:1 NAT）的归属记录。
//
// 与 nat_rules（端口映射）分开存储：端口映射是「外网端口 → 内网端口」的一对多关系，
// 地址转换是「公网地址 → 内网地址」的一对一关系，两者在爱快上是两张不同的表，
// 唯一约束也不同，混用会让同一公网 IP 被重复分配给多台虚拟机。
type PublicIPRule struct {
	ID         int    `json:"id"`
	HsName     string `json:"hs_name"`
	VMUUID     string `json:"vm_uuid"`
	PublicAddr string `json:"public_addr"`
	LanAddr    string `json:"lan_addr"`
	MacAddr    string `json:"mac_addr"`
	Comment    string `json:"comment"`
	CreatedAt  string `json:"created_at"`
}

// publicIPRuleDDL 公网地址转换规则表。
// SQL 写在 initSchema 与 migrate 两处会导致方言转换遗漏，
// 因此统一由该常量提供。
const publicIPRuleDDL = `CREATE TABLE IF NOT EXISTS public_ip_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    hs_name     TEXT NOT NULL,
    vm_uuid     TEXT NOT NULL,
    public_addr TEXT NOT NULL,
    lan_addr    TEXT DEFAULT '',
    mac_addr    TEXT DEFAULT '',
    comment     TEXT DEFAULT '',
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (hs_name, public_addr)
)`

const publicIPRuleIndexDDL = `CREATE INDEX IF NOT EXISTS idx_public_ip_rules_name ON public_ip_rules (hs_name)`

// PublicIPOwner 查询指定主机上某公网地址的归属虚拟机。
//
// 分配前必须查询：UPSERT 会静默改写归属，导致两台虚拟机共用同一个公网 IP。
func (d *DB) PublicIPOwner(hsName, publicAddr string) (string, bool, error) {
	var vmUUID string
	err := d.db.QueryRow(
		"SELECT vm_uuid FROM public_ip_rules WHERE hs_name = ? AND public_addr = ?",
		hsName, publicAddr).Scan(&vmUUID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return vmUUID, true, nil
}

// UpsertPublicIPRule 新增或更新公网地址转换记录。
func (d *DB) UpsertPublicIPRule(hsName, vmUUID, publicAddr, lanAddr, macAddr, comment string) error {
	_, err := d.db.Exec(`INSERT INTO public_ip_rules (hs_name, vm_uuid, public_addr, lan_addr, mac_addr, comment)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hs_name, public_addr) DO UPDATE SET
			vm_uuid   = excluded.vm_uuid,
			lan_addr  = excluded.lan_addr,
			mac_addr  = excluded.mac_addr,
			comment   = excluded.comment`,
		hsName, vmUUID, publicAddr, lanAddr, macAddr, comment)
	return err
}

// ListPublicIPRules 返回主机下全部公网地址转换记录。
func (d *DB) ListPublicIPRules(hsName string) ([]PublicIPRule, error) {
	return d.queryPublicIPRules(
		`SELECT id, hs_name, vm_uuid, public_addr, lan_addr, mac_addr, comment, created_at
		 FROM public_ip_rules WHERE hs_name = ? ORDER BY public_addr`, hsName)
}

// ListVMPublicIPRules 返回指定虚拟机的公网地址转换记录。
func (d *DB) ListVMPublicIPRules(hsName, vmUUID string) ([]PublicIPRule, error) {
	return d.queryPublicIPRules(
		`SELECT id, hs_name, vm_uuid, public_addr, lan_addr, mac_addr, comment, created_at
		 FROM public_ip_rules WHERE hs_name = ? AND vm_uuid = ? ORDER BY public_addr`, hsName, vmUUID)
}

func (d *DB) queryPublicIPRules(query string, args ...interface{}) ([]PublicIPRule, error) {
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PublicIPRule, 0)
	for rows.Next() {
		var r PublicIPRule
		var createdAt sql.NullString
		if err := rows.Scan(&r.ID, &r.HsName, &r.VMUUID, &r.PublicAddr,
			&r.LanAddr, &r.MacAddr, &r.Comment, &createdAt); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			r.CreatedAt = normalizePublicIPTime(createdAt.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// normalizePublicIPTime 归一化不同数据库驱动返回的时间字符串。
//
// SQLite 的 CURRENT_TIMESTAMP 是 "2006-01-02 15:04:05"，
// MySQL/PG 可能带回毫秒与时区；接口统一输出同一格式，避免前端解析差异。
func normalizePublicIPTime(raw string) string {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02 15:04:05")
		}
	}
	return raw
}

// DeletePublicIPRule 删除指定公网地址的转换记录。
func (d *DB) DeletePublicIPRule(hsName, publicAddr string) error {
	_, err := d.db.Exec("DELETE FROM public_ip_rules WHERE hs_name = ? AND public_addr = ?", hsName, publicAddr)
	return err
}

// DeleteVMPublicIPRules 删除虚拟机全部公网地址转换记录（删除虚拟机时调用）。
func (d *DB) DeleteVMPublicIPRules(hsName, vmUUID string) error {
	_, err := d.db.Exec("DELETE FROM public_ip_rules WHERE hs_name = ? AND vm_uuid = ?", hsName, vmUUID)
	return err
}
