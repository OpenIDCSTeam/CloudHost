package driver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// newFakeVMRest 启动一个最小 vmrest 服务：两台虚拟机，只有 vmw-abc 匹配前缀
func newFakeVMRest(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/vms":
			_, _ = w.Write([]byte(`[{"id":"1","path":"C:\\vms\\vmw-abc.vmx"},{"id":"2","path":"C:\\vms\\other-vm.vmx"}]`))
		case strings.HasSuffix(r.URL.Path, "/power"):
			_, _ = w.Write([]byte(`{"power_state":"poweredOn"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newFakeVMRestDriver(t *testing.T, srv *httptest.Server) *WorkstationDriver {
	t.Helper()
	cfg := model.NewHSConfig()
	cfg.ServerAddr = strings.TrimPrefix(srv.URL, "http://")
	cfg.ServerPort = 0
	cfg.FilterName = "vmw-"
	return NewWorkstationDriver(cfg)
}

// TestWorkstationScanVMsRespectsFilter 扫描只纳管匹配前缀的虚拟机，
// 并移除历史遗留的错前缀记录
func TestWorkstationScanVMsRespectsFilter(t *testing.T) {
	srv := newFakeVMRest(t)
	d := newFakeVMRestDriver(t, srv)

	// 预置一条早期版本错误纳管的记录
	bad := model.NewVMConfig()
	bad.VmUUID = "other-vm"
	bad.OsName = "other-vm"
	d.SetVMSaving(map[string]*model.VMConfig{"other-vm": bad})

	msg, err := d.ScanVMs()
	if err != nil || msg == nil || !msg.Success {
		t.Fatalf("扫描失败: %v, %+v", err, msg)
	}

	saving := d.GetVMSaving()
	if _, ok := saving["other-vm"]; ok {
		t.Fatalf("前缀不匹配的记录应被移除")
	}
	if _, ok := saving["vmw-abc"]; !ok {
		t.Fatalf("匹配前缀的虚拟机应被纳管")
	}

	results, _ := msg.Results.(map[string]interface{})
	removed, _ := results["removed"].([]string)
	if len(removed) != 1 || removed[0] != "other-vm" {
		t.Fatalf("removed = %v，期望 [other-vm]", removed)
	}
}

// TestWorkstationCrontabRespectsFilter 调度器的周期同步不得把平台上的
// 其它虚拟机偷偷加入纳管列表
func TestWorkstationCrontabRespectsFilter(t *testing.T) {
	srv := newFakeVMRest(t)
	d := newFakeVMRestDriver(t, srv)

	if !d.Crontab() {
		t.Fatalf("Crontab 执行失败")
	}
	saving := d.GetVMSaving()
	if _, ok := saving["other-vm"]; ok {
		t.Fatalf("Crontab 不应纳管前缀不匹配的虚拟机")
	}
	if _, ok := saving["vmw-abc"]; !ok {
		t.Fatalf("Crontab 应纳管匹配前缀的虚拟机")
	}
}
