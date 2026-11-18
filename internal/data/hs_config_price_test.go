package data

import "testing"

// 售价字段在 INSERT / SELECT / Scan / UPDATE 里是按位置手写的，
// 少写或错位一个字段就会把后面所有单价串位（而且不会报错）。
// 这里给每个单价一个互不相同的值，做一次完整往返校验。
func TestHostPriceRoundTrip(t *testing.T) {
	db, err := NewDB(t.TempDir())
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	row := &HSConfigRow{
		HsName:       "h1",
		ServerName:   "宿主1",
		ServerType:   "PromoxSetup",
		ServerAddr:   "10.0.0.1",
		NCpuPrice:    1.01,
		NMemPrice:    1.02,
		NHddPrice:    1.03,
		NNetPrice:    1.04,
		NDatPrice:    1.05,
		NSpeedUPrice: 1.06,
		NSpeedDPrice: 1.07,
		NFluPrice:    1.08,
		NNatPrice:    1.09,
		NWebPrice:    1.10,
		NBakPrice:    1.11,
		NIp4Price:    1.12,
		NIp6Price:    1.13,
	}
	if err := db.CreateHost(row); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}

	check := func(stage string, want *HSConfigRow) {
		t.Helper()
		got, err := db.GetHost("h1")
		if err != nil || got == nil {
			t.Fatalf("%s：读取主机失败: %v", stage, err)
		}
		pairs := []struct {
			name      string
			got, want float64
		}{
			{"n_cpu_price", got.NCpuPrice, want.NCpuPrice},
			{"n_mem_price", got.NMemPrice, want.NMemPrice},
			{"n_hdd_price", got.NHddPrice, want.NHddPrice},
			{"n_net_price", got.NNetPrice, want.NNetPrice},
			{"n_dat_price", got.NDatPrice, want.NDatPrice},
			{"n_speed_u_price", got.NSpeedUPrice, want.NSpeedUPrice},
			{"n_speed_d_price", got.NSpeedDPrice, want.NSpeedDPrice},
			{"n_flu_price", got.NFluPrice, want.NFluPrice},
			{"n_nat_price", got.NNatPrice, want.NNatPrice},
			{"n_web_price", got.NWebPrice, want.NWebPrice},
			{"n_bak_price", got.NBakPrice, want.NBakPrice},
			{"n_ip4_price", got.NIp4Price, want.NIp4Price},
			{"n_ip6_price", got.NIp6Price, want.NIp6Price},
		}
		for _, p := range pairs {
			if p.got != p.want {
				t.Errorf("%s：%s = %v，期望 %v", stage, p.name, p.got, p.want)
			}
		}
	}

	check("创建后", row)

	row.NBakPrice = 2.5
	if err := db.UpdateHost(row); err != nil {
		t.Fatalf("更新主机失败: %v", err)
	}
	check("更新后", row)
}
