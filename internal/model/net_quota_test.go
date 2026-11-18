package model

import "testing"

func intPtr(v int) *int { return &v }

// 网卡总数与公网/内网 V4、V6 数量分别按套餐上限校验，
// 未配置上限（nil）时不限制，历史数据不能被误锁。
func TestCheckNetQuota(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *VMConfig
		wantErr bool
	}{
		{
			name: "未配置上限不限制",
			cfg: &VMConfig{NicAll: map[string]NCConfig{
				"nic0": {NicType: "pub", IP4Addr: "1.1.1.1", IP6Addr: "2408::1"},
				"nic1": {NicType: "nat", IP4Addr: "10.0.0.1"},
			}},
			wantErr: false,
		},
		{
			name: "网卡数等于上限通过",
			cfg: &VMConfig{NicNum: intPtr(2), NicAll: map[string]NCConfig{
				"nic0": {NicType: "pub", IP4Addr: "1.1.1.1"},
				"nic1": {NicType: "nat", IP4Addr: "10.0.0.1"},
			}},
			wantErr: false,
		},
		{
			name: "网卡数超限",
			cfg: &VMConfig{NicNum: intPtr(1), NicAll: map[string]NCConfig{
				"nic0": {NicType: "pub", IP4Addr: "1.1.1.1"},
				"nic1": {NicType: "nat", IP4Addr: "10.0.0.1"},
			}},
			wantErr: true,
		},
		{
			name: "公网 V4 超限",
			cfg: &VMConfig{Ip4Pub: intPtr(1), NicAll: map[string]NCConfig{
				"nic0": {NicType: "pub", IP4Addr: "1.1.1.1"},
				"nic1": {NicType: "pub", IP4Addr: "1.1.1.2"},
			}},
			wantErr: true,
		},
		{
			name: "内网 V6 上限为 0 时不允许内网 V6",
			cfg: &VMConfig{Ip6Pri: intPtr(0), NicAll: map[string]NCConfig{
				"nic0": {NicType: "nat", IP4Addr: "10.0.0.1", IP6Addr: "2408::1"},
			}},
			wantErr: true,
		},
		{
			name: "公网地址不占用内网额度",
			cfg: &VMConfig{Ip4Pri: intPtr(1), NicAll: map[string]NCConfig{
				"nic0": {NicType: "pub", IP4Addr: "1.1.1.1"},
				"nic1": {NicType: "nat", IP4Addr: "10.0.0.1"},
			}},
			wantErr: false,
		},
	}

	for _, c := range cases {
		err := c.cfg.CheckNetQuota()
		if c.wantErr && err == nil {
			t.Errorf("%s：期望超限，实际通过", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s：期望通过，实际报错 %v", c.name, err)
		}
	}
}

// 套餐配额复制到虚拟机后，两者不能再共享指针，
// 否则修改套餐会连带改掉已创建虚拟机的上限。
func TestCopyNetQuotaFrom(t *testing.T) {
	plan := &VMConfig{
		NicNum: intPtr(3),
		Ip4Pub: intPtr(2),
		Ip4Pri: intPtr(1),
		Ip6Pub: intPtr(0),
		Ip6Pri: intPtr(0),
	}
	vm := &VMConfig{}
	vm.CopyNetQuotaFrom(plan)
	if vm.NicNum == nil || *vm.NicNum != 3 {
		t.Fatalf("nic_num 未复制，实际 %v", vm.NicNum)
	}
	*plan.NicNum = 9
	if *vm.NicNum != 3 {
		t.Errorf("复制后仍共享指针：修改套餐把虚拟机上限改成了 %d", *vm.NicNum)
	}
	if vm.Ip6Pub == nil || *vm.Ip6Pub != 0 {
		t.Errorf("上限 0 应原样复制，实际 %v", vm.Ip6Pub)
	}
}
