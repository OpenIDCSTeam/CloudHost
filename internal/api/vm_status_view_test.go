package api

import (
	"encoding/json"
	"testing"
)

// 调度器每轮采集都会往 vm_status 写一条只含电源态的记录，与 Agent 上报混在同一张
// 历史表里。前端列表页只取 status[0]，若最新的那条是调度器记录，CPU/内存/硬盘/流量
// 会全部退化成配置值（页面显示 0）。这里锁定「首条必须是合成后的完整状态」。
func TestComposeVMStatusListMergesSchedulerIntoLatestReport(t *testing.T) {
	// 按 on_update 倒序：调度器记录在最前，Agent 上报在其后
	rawList := []string{
		`{"ac_status":"STARTED","vm_state":"运行中","on_update":2000}`,
		`{"ac_status":"STARTED","cpu_total":4,"cpu_usage":11,"mem_total":4096,"mem_usage":3000,"on_update":1900}`,
	}

	got := composeVMStatusList(rawList, vmStatusHistoryMax)
	if len(got) == 0 {
		t.Fatal("结果不应为空")
	}
	first, ok := got[0].(map[string]interface{})
	if !ok {
		t.Fatalf("首条应为 map，实际 %T", got[0])
	}
	if first["cpu_total"] != float64(4) {
		t.Errorf("首条应带上最新上报的 cpu_total，实际 %v", first["cpu_total"])
	}
	if first["mem_usage"] != float64(3000) {
		t.Errorf("首条应带上最新上报的 mem_usage，实际 %v", first["mem_usage"])
	}
	if first["ac_status"] != "STARTED" {
		t.Errorf("电源态应取最新记录，实际 %v", first["ac_status"])
	}
	if first["on_update"] != float64(2000) {
		t.Errorf("时间戳应取最新记录，实际 %v", first["on_update"])
	}
	// 首条是合成结果，原始调度器记录不再重复出现
	if len(got) != 2 {
		t.Fatalf("期望 2 条（合成首条 + 历史 1 条），实际 %d", len(got))
	}
}

// Agent 上报本身是最新记录时，不应产生重复条目。
func TestComposeVMStatusListKeepsReportAsIs(t *testing.T) {
	rawList := []string{
		`{"ac_status":"STARTED","cpu_total":8,"cpu_usage":50,"on_update":2000}`,
		`{"ac_status":"STOPPED","vm_state":"已关机","on_update":1900}`,
	}

	got := composeVMStatusList(rawList, vmStatusHistoryMax)
	if len(got) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(got))
	}
	first := got[0].(map[string]interface{})
	if first["cpu_usage"] != float64(50) || first["ac_status"] != "STARTED" {
		t.Errorf("首条应保持最新上报内容，实际 %v", first)
	}
	if second := got[1].(map[string]interface{}); second["ac_status"] != "STOPPED" {
		t.Errorf("第二条应为更早的调度器记录，实际 %v", second)
	}
}

// 只有调度器记录（Agent 从未上报）时仍要返回电源态，不能返回空数组。
func TestComposeVMStatusListSchedulerOnly(t *testing.T) {
	got := composeVMStatusList([]string{`{"ac_status":"STOPPED","vm_state":"已关机","on_update":2000}`}, vmStatusHistoryMax)
	if len(got) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(got))
	}
	if got[0].(map[string]interface{})["ac_status"] != "STOPPED" {
		t.Errorf("电源态丢失: %v", got[0])
	}
}

func TestComposeVMStatusListEmptyAndInvalid(t *testing.T) {
	if got := composeVMStatusList(nil, vmStatusHistoryMax); len(got) != 0 {
		t.Errorf("空输入应返回空数组，实际 %v", got)
	}
	if got := composeVMStatusList([]string{"", "not-json", `{}`}, vmStatusHistoryMax); len(got) != 0 {
		t.Errorf("无效记录应被丢弃，实际 %v", got)
	}
}

func TestComposeVMStatusListRespectsMax(t *testing.T) {
	rawList := []string{
		`{"ac_status":"STARTED","on_update":3000}`,
		`{"ac_status":"STARTED","on_update":2000}`,
		`{"ac_status":"STARTED","on_update":1000}`,
	}
	if got := composeVMStatusList(rawList, 2); len(got) != 2 {
		t.Errorf("应被截断为 2 条，实际 %d", len(got))
	}
}

// /api/client/status 的 history 供监控图表使用：只保留带指标的 Agent 上报，
// 并按升序返回；电源态单独取最新记录。
func TestSplitVMStatusHistory(t *testing.T) {
	rawList := []string{
		`{"ac_status":"STARTED","vm_state":"运行中","on_update":3000}`,
		`{"ac_status":"STARTED","cpu_total":4,"cpu_usage":20,"on_update":2000}`,
		`{"ac_status":"STARTED","cpu_total":4,"cpu_usage":10,"on_update":1000}`,
	}

	power, history := splitVMStatusHistory(rawList)
	if power != "STARTED" {
		t.Errorf("电源态应取最新记录，实际 %q", power)
	}
	if len(history) != 2 {
		t.Fatalf("history 应只保留 2 条上报记录，实际 %d", len(history))
	}
	first := history[0].(map[string]interface{})
	last := history[len(history)-1].(map[string]interface{})
	if first["on_update"] != float64(1000) || last["on_update"] != float64(2000) {
		t.Errorf("history 应按时间升序，实际首条 %v 末条 %v", first["on_update"], last["on_update"])
	}
}

// 电源态为 UNKNOWN 的历史记录不应压过更早的有效值。
func TestSplitVMStatusHistorySkipsUnknownPower(t *testing.T) {
	rawList := []string{
		`{"ac_status":"UNKNOWN","on_update":3000}`,
		`{"ac_status":"STARTED","cpu_total":4,"on_update":2000}`,
	}
	power, history := splitVMStatusHistory(rawList)
	if power != "STARTED" {
		t.Errorf("应跳过 UNKNOWN 取有效电源态，实际 %q", power)
	}
	if len(history) != 1 {
		t.Errorf("history 应只含带指标记录，实际 %d", len(history))
	}
	// 确认输出可被 JSON 序列化（接口直接返回给前端）
	if _, err := json.Marshal(history); err != nil {
		t.Errorf("history 序列化失败: %v", err)
	}
}
