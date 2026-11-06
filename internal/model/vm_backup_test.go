package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 前端按 Unix 秒处理 backup_time（new Date(ts*1000)），
// 序列化成 RFC3339 字符串会显示 Invalid Date。
func TestVMBackupMarshalUnixSeconds(t *testing.T) {
	b := VMBackup{
		BackupName: "vzdump-qemu-103-2026_11_05-22_11_50.vma.zst",
		BackupTime: time.Unix(1762351910, 0),
		BackupTips: "manual",
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"backup_time":1762351910`) {
		t.Errorf("backup_time 应为 Unix 秒，实际 %s", raw)
	}
}

// 零值时间输出 0，避免前端显示 1970 或无效日期。
func TestVMBackupMarshalZeroTime(t *testing.T) {
	raw, err := json.Marshal(VMBackup{BackupName: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"backup_time":0`) {
		t.Errorf("零值时间应输出 0，实际 %s", raw)
	}
}

// 已落库的历史数据可能是秒、RFC3339 或本地时间字符串，都要能读回。
func TestVMBackupUnmarshalFormats(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{"unix秒", `{"backup_name":"a","backup_time":1762351910}`, 1762351910},
		{"RFC3339", `{"backup_name":"a","backup_time":"2026-11-05T22:11:50+08:00"}`, 1793887910},
		{"本地时间", `{"backup_name":"a","backup_time":"2026-11-05 22:11:50"}`, 0}, // 按 UTC 解析，仅校验可解析
		{"null", `{"backup_name":"a","backup_time":null}`, 0},
		{"缺失", `{"backup_name":"a"}`, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b VMBackup
			if err := json.Unmarshal([]byte(c.raw), &b); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if b.BackupName != "a" {
				t.Errorf("backup_name 解析错误: %q", b.BackupName)
			}
			if c.want != 0 && b.BackupTime.Unix() != c.want {
				t.Errorf("时间 = %d，期望 %d", b.BackupTime.Unix(), c.want)
			}
		})
	}
}
