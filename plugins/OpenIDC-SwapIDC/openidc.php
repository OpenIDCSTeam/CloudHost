<?php
/**
 * OpenIDC-SwapIDC - OpenIDC 虚拟机管理财务系统插件
 * 对接 OpenIDC HostAgent REST API，实现虚拟机的全生命周期管理
 */

// ============================================================================
// 配置选项定义
// ============================================================================
function openidc_ConfigOptions() {
    $configarray = array(
        // 1 - 主机名称（hs_name），对应 OpenIDC 中的主机标识
        "主机名称"      => array("Type" => "text", "Size" => "20", "Description" => "OpenIDC 中的主机名称(hs_name)，必填"),
        // 2 - 操作系统
        "操作系统"      => array("Type" => "text", "Size" => "20", "Description" => "如 Ubuntu22.04，留空则不指定"),
        // 3 - CPU 核数
        "CPU核数"       => array("Type" => "text", "Size" => "5",  "Description" => "核"),
        // 4 - 内存
        "内存"          => array("Type" => "text", "Size" => "7",  "Description" => "MB，如 2048"),
        // 5 - 硬盘
        "硬盘"          => array("Type" => "text", "Size" => "7",  "Description" => "MB，如 20480"),
        // 6 - 上行带宽
        "上行带宽"      => array("Type" => "text", "Size" => "5",  "Description" => "Mbps，0=不限"),
        // 7 - 下行带宽
        "下行带宽"      => array("Type" => "text", "Size" => "5",  "Description" => "Mbps，0=不限"),
        // 8 - NAT端口数
        "NAT端口数"     => array("Type" => "text", "Size" => "5",  "Description" => "默认10"),
        // 9 - Web代理数
        "Web代理数"     => array("Type" => "text", "Size" => "5",  "Description" => "默认10"),
        // 10 - 流量限制
        "流量限制"      => array("Type" => "text", "Size" => "5",  "Description" => "GB/月，0=不限"),
        // 11 - 主DNS服务器
        "主DNS"         => array("Type" => "text", "Size" => "20", "Description" => "主DNS服务器地址，如 8.8.8.8，留空则不指定"),
        // 12 - 备DNS服务器
        "备DNS"         => array("Type" => "text", "Size" => "20", "Description" => "备用DNS服务器地址，如 8.8.4.4，留空则不指定"),
        // 13 - 附加参数（JSON格式）
        "附加参数"      => array("Type" => "text", "Size" => "30", "Description" => "JSON格式附加参数，如 {\"vm_name\":\"test\"}"),
        // 14 - 最大备份数量
        "最大备份数"     => array("Type" => "text", "Size" => "5",  "Description" => "允许最大备份数量，默认1"),
        // 15 - 最大光盘数量
        "最大光盘数"     => array("Type" => "text", "Size" => "5",  "Description" => "允许最大光盘数量，默认1"),
        // 16 - 最大PCIe数量
        "最大PCIe数"    => array("Type" => "text", "Size" => "5",  "Description" => "允许最大PCIe直通数量，默认0"),
        // 17 - 最大USB数量
        "最大USB数"     => array("Type" => "text", "Size" => "5",  "Description" => "允许最大USB直通数量，默认0"),
        // 18 - 最大数据盘数量
        "最大数据盘数"   => array("Type" => "text", "Size" => "5",  "Description" => "允许最大数据盘数量，默认10"),
        // 19 - 数据盘合计容量
        "数据盘总容量"   => array("Type" => "text", "Size" => "7",  "Description" => "允许数据盘合计容量(MB)，0=不限"),
        // 说明：API地址由服务器主机名/IP+端口自动构建，Token使用服务器哈希密码字段
    );
    return $configarray;
}

// ============================================================================
// 工具函数
// ============================================================================

/**
 * 获取 API Token（使用服务器哈希密码字段）
 */
function openidc_get_token($params) {
    return trim($params["serveraccesshash"]);
}

/**
 * 构建 OpenIDC API 基础 URL（使用服务器主机名/IP + 端口）
 */
function openidc_build_base_url($params) {
    $host   = $params["serverhostname"] ? $params["serverhostname"] : $params["serverip"];
    $port   = intval($params["serverport"]);
    $secure = !empty($params["serversecure"]);
    if ($port <= 0) $port = 1880;
    $scheme = $secure ? 'https' : 'http';
    return $scheme . '://' . $host . ':' . $port;
}

/**
 * 发起 OpenIDC API 请求
 * @param string $method  HTTP方法：GET/POST/PUT/DELETE
 * @param string $url     完整URL
 * @param string $token   Bearer Token
 * @param array  $data    请求体数据（POST/PUT时使用）
 * @return array|false    解析后的响应数组，失败返回false
 */
function openidc_api_request($method, $url, $token, $data = null) {
    $ch = curl_init();
    curl_setopt($ch, CURLOPT_URL, $url);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
    curl_setopt($ch, CURLOPT_TIMEOUT, 60);
    curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
    curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, false);
    curl_setopt($ch, CURLOPT_HTTPHEADER, array(
        'Authorization: Bearer ' . $token,
        'Content-Type: application/json',
        'Accept: application/json',
    ));

    switch (strtoupper($method)) {
        case 'POST':
            curl_setopt($ch, CURLOPT_POST, 1);
            curl_setopt($ch, CURLOPT_POSTFIELDS, $data ? json_encode($data) : '{}');
            break;
        case 'PUT':
            curl_setopt($ch, CURLOPT_CUSTOMREQUEST, 'PUT');
            curl_setopt($ch, CURLOPT_POSTFIELDS, $data ? json_encode($data) : '{}');
            break;
        case 'DELETE':
            curl_setopt($ch, CURLOPT_CUSTOMREQUEST, 'DELETE');
            break;
        default: // GET
            break;
    }

    $response = curl_exec($ch);
    $errno = curl_errno($ch);
    curl_close($ch);

    if ($errno || $response === false) {
        return false;
    }
    $decoded = json_decode($response, true);
    return is_array($decoded) ? $decoded : false;
}

/**
 * 统一处理 API 响应，返回 WHMCS 格式的结果字符串
 */
function openidc_handle_response($result) {
    if ($result === false) {
        return '无法连接到 OpenIDC 服务器';
    }
    $code = isset($result['code']) ? intval($result['code']) : 0;
    if ($code === 200) {
        return '成功';
    }
    $msg = isset($result['msg']) ? $result['msg'] : '未知错误';
    return $msg;
}

/**
 * 获取 vm_uuid（即 WHMCS username）
 */
function openidc_get_vm_uuid($params) {
    return $params["username"];
}

/**
 * 获取 hs_name
 */
function openidc_get_hs_name($params) {
    return trim($params["configoption1"]);
}

/**
 * 构建虚拟机创建/更新参数
 */
function openidc_build_vm_data($params) {
    $data = array();

    // 基础参数
    $data['vm_uuid']  = openidc_get_vm_uuid($params);
    $data['vm_name']  = $params["domain"] ?: $params["username"];

    // 随机生成系统密码（12位，含大小写字母+数字+特殊字符）
    $chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*';
    $os_pass = '';
    for ($i = 0; $i < 12; $i++) {
        $os_pass .= $chars[random_int(0, strlen($chars) - 1)];
    }
    $data['os_pass'] = $os_pass;

    // 操作系统
    $os = trim($params["configoption2"]);
    if ($os) $data['os_name'] = $os;

    // 资源参数
    $cpu = intval($params["configoption3"]);
    if ($cpu > 0) $data['cpu_num'] = $cpu;

    $mem = intval($params["configoption4"]);
    if ($mem > 0) $data['mem_num'] = $mem;

    $hdd = intval($params["configoption5"]);
    if ($hdd > 0) $data['hdd_num'] = $hdd;

    $speed_u = intval($params["configoption6"]);
    $data['speed_u'] = $speed_u;

    $speed_d = intval($params["configoption7"]);
    $data['speed_d'] = $speed_d;

    $nat_num = intval($params["configoption8"]);
    if ($nat_num > 0) $data['nat_num'] = $nat_num;

    $web_num = intval($params["configoption9"]);
    $data['web_num'] = $web_num;

    $flu_num = intval($params["configoption10"]);
    $data['flu_num'] = $flu_num;

    // DNS 服务器
    $dns1 = trim($params["configoption11"]);
    if ($dns1) $data['dns1'] = $dns1;

    $dns2 = trim($params["configoption12"]);
    if ($dns2) $data['dns2'] = $dns2;

    // 附加参数（JSON格式）
    $extra = trim($params["configoption13"]);
    if ($extra) {
        $extra_data = json_decode($extra, true);
        if (is_array($extra_data)) {
            $data = array_merge($data, $extra_data);
        }
    }

    // 网卡配置：留一个空配置，由系统自动分配IP
    $data['nic_all'] = array(
        'ethernet0' => array(
            'nic_type' => 'nat',
            'ip4_addr' => '',
            'ip6_addr' => ''
        )
    );

    // 配额配置
    $bak_num = $params["configoption14"];
    if ($bak_num !== '') $data['bak_num'] = intval($bak_num) ?: 1;

    $iso_num = $params["configoption15"];
    if ($iso_num !== '') $data['iso_num'] = intval($iso_num) ?: 1;

    $pci_num = $params["configoption16"];
    if ($pci_num !== '') $data['pci_num'] = intval($pci_num);

    $usb_num = $params["configoption17"];
    if ($usb_num !== '') $data['usb_num'] = intval($usb_num);

    $dat_num = $params["configoption18"];
    if ($dat_num !== '') $data['dat_num'] = intval($dat_num) ?: 10;

    $dat_all = $params["configoption19"];
    if ($dat_all !== '') $data['dat_all'] = intval($dat_all);

    return $data;
}

// ============================================================================
// WHMCS 标准接口实现
// ============================================================================

/**
 * 开通账号（创建虚拟机）
 */
function openidc_CreateAccount($params) {
    if (empty($params["username"])) {
        return '用户名不能为空';
    }
    $hs_name  = openidc_get_hs_name($params);
    if (empty($hs_name)) {
        return '主机名称(configoption1)不能为空';
    }
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $vm_data  = openidc_build_vm_data($params);

    $url    = $base_url . '/api/client/create/' . urlencode($hs_name);
    $result = openidc_api_request('POST', $url, $token, $vm_data);
    return openidc_handle_response($result);
}

/**
 * 删除账号（删除虚拟机）
 */
function openidc_TerminateAccount($params) {
    if (empty($params["username"])) {
        return '用户名不能为空';
    }
    $hs_name  = openidc_get_hs_name($params);
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $vm_uuid  = openidc_get_vm_uuid($params);

    $url    = $base_url . '/api/client/delete/' . urlencode($hs_name) . '/' . urlencode($vm_uuid);
    $result = openidc_api_request('DELETE', $url, $token);
    return openidc_handle_response($result);
}

/**
 * 暂停账号（关闭虚拟机电源）
 */
function openidc_SuspendAccount($params) {
    if (empty($params["username"])) {
        return '用户名不能为空';
    }
    $hs_name  = openidc_get_hs_name($params);
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $vm_uuid  = openidc_get_vm_uuid($params);

    $url    = $base_url . '/api/client/powers/' . urlencode($hs_name) . '/' . urlencode($vm_uuid);
    $result = openidc_api_request('POST', $url, $token, array('action' => 'stop'));
    return openidc_handle_response($result);
}

/**
 * 恢复账号（启动虚拟机）
 */
function openidc_UnsuspendAccount($params) {
    if (empty($params["username"])) {
        return '用户名不能为空';
    }
    $hs_name  = openidc_get_hs_name($params);
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $vm_uuid  = openidc_get_vm_uuid($params);

    $url    = $base_url . '/api/client/powers/' . urlencode($hs_name) . '/' . urlencode($vm_uuid);
    $result = openidc_api_request('POST', $url, $token, array('action' => 'start'));
    return openidc_handle_response($result);
}

/**
 * 修改密码（修改虚拟机系统密码）
 */
function openidc_ChangePassword($params) {
    if (empty($params["username"])) {
        return '用户名不能为空';
    }
    $hs_name  = openidc_get_hs_name($params);
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $vm_uuid  = openidc_get_vm_uuid($params);

    $url    = $base_url . '/api/client/password/' . urlencode($hs_name) . '/' . urlencode($vm_uuid);
    $result = openidc_api_request('POST', $url, $token, array('password' => $params["password"]));
    return openidc_handle_response($result);
}

/**
 * 升降套餐（更新虚拟机配置）
 */
function openidc_ChangePackage($params) {
    if (empty($params["username"])) {
        return '用户名不能为空';
    }
    $hs_name  = openidc_get_hs_name($params);
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $vm_uuid  = openidc_get_vm_uuid($params);
    $vm_data  = openidc_build_vm_data($params);

    // 升降套餐不传 vm_uuid（路径中已有），移除 vm_uuid 字段
    unset($vm_data['vm_uuid']);

    $url    = $base_url . '/api/client/update/' . urlencode($hs_name) . '/' . urlencode($vm_uuid);
    $result = openidc_api_request('PUT', $url, $token, $vm_data);
    return openidc_handle_response($result);
}

// ============================================================================
// 客户端面板
// ============================================================================

/**
 * 客户端面板按钮（获取临时凭据后跳转到 OpenIDC 控制台）
 */
function openidc_ClientArea($params) {
    $lang     = plug_lang_get('openidc', '', '2');
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);
    $hs_name  = openidc_get_hs_name($params);
    $vm_uuid  = openidc_get_vm_uuid($params);

    // 通过 API 获取临时凭据
    $temp_url = $base_url . '/api/client/temptoken/' . urlencode($hs_name) . '/' . urlencode($vm_uuid);
    $result   = openidc_api_request('GET', $temp_url, $token);

    $login_url = $base_url;
    if ($result && isset($result['code']) && $result['code'] === 200 && isset($result['data']['temp_token'])) {
        $temp_token = $result['data']['temp_token'];
        $login_url  = $base_url . '/api/client/templogin?token=' . urlencode($temp_token);
    }

    $code = array(
        '<a href="' . htmlspecialchars($login_url) . '" target="_blank">' . ($lang['进入控制台'] ?: '进入控制台') . '</a>',
        '<a href="' . htmlspecialchars($base_url) . '" target="_blank">' . ($lang['管理地址'] ?: '管理地址') . '</a>',
    );
    return $code;
}

/**
 * 服务器状态检测
 */
function openidc_TestConnection($params) {
    $base_url = openidc_build_base_url($params);
    $token    = openidc_get_token($params);

    if (empty($token)) {
        return 'Token（服务器哈希密码）不能为空';
    }

    $url    = $base_url . '/api/server/status';
    $result = openidc_api_request('GET', $url, $token);

    if ($result === false) {
        return '无法连接到 OpenIDC 服务器：' . $base_url;
    }
    $code = isset($result['code']) ? intval($result['code']) : 0;
    if ($code === 200) {
        $info = isset($result['data']) ? $result['data'] : array();
        $desc = '连接成功';
        if (isset($info['version'])) $desc .= '，版本：' . $info['version'];
        if (isset($info['host_count'])) $desc .= '，主机数：' . $info['host_count'];
        return $desc;
    }
    $msg = isset($result['msg']) ? $result['msg'] : '未知错误';
    return '服务器返回错误：' . $msg;
}

/**
 * 后台管理员直接登录链接
 */
function openidc_AdminLink($params) {
    $base_url = openidc_build_base_url($params);
    $code = '<a href="' . htmlspecialchars($base_url) . '" target="_blank" class="btn btn-default">登录 OpenIDC 管理后台</a>';
    return $code;
}

/**
 * 客户端面板数据注入（供 clientarea.tpl 使用）
 */
function openidc_ClientAreaLIB($goods, &$server) {
    $openidc = array();
    $openidc['hs_name']  = $goods['配置选项1'] ?: '';
    $openidc['os']       = $goods['配置选项2'] ?: 'N/A';
    $openidc['cpu']      = $goods['配置选项3'] ?: 'N/A';
    $openidc['mem']      = $goods['配置选项4'] ?: 'N/A';
    $openidc['hdd']      = $goods['配置选项5'] ?: 'N/A';
    $openidc['speed_u']  = intval($goods['配置选项6']);
    $openidc['speed_d']  = intval($goods['配置选项7']);
    $openidc['nat_num']  = intval($goods['配置选项8']);
    $openidc['web_num']  = intval($goods['配置选项9']);
    $openidc['flu_num']  = intval($goods['配置选项10']);
    $openidc['dns1']     = $goods['配置选项11'] ?: '';
    $openidc['dns2']     = $goods['配置选项12'] ?: '';
    $openidc['bak_num']  = intval($goods['配置选项14']) ?: 1;
    $openidc['iso_num']  = intval($goods['配置选项15']) ?: 1;
    $openidc['pci_num']  = intval($goods['配置选项16']);
    $openidc['usb_num']  = intval($goods['配置选项17']);
    $openidc['dat_num']  = intval($goods['配置选项18']) ?: 10;
    $openidc['dat_all']  = intval($goods['配置选项19']);
    $openidc['vm_uuid']  = $server['用户名'] ?: '';
    // API地址和Token来自服务器配置，不在产品配置中显示
    // 隐藏服务器主机名/IP，由插件自行处理
    $server['主机名'] = '';
    $server['ip地址'] = '';
    TEMPLATE::assign('openidc', $openidc);
}

add_swap_plug('产品控制面板详情', 'openidc_ClientAreaLIB');