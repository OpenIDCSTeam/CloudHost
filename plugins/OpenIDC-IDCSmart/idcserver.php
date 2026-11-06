<?php

use app\common\logic\RunMap;
use app\common\model\HostModel;
use think\Db;

define('IDCSERVER_DEBUG', true);

// 调试日志输出函数
function idcserver_debug($message, $data = null) {
    if (!IDCSERVER_DEBUG) return;
    $log = '[LXD-DEBUG] ' . $message;
    if ($data !== null) {
        $log .= ' | Data: ' . json_encode($data, JSON_UNESCAPED_UNICODE);
    }
    error_log($log);
}

// 插件元数据信息
function idcserver_MetaData()
{
    return [
        'DisplayName' => '魔方财务-OpenIDCS对接模块',
        'APIVersion'  => '0.0.1',
        'HelpDoc'     => 'https://github.com/OpenIDCSTeam/OpenIDCS-Client',
    ];
}

// 产品配置选项定义
function idcserver_ConfigOptions()
{
    return [
        // 基础配置
        'host_name' => [
            'type'        => 'text',
            'name'        => '主机名称',
            'description' => 'OpenIDCS的主机名',
            'default'     => 'server01',
            'key'         => 'host_name',
        ],
        'image' => [
            'type'        => 'text',
            'name'        => '默认镜像',
            'description' => '镜像名如debian12',
            'default'     => 'debian12',
            'key'         => 'image',
        ],
        // CPU
        'cpus' => [
            'type'        => 'text',
            'name'        => 'CPU数量',
            'description' => 'CPU 分配核心数量',
            'default'     => '1',
            'key'         => 'cpus',
        ],
        'cpu_allowance' => [
            'type'        => 'text',
            'name'        => 'CPU配额',
            'description' => 'CPU 可用的百分比',
            'default'     => '0',
            'key'         => 'cpu_allowance',
        ],
        // 内存/显存
        'memory' => [
            'type'        => 'text',
            'name'        => '内存(MB)',
            'description' => '内存大小(单位MB)',
            'default'     => '1024',
            'key'         => 'memory',
        ],
        'gpu_mem' => [
            'type'        => 'text',
            'name'        => '显存(MB)',
            'description' => '显存大小(单位MB)',
            'default'     => '16',
            'key'         => 'gpu_mem',
        ],
        // 系统硬盘
        'disk' => [
            'type'        => 'text',
            'name'        => '硬盘(MB)',
            'description' => '硬盘容量(单位MB)',
            'default'     => '10240',
            'key'         => 'disk',
        ],
        'disk_io_limit' => [
            'type'        => 'text',
            'name'        => '硬盘(IO)',
            'description' => '硬盘IOPS(IO每秒)',
            'default'     => '10000',
            'key'         => 'disk_io_limit',
        ],
        // 带宽
        'egress' => [
            'type'        => 'text',
            'name'        => '上行(Mbps)',
            'description' => '上传的速度(Mbps)',
            'default'     => '100',
            'key'         => 'egress',
        ],
        'ingress' => [
            'type'        => 'text',
            'name'        => '下行(Mbps)',
            'description' => '下载的速度(Mbps)',
            'default'     => '100',
            'key'         => 'ingress',
        ],
        // 网络配额
        'nat_limit' => [
            'type'        => 'text',
            'name'        => 'NAT端口数',
            'description' => 'NAT 端口可用数量',
            'default'     => '100',
            'key'         => 'nat_limit',
        ],
        'proxy_limit' => [
            'type'        => 'text',
            'name'        => 'WEB代理数',
            'description' => 'WEB 反向代理数量',
            'default'     => '100',
            'key'         => 'proxy_limit',
        ],
        // IP配额
        'ipv4_limit' => [
            'type'        => 'text',
            'name'        => 'IPv4个数',
            'description' => 'IPv4地址数量上限',
            'default'     => '1',
            'key'         => 'ipv4_limit',
        ],
        'ipv6_limit' => [
            'type'        => 'text',
            'name'        => 'IPv6个数',
            'description' => 'IPv6地址数量上限',
            'default'     => '1',
            'key'         => 'ipv6_limit',
        ],
        // 备份/ISO
        'bak_num' => [
            'type'        => 'text',
            'name'        => '备份数量',
            'description' => '允许最大备份数量',
            'default'     => '1',
            'key'         => 'bak_num',
        ],
        'iso_num' => [
            'type'        => 'text',
            'name'        => '镜像数量',
            'description' => '允许挂载镜像数量',
            'default'     => '1',
            'key'         => 'iso_num',
        ],
        // 流量
        'traffic_limit' => [
            'type'        => 'text',
            'name'        => '月流量(MB)',
            'description' => '流量限制(单位MB)',
            'default'     => '102400',
            'key'         => 'traffic_limit',
        ],
        'flu_rst' => [
            'type'        => 'text',
            'name'        => '流量规则',
            'description' => '重置天,限速(M),0',
            'default'     => '31,10,0',
            'key'         => 'flu_rst',
        ],
        // PCIe/USB
        'pci_ids' => [
            'type'        => 'text',
            'name'        => '分配PCI设备ID',
            'description' => '分配设备ID或MDEV',
            'default'     => '',
            'key'         => 'pci_ids',
        ],
        'usb_ids' => [
            'type'        => 'text',
            'name'        => '分配USB设备ID',
            'description' => '分配设备 VID:PID',
            'default'     => '',
            'key'         => 'usb_ids',
        ],
        // 数据盘
        'dat_num' => [
            'type'        => 'text',
            'name'        => '数据盘数量(个)',
            'description' => '允许最大数据盘数',
            'default'     => '0',
            'key'         => 'dat_num',
        ],
        'dat_all' => [
            'type'        => 'text',
            'name'        => '数据盘合计(MB)',
            'description' => '总共容量上限(MB)',
            'default'     => '0',
            'key'         => 'dat_all',
        ],
    ];
}

// 测试API连接
function idcserver_TestLink($params)
{
    idcserver_debug('开始测试API连接', $params);

    $data = [
        'url'  => '/api/system/stats',
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');


    if ($res === null) {
        return [
            'status' => 200,
            'data'   => [
                'server_status' => 0,
                'msg'           => "连接失败: 无法连接到服务器"
            ]
        ];
    } elseif (isset($res['error'])) {
        return [
            'status' => 200,
            'data'   => [
                'server_status' => 0,
                'msg'           => "连接失败: " . $res['error']
            ]
        ];
    } elseif (isset($res['code']) && $res['code'] == 200) {
        return [
            'status' => 200,
            'data'   => [
                'server_status' => 1,
                'msg'           => "连接成功"
            ]
        ];
    } elseif (isset($res['lxd_version'])) {
        return [
            'status' => 200,
            'data'   => [
                'server_status' => 1,
                'msg'           => "连接成功"
            ]
        ];
    } elseif (isset($res['code'])) {
        return [
            'status' => 200,
            'data'   => [
                'server_status' => 0,
                'msg'           => "连接失败: " . ($res['msg'] ?? '服务器返回错误')
            ]
        ];
    } else {
        return [
            'status' => 200,
            'data'   => [
                'server_status' => 0,
                'msg'           => "连接失败: 响应格式异常"
            ]
        ];
    }
}

// 客户区页面定义
function idcserver_ClientArea($params)
{
    $pages = [
        'info'      => ['name' => '产品信息'],
        'nat_acl'   => ['name' => 'NAT转发'],
        'ipv4_acl'  => ['name' => 'IPv4绑定'],
        'ipv6_acl'  => ['name' => 'IPv6绑定'],
        'proxy_acl' => ['name' => '反向代理'],
    ];

    return $pages;
}

// 允许客户端调用的函数列表（覆盖原有定义，移至此处）
// 注意：idcserver_AllowFunction 在下方保留原定义

// 客户区输出处理
function idcserver_ClientAreaOutput($params, $key)
{
    idcserver_debug('ClientAreaOutput调用', ['key' => $key, 'action' => $_GET['action'] ?? null]);

    if (isset($_GET['action'])) {
        $action = $_GET['action'];
        idcserver_debug('处理API请求', ['action' => $action, 'domain' => $params['domain'] ?? null]);

        if (empty($params['domain'])) {
            header('Content-Type: application/json');
            echo json_encode(['code' => 400, 'msg' => '容器域名未设置']);
            exit;
        }

        if ($action === 'natcheck') {
            header('Content-Type: application/json');
            echo json_encode(idcserver_natcheck($params));
            exit;
        }

        if ($action === 'proxycheck') {
            header('Content-Type: application/json');
            echo json_encode(idcserver_proxycheck($params));
            exit;
        }

        // 获取临时凭据，用于跳转到OpenIDCS管理页面
        if ($action === 'get_temp_token' || $action === 'openidc_manage') {
            header('Content-Type: application/json');
            echo json_encode(idcserver_getTempToken($params));
            exit;
        }

        // 电源控制操作
        if (in_array($action, ['on', 'off', 'reboot', 'suspend', 'unsuspend'])) {
            header('Content-Type: application/json');
            $powerMap = [
                'on'         => 'S_START',
                'off'        => 'H_CLOSE',
                'reboot'     => 'S_RESET',
                'suspend'    => 'A_PAUSE',
                'unsuspend'  => 'S_START',
            ];
            $hs_name = $params['configoptions']['host_name'] ?? 'default';
            $vm_uuid = $params['domain'];
            $powerData = [
                'url'  => '/api/client/powers/' . $hs_name . '/' . $vm_uuid,
                'type' => 'application/json',
                'data' => ['action' => $powerMap[$action]],
            ];
            $powerRes = idcserver_JSONCurl($params, $powerData, 'POST');
            echo json_encode(isset($powerRes['code']) && $powerRes['code'] == 200
                ? ['status' => 'success', 'msg' => $powerRes['msg'] ?? '操作成功']
                : ['status' => 'error', 'msg' => $powerRes['msg'] ?? '操作失败']);
            exit;
        }

        // VNC控制台
        if ($action === 'vnc') {
            header('Content-Type: application/json');
            echo json_encode(idcserver_vnc($params));
            exit;
        }

        $hs_name = $params['configoptions']['host_name'] ?? 'default';
        $vm_uuid = $params['domain'];

        $apiEndpoints = [
            'getinfo'    => '/api/client/detail/' . $hs_name . '/' . $vm_uuid,
            'getstats'   => '/api/client/detail/' . $hs_name . '/' . $vm_uuid,
            'gettraffic' => '/api/client/detail/' . $hs_name . '/' . $vm_uuid,
            'getinfoall' => '/api/client/detail/' . $hs_name . '/' . $vm_uuid,
            'natlist'    => '/api/client/natget/' . $hs_name . '/' . $vm_uuid,
            'ipv4list'   => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
            'ipv6list'   => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
            'proxylist'  => '/api/client/proxys/detail/' . $hs_name . '/' . $vm_uuid,
        ];

        $apiEndpoint = $apiEndpoints[$action] ?? '';

        if (!$apiEndpoint) {
            header('Content-Type: application/json');
            echo json_encode(['code' => 400, 'msg' => '不支持的操作: ' . $action]);
            exit;
        }

        $requestData = [
            'url'  => $apiEndpoint . '?_t=' . time(),
            'type' => 'application/x-www-form-urlencoded',
            'data' => [],
        ];

        $res = idcserver_Curl($params, $requestData, 'GET');

        if ($res === null) {
            $res = ['code' => 500, 'msg' => '连接服务器失败'];
        } elseif (!is_array($res)) {
            $res = ['code' => 500, 'msg' => '服务器返回了无效的响应格式'];
        } else {
            $res = idcserver_TransformAPIResponse($action, $res);
        }

        header('Content-Type: application/json');
        header('Cache-Control: no-cache, no-store, must-revalidate');
        header('Pragma: no-cache');
        header('Expires: 0');
        echo json_encode($res);
        exit;
    }

    if ($key == 'info') {
        return [
            'template' => 'templates/info.html',
            'vars'     => [],
        ];
    }

    if ($key == 'nat_acl') {
        // 使用正确的API端点获取NAT列表
        $hs_name = $params['configoptions']['host_name'] ?? 'default';
        $vm_uuid = $params['domain'];
        
        $requestData = [
            'url'  => '/api/client/natget/' . $hs_name . '/' . $vm_uuid . '?_t=' . time(),
            'type' => 'application/x-www-form-urlencoded',
            'data' => [],
        ];
        $res = idcserver_Curl($params, $requestData, 'GET');

        $nat_limit = intval($params['configoptions']['nat_limit'] ?? 5);
        $current_count = idcserver_getNATRuleCount($params);

        return [
            'template' => 'templates/nat.html',
            'vars'     => [
                'list' => $res['data'] ?? [],
                'msg'  => $res['msg'] ?? '',
                'nat_limit' => $nat_limit,
                'current_count' => $current_count,
                'remaining_count' => max(0, $nat_limit - $current_count),
                'udp_enabled' => false, // UDP已禁用
                'nat_enabled' => true,
            ],
        ];
    }

    if ($key == 'ipv4_acl') {
        // 使用正确的API端点获取IPv4列表
        $hs_name = $params['configoptions']['host_name'] ?? 'default';
        $vm_uuid = $params['domain'];
        
        $requestData = [
            'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid . '?_t=' . time(),
            'type' => 'application/x-www-form-urlencoded',
            'data' => [],
        ];
        $res = idcserver_Curl($params, $requestData, 'GET');

        $ipv4_limit = intval($params['configoptions']['ipv4_limit'] ?? 1);
        $current_count = idcserver_getIPv4BindingCount($params);

        return [
            'template' => 'templates/ipv4.html',
            'vars'     => [
                'list' => $res['data'] ?? [],
                'msg'  => $res['msg'] ?? '',
                'ipv4_limit' => $ipv4_limit,
                'current_count' => $current_count,
                'remaining_count' => max(0, $ipv4_limit - $current_count),
                'container_name' => $params['domain'],
                'ipv4_enabled' => true,
                'ipv4_allow_delete' => true, // 默认允许删除
            ],
        ];
    }

    if ($key == 'ipv6_acl') {
        // 使用正确的API端点获取IPv6列表
        $hs_name = $params['configoptions']['host_name'] ?? 'default';
        $vm_uuid = $params['domain'];
        
        $requestData = [
            'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid . '?_t=' . time(),
            'type' => 'application/x-www-form-urlencoded',
            'data' => [],
        ];
        $res = idcserver_Curl($params, $requestData, 'GET');

        $ipv6_limit = intval($params['configoptions']['ipv6_limit'] ?? 1);
        $current_count = idcserver_getIPv6BindingCount($params);

        return [
            'template' => 'templates/ipv6.html',
            'vars'     => [
                'list' => $res['data'] ?? [],
                'msg'  => $res['msg'] ?? '',
                'ipv6_limit' => $ipv6_limit,
                'current_count' => $current_count,
                'remaining_count' => max(0, $ipv6_limit - $current_count),
                'container_name' => $params['domain'],
                'ipv6_enabled' => true,
                'ipv6_allow_delete' => true, // 默认允许删除
            ],
        ];
    }
    
    if ($key == 'proxy_acl') {
        $proxy_enabled = ($params['configoptions']['proxy_enabled'] ?? 'false') === 'true';
        
        // 使用正确的API端点获取反向代理列表
        $hs_name = $params['configoptions']['host_name'] ?? 'default';
        $vm_uuid = $params['domain'];
        
        $requestData = [
            'url'  => '/api/client/proxys/detail/' . $hs_name . '/' . $vm_uuid . '?_t=' . time(),
            'type' => 'application/x-www-form-urlencoded',
            'data' => [],
        ];
        
        $res = idcserver_Curl($params, $requestData, 'GET');
        
        $proxy_limit = intval($params['configoptions']['proxy_limit'] ?? 1);
        $current_count = is_array($res['data']) ? count($res['data']) : 0;
        
        return [
            'template' => 'templates/proxy.html',
            'vars'     => [
                'list' => $res['data'] ?? [],
                'msg'  => $res['msg'] ?? '',
                'proxy_limit' => $proxy_limit,
                'current_count' => $current_count,
                'remaining_count' => max(0, $proxy_limit - $current_count),
                'container_name' => $params['domain'],
                'proxy_enabled' => $proxy_enabled,
            ],
        ];
    }
}

function idcserver_getContainerIPs($params, $hostname) {
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $hostname;
    $network_mode = $params['configoptions']['network_mode'] ?? 'mode1';
    $server_ipv4 = $params['server_ip'];
    $server_ipv6 = $params['server_ipv6'] ?? '';
    
    $dedicatedip = '';
    $assignedips = '';
    
    // 尝试从虚拟机配置中获取IP地址（对齐Python的API端点）
    try {
        $data = [
            'url'  => '/api/client/detail/' . $hs_name . '/' . $vm_uuid,
            'type' => 'application/x-www-form-urlencoded',
            'data' => [],
        ];
        $res = idcserver_Curl($params, $data, 'GET');
        
        if (isset($res['code']) && $res['code'] == 200 && isset($res['data']['config'])) {
            $vm_config = $res['data']['config'];
            
            // 从nic_all中获取IP地址（对齐Python的NCConfig结构）
            if (isset($vm_config['nic_all']) && is_array($vm_config['nic_all'])) {
                $ipv4_addresses = [];
                $ipv6_addresses = [];
                
                foreach ($vm_config['nic_all'] as $nic_name => $nic_config) {
                    if (isset($nic_config['ip4_addr']) && !empty($nic_config['ip4_addr'])) {
                        $ipv4_addresses[] = $nic_config['ip4_addr'];
                    }
                    if (isset($nic_config['ip6_addr']) && !empty($nic_config['ip6_addr'])) {
                        $ipv6_addresses[] = $nic_config['ip6_addr'];
                    }
                }
            }
        }
    } catch (Exception $e) {
        idcserver_debug('获取虚拟机IP失败', ['error' => $e->getMessage()]);
    }
    
    switch ($network_mode) {
        case 'mode1':
            $dedicatedip = $server_ipv4;
            $assignedips = '';
            break;
        case 'mode2':
            $dedicatedip = $server_ipv6;
            $assignedips = '';
            break;
        case 'mode3':
            $dedicatedip = $server_ipv4;
            $assignedips = $server_ipv6;
            break;
        case 'mode4':
            $dedicatedip = $server_ipv4;
            $ipv6_list = idcserver_getIndependentIPv6List($params);
            $assignedips = !empty($ipv6_list) ? $ipv6_list[0] : '';
            break;
        case 'mode5':
            $ipv4_list = idcserver_getIndependentIPv4List($params);
            $dedicatedip = !empty($ipv4_list) ? $ipv4_list[0] : '';
            $assignedips = '';
            break;
        case 'mode6':
            $ipv6_list = idcserver_getIndependentIPv6List($params);
            $dedicatedip = !empty($ipv6_list) ? $ipv6_list[0] : '';
            $assignedips = '';
            break;
        case 'mode7':
            $ipv4_list = idcserver_getIndependentIPv4List($params);
            $ipv6_list = idcserver_getIndependentIPv6List($params);
            $dedicatedip = !empty($ipv4_list) ? $ipv4_list[0] : '';
            $assignedips = !empty($ipv6_list) ? $ipv6_list[0] : '';
            break;
    }
    
    return [
        'dedicatedip' => $dedicatedip,
        'assignedips' => $assignedips,
    ];
}

function idcserver_getIndependentIPv4List($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200 && isset($res['data']) && is_array($res['data'])) {
        $ipv4_addresses = [];
        foreach ($res['data'] as $item) {
            if (isset($item['public_ipv4'])) {
                $ipv4_addresses[] = $item['public_ipv4'];
            }
        }
        return $ipv4_addresses;
    }

    return [];
}

function idcserver_getIndependentIPv6List($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200 && isset($res['data']) && is_array($res['data'])) {
        $ipv6_addresses = [];
        foreach ($res['data'] as $item) {
            if (isset($item['public_ipv6'])) {
                $ipv6_addresses[] = $item['public_ipv6'];
            }
        }
        return $ipv6_addresses;
    }

    return [];
}

// 允许客户端调用的函数列表
function idcserver_AllowFunction()
{
    return [
        'client' => ['natadd', 'natdel', 'natlist', 'natcheck', 'ipv4add', 'ipv4del', 'ipv4list', 'ipv6add', 'ipv6del', 'ipv6list', 'proxyadd', 'proxydel', 'proxylist', 'proxycheck', 'openidc_manage', 'on', 'off', 'reboot', 'suspend', 'unsuspend', 'vnc'],
    ];
}

// 订单核验（风控检查），返回通过则自动开通
function idcserver_FraudCheck($params)
{
    return ['status' => 'success', 'msg' => '核验通过'];
}

// 获取临时凭据（内部函数，供 ClientAreaOutput 调用）
function idcserver_getTempToken($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    if (empty($vm_uuid)) {
        return ['code' => 400, 'msg' => '容器域名未设置'];
    }

    $data = [
        'url'  => '/api/client/temptoken/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if ($res === null) {
        return ['code' => 500, 'msg' => '连接OpenIDCS服务器失败'];
    }

    if (isset($res['code']) && $res['code'] == 200 && isset($res['data']['temp_token'])) {
        // 构建跳转URL：https://<server>:<port>/api/client/templogin?token=<temp_token>
        $protocol = (!empty($params['server_secure']) && $params['server_secure'] == 'on') ? 'https' : 'http';
        $jump_url = $protocol . '://' . $params['server_ip'] . ':' . $params['port'] . '/api/client/templogin?token=' . $res['data']['temp_token'];
        return [
            'code'     => 200,
            'msg'      => '临时凭据生成成功',
            'data'     => [
                'temp_token' => $res['data']['temp_token'],
                'expire'     => $res['data']['expire'],
                'jump_url'   => $jump_url,
            ],
        ];
    }

    return [
        'code' => $res['code'] ?? 500,
        'msg'  => $res['msg'] ?? '获取临时凭据失败',
    ];
}

// 跳转到OpenIDCS管理页面（客户端可调用函数）
function idcserver_openidc_manage($params)
{
    return idcserver_getTempToken($params);
}

// 创建虚拟机
function idcserver_CreateAccount($params)
{
    idcserver_debug('开始创建虚拟机', ['domain' => $params['domain']]);

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain']; // 使用domain作为虚拟机UUID

    // 生成随机端口和密码
    $vc_port = rand(10000, 59999);
    $vc_pass = $params['password'] ?? randStr(16);

    // 内存/硬盘/带宽/流量均为纯数字MB/Mbps，直接读取
    $mem_num = (int)($params['configoptions']['memory'] ?? 1024);
    $hdd_num = (int)($params['configoptions']['disk'] ?? 8192);
    $speed_u = (int)($params['configoptions']['egress'] ?? 100);
    $speed_d = (int)($params['configoptions']['ingress'] ?? 100);
    $flu_num = (int)($params['configoptions']['traffic_limit'] ?? 102400);

    // 解析CPU百分比（纯数字，0表示不限制）
    $cpu_per = (int)str_replace('%', '', $params['configoptions']['cpu_allowance'] ?? '0');

    // 解析流量控制规则 flu_rst：格式 "天数,限速Mbps,上次重置时间戳"
    $flu_rst_str = $params['configoptions']['flu_rst'] ?? '31,10,0';
    $flu_rst_parts = array_map('intval', explode(',', $flu_rst_str));
    $flu_rst = [
        $flu_rst_parts[0] ?? 31,
        $flu_rst_parts[1] ?? 10,
        $flu_rst_parts[2] > 0 ? $flu_rst_parts[2] : time(),
    ];

    $vm_name = $vm_uuid;

    // 构建虚拟机配置（完全对齐Python的VMConfig结构）
    $vm_config = [
        'vm_uuid' => $vm_uuid,
        'vm_name' => $vm_name,
        'os_name' => $params['configoptions']['image'] ?? 'debian12',
        'os_pass' => $vc_pass,
        'vc_port' => $vc_port,
        'vc_pass' => $vc_pass,
        // 资源配置
        'cpu_num' => (int)($params['configoptions']['cpus'] ?? 1),
        'cpu_per' => $cpu_per,
        'gpu_mem' => (int)($params['configoptions']['gpu_mem'] ?? 0),
        'mem_num' => $mem_num,
        'hdd_num' => $hdd_num,
        'hdd_iop' => (int)($params['configoptions']['disk_io_limit'] ?? 0),
        // 网络配置
        'speed_u' => $speed_u,
        'speed_d' => $speed_d,
        'flu_num' => $flu_num,
        'flu_rst' => $flu_rst,
        'nat_num' => (int)($params['configoptions']['nat_limit'] ?? 5),
        'web_num' => (int)($params['configoptions']['proxy_limit'] ?? 1),
        // 配额配置
        'bak_num' => (int)($params['configoptions']['bak_num'] ?? 1),
        'iso_num' => (int)($params['configoptions']['iso_num'] ?? 1),
        'pci_num' => count(array_filter(array_map('trim', explode(',', $params['configoptions']['pci_ids'] ?? '')))),
        'usb_num' => count(array_filter(array_map('trim', explode(',', $params['configoptions']['usb_ids'] ?? '')))),
        'dat_num' => (int)($params['configoptions']['dat_num'] ?? 1),
        'dat_all' => (int)($params['configoptions']['dat_all'] ?? 0),
        // 附加配置（空数组/列表）
        'nic_all' => [],
        'hdd_all' => [],
        'iso_all' => [],
        'nat_all' => [],
        'web_all' => [],
        'backups' => [],
        'own_all' => ['admin'],
        'pci_all' => idcserver_parseDeviceIds($params['configoptions']['pci_ids'] ?? ''),
        'usb_all' => idcserver_parseDeviceIds($params['configoptions']['usb_ids'] ?? ''),
    ];

    // 默认NAT网卡配置
    $vm_config['nic_all'] = [
        'eth0' => [
            'nic_type' => 'nat',
            'mac_addr' => generateRandomMac(),
            'ip4_addr' => '',
            'ip6_addr' => '',
        ],
    ];

    $data = [
        'url'  => '/api/client/create/' . $hs_name,
        'type' => 'application/json',
        'data' => $vm_config,
    ];

    idcserver_debug('发送创建请求', $data);
    $res = idcserver_JSONCurl($params, $data, 'POST');
    idcserver_debug('创建响应', $res);

    if (isset($res['code']) && $res['code'] == 200) {
        sleep(2);

        // 构建IP信息
        $ipInfo = idcserver_getContainerIPs($params, $params['domain']);
        $dedicatedip = $ipInfo['dedicatedip'];
        $assignedips = $ipInfo['assignedips'];

        $update = [
            'dedicatedip'  => $dedicatedip,
            'assignedips'  => $assignedips,
            'domainstatus' => 'Active',
            'username'     => 'root',
            'password'     => $vc_pass,
        ];

        // SSH端口使用VNC端口
        $update['port'] = $vc_port;

        try {
            Db::name('host')->where('id', $params['hostid'])->update($update);
            idcserver_debug('数据库更新成功', $update);
        } catch (\Exception $e) {
             return ['status' => 'error', 'msg' => ($res['msg'] ?? '创建成功，但同步数据到面板失败: ' . $e->getMessage())];
        }

        return ['status' => 'success', 'msg' => $res['msg'] ?? '虚拟机创建成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '创建失败'];
    }
}

// 生成随机MAC地址
function generateRandomMac() {
    $mac = '';
    for ($i = 0; $i < 6; $i++) {
        $mac .= sprintf('%02x', mt_rand(0, 255));
        if ($i < 5) $mac .= ':';
    }
    return $mac;
}

// 解析大小字符串（支持MB/GB单位）
// 解析设备ID字符串，转换为pci_all/usb_all所需的字典格式（键为设备ID，值为空对象）
function idcserver_parseDeviceIds($ids_str) {
    if (empty(trim($ids_str))) {
        return [];
    }
    $result = [];
    $ids = array_filter(array_map('trim', explode(',', $ids_str)));
    foreach ($ids as $id) {
        if ($id !== '') {
            $result[$id] = [];  // 空对象，后端VFConfig/USBInfos会用默认值初始化
        }
    }
    return $result;
}

function idcserver_parseSize($size_str) {
    $size_str = trim(strtoupper($size_str));
    if (preg_match('/(\d+)\s*(MB|GB)?/', $size_str, $matches)) {
        $value = (int)$matches[1];
        $unit = $matches[2] ?? 'MB';
        if ($unit === 'GB') {
            return $value * 1024; // 转换为MB
        }
        return $value;
    }
    return (int)$size_str; // 默认当作MB
}

// 解析带宽字符串（支持Mbit/Gbit单位）
function idcserver_parseBandwidth($bandwidth_str) {
    $bandwidth_str = trim(strtoupper($bandwidth_str));
    if (preg_match('/(\d+)\s*(MBIT|GBIT)?/', $bandwidth_str, $matches)) {
        $value = (int)$matches[1];
        $unit = $matches[2] ?? 'MBIT';
        if ($unit === 'GBIT') {
            return $value * 1000; // 转换为Mbit
        }
        return $value;
    }
    return (int)$bandwidth_str; // 默认当作Mbit
}

// 同步虚拟机信息
function idcserver_Sync($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];
    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200) {
        if (class_exists('think\Db') && isset($params['hostid'])) {
            try {
                $ipInfo = idcserver_getContainerIPs($params, $params['domain']);
                
                $update_data = [
                    'dedicatedip' => $ipInfo['dedicatedip'],
                    'assignedips' => $ipInfo['assignedips'],
                ];

                // 从虚拟机配置中获取VNC端口
                if (isset($res['data']['config']['vc_port']) && !empty($res['data']['config']['vc_port'])) {
                    $update_data['port'] = $res['data']['config']['vc_port'];
                }

                Db::name('host')->where('id', $params['hostid'])->update($update_data);
            } catch (Exception $e) {
                idcserver_debug('同步数据库失败', ['error' => $e->getMessage()]);
            }
        }
        return ['status' => 'success', 'msg' => $res['msg'] ?? '同步成功'];
    }

    return ['status' => 'error', 'msg' => $res['msg'] ?? '同步失败'];
}

// 删除虚拟机
function idcserver_TerminateAccount($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/delete/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];
    $res = idcserver_Curl($params, $data, 'DELETE');

    return isset($res['code']) && $res['code'] == 200
        ? ['status' => 'success', 'msg' => $res['msg'] ?? '删除成功']
        : ['status' => 'error', 'msg' => $res['msg'] ?? '删除失败'];
}

// 启动虚拟机（对齐Python的API端点）
function idcserver_On($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/powers/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => ['action' => 'S_START'], // 对齐Python的VMPowers操作
    ];
    $res = idcserver_JSONCurl($params, $data, 'POST');

    return isset($res['code']) && $res['code'] == 200
        ? ['status' => 'success', 'msg' => $res['msg'] ?? '开机成功']
        : ['status' => 'error', 'msg' => $res['msg'] ?? '开机失败'];
}

// 关闭虚拟机（对齐Python的API端点）
function idcserver_Off($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/powers/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => ['action' => 'H_CLOSE'], // 对齐Python的VMPowers操作
    ];
    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? '关机成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '关机失败'];
    }
}

// 暂停虚拟机（对齐Python的API端点）
function idcserver_SuspendAccount($params)
{
    idcserver_debug('开始暂停虚拟机', ['domain' => $params['domain']]);

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/powers/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => ['action' => 'A_PAUSE'], // 对齐Python的VMPowers操作
    ];
    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? '虚拟机暂停成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '虚拟机暂停失败'];
    }
}

// 恢复虚拟机（对齐Python的API端点）
function idcserver_UnsuspendAccount($params)
{
    idcserver_debug('开始解除暂停虚拟机', ['domain' => $params['domain']]);

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/powers/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => ['action' => 'S_START'], // 恢复就是启动
    ];
    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? '虚拟机恢复成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '虚拟机恢复失败'];
    }
}

// 重启虚拟机（对齐Python的API端点）
function idcserver_Reboot($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/powers/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => ['action' => 'S_RESET'], // 对齐Python的VMPowers操作
    ];
    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? '重启成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '重启失败'];
    }
}

// 获取容器NAT规则数量（对齐Python的API端点）
function idcserver_getNATRuleCount($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/natget/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200 && isset($res['data']) && is_array($res['data'])) {
        $rules = $res['data'];
        
        $counted = [];
        
        foreach ($rules as $rule) {
            $external_port = $rule['external_port'] ?? $rule['dport'] ?? '';
            $internal_port = $rule['internal_port'] ?? $rule['sport'] ?? '';
            $external_port_end = $rule['external_port_end'] ?? $rule['dport_end'] ?? 0;
            $internal_port_end = $rule['internal_port_end'] ?? $rule['sport_end'] ?? 0;
            $protocol = strtolower($rule['protocol'] ?? $rule['dtype'] ?? '');
            
            $key = $external_port . '_' . $internal_port . '_' . $external_port_end . '_' . $internal_port_end;
            
            if (!isset($counted[$key])) {
                $counted[$key] = [
                    'external_port' => $external_port,
                    'external_port_end' => $external_port_end,
                    'protocols' => []
                ];
            }
            
            $counted[$key]['protocols'][] = $protocol;
        }
        
        $totalCount = 0;
        foreach ($counted as $item) {
            if ($item['external_port_end'] > 0) {
                $portCount = $item['external_port_end'] - $item['external_port'] + 1;
                $totalCount += $portCount;
            } else {
                $totalCount += 1;
            }
        }
        
        return $totalCount;
    }

    return 0;
}

// 添加NAT端口转发（对齐Python的API端点和PortData结构）
function idcserver_natadd($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $lan_port = intval($post['sport'] ?? 0); // 内网端口
    $wan_port = intval($post['dport'] ?? 0); // 外网端口（0表示由后端自动分配）
    $description = trim($post['description'] ?? '');

    $nat_limit = intval($params['configoptions']['nat_limit'] ?? 5);
    $current_count = idcserver_getNATRuleCount($params);
    
    if ($lan_port <= 0 || $lan_port > 65535) {
        return ['status' => 'error', 'msg' => '内网端口超过范围'];
    }
    
    if ($current_count >= $nat_limit) {
        return ['status' => 'error', 'msg' => "NAT规则数量已达到限制（{$nat_limit}条），无法添加更多规则"];
    }

    // 构建PortData结构（对齐Python的PortData字段：lan_port/wan_port/lan_addr/nat_tips）
    $port_data = [
        'lan_port' => $lan_port,
        'wan_port' => $wan_port, // 0时后端自动分配外网端口
        'lan_addr' => '',        // 由后端自动填充
        'nat_tips' => $description,
    ];

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/natadd/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => $port_data,
    ];

    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => "NAT转发添加成功（{$protocol}）"];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'NAT转发添加失败'];
    }
}

// 删除NAT端口转发（对齐Python的API端点）
function idcserver_natdel($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $rule_index = intval($post['rule_index'] ?? -1);
    
    if ($rule_index < 0) {
        return ['status' => 'error', 'msg' => '缺少规则索引参数'];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/natdel/' . $hs_name . '/' . $vm_uuid . '/' . $rule_index,
        'type' => 'application/x-www-form-urlencoded',
        'data' => '',
    ];

    $res = idcserver_Curl($params, $data, 'DELETE');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? 'NAT转发删除成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'NAT转发删除失败'];
    }
}

// 查询虚拟机运行状态（对齐Python的API端点）
function idcserver_Status($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/status/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];
    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200) {
        $result = ['status' => 'success'];

        // 后端返回 data.power_status（VMPowers枚举名称）
        $data = $res['data'] ?? [];
        $vmStatus = '';
        if (isset($data['power_status'])) {
            $vmStatus = $data['power_status'];
        } elseif (isset($data[0]['vm_status'])) {
            // 兼容旧格式（列表）
            $vmStatus = $data[0]['vm_status'];
        }

        switch (strtoupper($vmStatus)) {
            case 'STARTED':
            case 'RUNNING':
                $result['data']['status'] = 'on';
                $result['data']['des'] = '运行中';
                break;
            case 'STOPPED':
            case 'SHUTDOWN':
                $result['data']['status'] = 'off';
                $result['data']['des'] = '已关机';
                break;
            case 'SUSPEND':
            case 'FROZEN':
            case 'SUSPENDED':
            case 'PAUSED':
                $result['data']['status'] = 'suspend';
                $result['data']['des'] = '已暂停';
                break;
            default:
                $result['data']['status'] = 'unknown';
                $result['data']['des'] = '无法获取状态';
                break;
        }

        return $result;
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '获取状态失败'];
    }
}

// 重置容器密码
function idcserver_CrackPassword($params, $new_pass)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/password/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => [
            'password' => $new_pass,
        ],
    ];
    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        try {
            Db::name('host')->where('id', $params['hostid'])->update(['password' => $new_pass]);
        } catch (\Exception $e) {
            return ['status' => 'error', 'msg' => ($res['msg'] ?? $res['message'] ?? '密码重置成功，但同步新密码到面板数据库失败: ' . $e->getMessage())];
        }
        return ['status' => 'success', 'msg' => $res['msg'] ?? $res['message'] ?? '密码重置成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? $res['message'] ?? '密码重置失败'];
    }
}

// 重装容器操作系统
function idcserver_Reinstall($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    $reinstall_pass = $params['password'] ?? '';
    $os_name = $params['configoptions']['image'] ?? ''; // 从产品配置中获取操作系统

    if (empty($os_name)) {
        return ['status' => 'error', 'msg' => '操作系统参数错误'];
    }

    // 构建重装配置（使用VMConfig格式）
    $reinstall_config = [
        'os_name' => $os_name,
        'os_pass' => $reinstall_pass,
    ];

    $data = [
        'url'  => '/api/client/update/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => $reinstall_config,
    ];
    
    $res = idcserver_JSONCurl($params, $data, 'PUT');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? $res['message'] ?? '重装成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? $res['message'] ?? '重装失败'];
    }
}

// 发送JSON格式的cURL请求
function idcserver_JSONCurl($params, $data = [], $request = 'POST')
{
    $curl = curl_init();

    $protocol = (!empty($params['server_secure']) && $params['server_secure'] == 'on') ? 'https' : 'http';
    $url = $protocol . '://' . $params['server_ip'] . ':' . $params['port'] . $data['url'];

    $curlOptions = [
        CURLOPT_URL            => $url,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_ENCODING       => '',
        CURLOPT_MAXREDIRS      => 10,
        CURLOPT_TIMEOUT        => 30,
        CURLOPT_CONNECTTIMEOUT => 10,
        CURLOPT_FOLLOWLOCATION => true,
        CURLOPT_HTTP_VERSION   => CURL_HTTP_VERSION_1_1,
        CURLOPT_CUSTOMREQUEST  => $request,
        CURLOPT_POSTFIELDS     => json_encode($data['data']),
        CURLOPT_HTTPHEADER     => [
            'Authorization: Bearer ' . $params['accesshash'],
            'Content-Type: application/json',
        ],
    ];

    $curlOptions[CURLOPT_SSL_VERIFYPEER] = false;
    $curlOptions[CURLOPT_SSL_VERIFYHOST] = false;
    $curlOptions[CURLOPT_SSLVERSION] = CURL_SSLVERSION_TLSv1_2;

    curl_setopt_array($curl, $curlOptions);

    $response = curl_exec($curl);
    $errno    = curl_errno($curl);

    curl_close($curl);

    if ($errno) {
        return null;
    }

    return json_decode($response, true);
}

// 发送通用的cURL请求
function idcserver_Curl($params, $data = [], $request = 'POST')
{
    $curl = curl_init();

    $protocol = (!empty($params['server_secure']) && $params['server_secure'] == 'on') ? 'https' : 'http';
    $url = $protocol . '://' . $params['server_ip'] . ':' . $params['port'] . $data['url'];

    idcserver_debug('发送请求', [
        'url' => $url,
        'method' => $request
    ]);

    $postFields = ($request === 'POST' || $request === 'PUT') ? ($data['data'] ?? null) : null;
    if ($request === 'GET' && !empty($data['data']) && is_array($data['data'])) {
        $url .= (strpos($url, '?') === false ? '?' : '&') . http_build_query($data['data']);
    } elseif ($request === 'GET' && !empty($data['data']) && is_string($data['data'])) {
         $url .= (strpos($url, '?') === false ? '?' : '&') . $data['data'];
    }

    $curlOptions = [
        CURLOPT_URL            => $url,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_ENCODING       => '',
        CURLOPT_MAXREDIRS      => 10,
        CURLOPT_TIMEOUT        => 30,
        CURLOPT_CONNECTTIMEOUT => 10,
        CURLOPT_FOLLOWLOCATION => true,
        CURLOPT_HTTP_VERSION   => CURL_HTTP_VERSION_1_1,
        CURLOPT_CUSTOMREQUEST  => $request,
        CURLOPT_HTTPHEADER     => [
            'Authorization: Bearer ' . $params['accesshash'],
            'Content-Type: ' . ($data['type'] ?? 'application/x-www-form-urlencoded'),
        ],
    ];

    $curlOptions[CURLOPT_SSL_VERIFYPEER] = false;
    $curlOptions[CURLOPT_SSL_VERIFYHOST] = false;
    $curlOptions[CURLOPT_SSLVERSION] = CURL_SSLVERSION_TLSv1_2;

    curl_setopt_array($curl, $curlOptions);

    if ($postFields !== null) {
        curl_setopt($curl, CURLOPT_POSTFIELDS, $postFields);
    }

    $response = curl_exec($curl);
    $errno    = curl_errno($curl);
    $httpCode = curl_getinfo($curl, CURLINFO_HTTP_CODE);
    $curlError = curl_error($curl);

    curl_close($curl);

    idcserver_debug('请求响应', [
        'http_code' => $httpCode,
        'response_length' => strlen($response),
        'curl_errno' => $errno,
        'curl_error' => $curlError
    ]);

    if ($errno) {
        idcserver_debug('CURL错误', [
            'errno' => $errno,
            'error' => $curlError,
            'error_desc' => curl_strerror($errno)
        ]);
        return null;
    }

    $decoded = json_decode($response, true);
    idcserver_debug('解析响应', ['code' => $decoded['code'] ?? 'NO CODE']);
    return $decoded;
}

// 转换API响应以适配前端
function idcserver_TransformAPIResponse($action, $response)
{
    if (isset($response['error'])) {
        return [
            'code' => 400,
            'msg' => $response['error']
        ];
    }

    if (!isset($response['code']) || $response['code'] != 200) {
        return $response; 
    }

    switch ($action) {
        case 'getinfo':
            return $response;

        case 'getstats':
        case 'getinfoall':
            if (isset($response['data'])) {
                $data = $response['data'];

                $transformed = [
                    'code' => 200,
                    'msg' => '获取容器信息成功',
                    'data' => [
                        'hostname' => $data['hostname'] ?? '',
                        'status' => $data['status'] ?? '',
                        'ipv4' => $data['ipv4'] ?? '',
                        'ipv6' => $data['ipv6'] ?? '',
                        'type' => $data['type'] ?? '',
                        'created_at' => $data['created_at'] ?? '',
                        'cpus' => $data['config']['cpus'] ?? 1,
                        'memory' => $data['memory'] ?? 1024,
                        'disk' => $data['disk'] ?? 10240,
                        'config' => [
                            'cpus' => $data['config']['cpus'] ?? 1,
                            'memory' => $data['config']['memory'] ?? '1024 MB',
                            'disk' => $data['config']['disk'] ?? '10240 MB',
                            'traffic_limit' => $data['config']['traffic_limit'] ?? 0,
                        ],
                        'cpu_usage' => $data['cpu_usage'] ?? 0,
                        'memory_usage' => $data['memory_usage'] ?? '0 B',
                        'memory_usage_raw' => $data['memory_usage_raw'] ?? 0,
                        'disk_usage' => $data['disk_usage'] ?? '0 B',
                        'disk_usage_raw' => $data['disk_usage_raw'] ?? 0,
                        'traffic_usage' => $data['traffic_usage'] ?? '0 B',
                        'traffic_usage_raw' => $data['traffic_usage_raw'] ?? 0,
                        'cpu_percent' => $data['cpu_percent'] ?? 0,
                        'memory_percent' => $data['memory_percent'] ?? 0,
                        'disk_percent' => $data['disk_percent'] ?? 0,
                        'last_update' => date('Y-m-d H:i:s'),
                        'timestamp' => time(),
                    ]
                ];

                return $transformed;
            }
            break;

        case 'gettraffic':
            if (isset($response['data']['used'])) {
                return [
                    'code' => 200,
                    'msg' => '获取流量使用量成功',
                    'data' => [
                        'used' => $response['data']['used'],
                    ]
                ];
            }
            break;
        
        case 'ipv4list':
            if (isset($response['data']) && is_array($response['data'])) {
                return [
                    'code' => 200,
                    'msg' => $response['msg'] ?? 'IPv4列表获取成功',
                    'data' => [
                        'list' => $response['data'],
                        'limit' => 0,
                        'current' => count($response['data']),
                    ]
                ];
            }
            break;
        
        case 'ipv6list':
            if (isset($response['data']) && is_array($response['data'])) {
                return [
                    'code' => 200,
                    'msg' => $response['msg'] ?? 'IPv6列表获取成功',
                    'data' => [
                        'list' => $response['data'],
                        'limit' => 0,
                        'current' => count($response['data']),
                    ]
                ];
            }
            break;
        
        case 'natlist':
            if (isset($response['data']) && is_array($response['data'])) {
                return [
                    'code' => 200,
                    'msg' => $response['msg'] ?? 'NAT列表获取成功',
                    'data' => [
                        'list' => $response['data'],
                        'limit' => 0,
                        'current' => count($response['data']),
                    ]
                ];
            }
            break;
        
        case 'proxylist':
            if (isset($response['data']) && is_array($response['data'])) {
                return [
                    'code' => 200,
                    'msg' => $response['msg'] ?? 'Proxy列表获取成功',
                    'data' => [
                        'list' => $response['data'],
                        'limit' => 0,
                        'current' => count($response['data']),
                    ]
                ];
            }
            break;
    }

    return $response;
}

// 获取NAT规则列表（对齐Python的API端点）
function idcserver_natlist($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $requestData = [
        'url'  => '/api/client/natget/' . $hs_name . '/' . $vm_uuid . '?_t=' . time(),
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];
    $res = idcserver_Curl($params, $requestData, 'GET');
    if ($res === null) {
        return ['code' => 500, 'msg' => '连接API服务器失败', 'data' => []];
    }
    return $res;
}

function idcserver_natcheck($params)
{
    // 先尝试从URL查询参数获取
    $dport = intval($_GET['dport'] ?? 0);
    $dtype = strtolower(trim($_GET['dtype'] ?? ''));
    $hostname = trim($_GET['hostname'] ?? '');

    // 如果GET参数为空，尝试从POST获取
    if ($dport <= 0) {
        $dport = intval($_POST['dport'] ?? 0);
    }
    if (empty($dtype)) {
        $dtype = strtolower(trim($_POST['dtype'] ?? 'tcp'));
    }
    if (empty($hostname)) {
        $hostname = trim($_POST['hostname'] ?? '');
    }

    // 如果还是为空，尝试从原始POST数据解析
    if ($dport <= 0 || empty($hostname)) {
        $postRaw = file_get_contents("php://input");
        if (!empty($postRaw)) {
            parse_str($postRaw, $input);
            if ($dport <= 0) {
                $dport = intval($input['dport'] ?? 0);
            }
            if (empty($dtype)) {
                $dtype = strtolower(trim($input['dtype'] ?? 'tcp'));
            }
            if (empty($hostname)) {
                $hostname = trim($input['hostname'] ?? '');
            }
        }
    }

    // 如果hostname还是空，使用params中的domain
    if (empty($hostname)) {
        $hostname = trim($params['domain'] ?? '');
    }

    idcserver_debug('natcheck参数解析', [
        'dport' => $dport, 
        'dtype' => $dtype, 
        'hostname' => $hostname,
        'GET' => $_GET,
        'POST' => $_POST,
        'raw_input' => file_get_contents("php://input"),
        'params_domain' => $params['domain'] ?? 'null'
    ]);

    // 参数验证
    if ($dport <= 0) {
        return ['code' => 400, 'msg' => '缺少端口参数', 'data' => ['available' => false, 'reason' => '缺少端口参数']];
    }
    if (!in_array($dtype, ['tcp', 'udp'])) {
        return ['code' => 400, 'msg' => '协议类型错误', 'data' => ['available' => false, 'reason' => '协议类型错误']];
    }
    if ($dport < 10000 || $dport > 65535) {
        return ['code' => 400, 'msg' => '端口范围为10000-65535', 'data' => ['available' => false, 'reason' => '端口范围为10000-65535']];
    }
    if (empty($hostname)) {
        return ['code' => 400, 'msg' => '容器标识缺失', 'data' => ['available' => false, 'reason' => '容器标识缺失']];
    }

    // 使用GET请求调用后端API
    $queryParams = http_build_query([
        'hostname' => $hostname,
        'protocol' => $dtype,
        'port'     => $dport,
    ]);

    $requestData = [
        'url'  => '/api/nat/check?' . $queryParams,
        'type' => 'application/x-www-form-urlencoded',
        'data' => '',
    ];

    $res = idcserver_Curl($params, $requestData, 'GET');

    if ($res === null) {
        return ['code' => 500, 'msg' => '连接服务器失败', 'data' => ['available' => false, 'reason' => '连接服务器失败']];
    }

    if (!isset($res['code'])) {
        return ['code' => 500, 'msg' => '服务器返回无效数据', 'data' => ['available' => false, 'reason' => '服务器返回无效数据']];
    }

    return $res;
}

// 获取VNC控制台URL（对齐Python的API端点）
function idcserver_vnc($params) {
    idcserver_debug('VNC控制台请求', ['domain' => $params['domain']]);

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    // 检查虚拟机状态
    $statusData = [
        'url'  => '/api/client/status/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];
    $statusRes = idcserver_Curl($params, $statusData, 'GET');

    if (!isset($statusRes['code']) || $statusRes['code'] != 200) {
        return ['status' => 'error', 'msg' => $statusRes['msg'] ?? '无法获取虚拟机状态'];
    }

    // 检查虚拟机是否运行
    $statusData2 = $statusRes['data'] ?? [];
    $vmStatus = '';
    if (isset($statusData2['power_status'])) {
        $vmStatus = strtoupper($statusData2['power_status']);
    } elseif (isset($statusData2[0]['vm_status'])) {
        $vmStatus = strtoupper($statusData2[0]['vm_status']);
    }
    if (!in_array($vmStatus, ['RUNNING', 'STARTED'])) {
        return ['status' => 'error', 'msg' => '虚拟机未运行，无法连接控制台'];
    }

    // 获取VNC访问地址
    $remoteData = [
        'url'  => '/api/client/remote/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $remoteRes = idcserver_Curl($params, $remoteData, 'GET');

    if (!isset($remoteRes['code']) || $remoteRes['code'] != 200) {
        return ['status' => 'error', 'msg' => $remoteRes['msg'] ?? '生成控制台访问地址失败'];
    }

    $consoleUrl = $remoteRes['data']['url'] ?? '';
    if (empty($consoleUrl)) {
        return ['status' => 'error', 'msg' => '控制台访问地址为空'];
    }

    return [
        'status' => 'success',
        'url' => $consoleUrl,
        'msg' => '控制台连接已准备就绪'
    ];
}

// 后台自定义按钮定义
function idcserver_AdminButton($params)
{
    if (!empty($params['domain'])) {
        return [
            'TrafficReset'   => '流量重置',
            'OpenIDCManage'  => 'OpenIDC管理',
        ];
    }
    return [];
}

// 后台跳转到OpenIDC管理页面
function idcserver_OpenIDCManage($params)
{
    idcserver_debug('后台OpenIDC管理跳转', ['domain' => $params['domain']]);

    if (empty($params['domain'])) {
        return ['status' => 'error', 'msg' => '容器域名参数缺失'];
    }

    $result = idcserver_getTempToken($params);

    if (isset($result['code']) && $result['code'] == 200 && isset($result['data']['jump_url'])) {
        // 后台按钮通常不能直接跳转，返回URL供前端处理
        return [
            'status' => 'success',
            'msg'    => '临时凭据生成成功，请访问以下链接（5分钟内有效）：' . $result['data']['jump_url'],
        ];
    }

    return [
        'status' => 'error',
        'msg'    => $result['msg'] ?? '获取临时凭据失败',
    ];
}

// 处理流量重置请求
function idcserver_TrafficReset($params)
{
    idcserver_debug('流量重置请求', ['domain' => $params['domain']]);

    if (empty($params['domain'])) {
        return ['status' => 'error', 'msg' => '容器域名参数缺失'];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    // 通过更新虚拟机配置重置流量（flu_used=0）
    $data = [
        'url'  => '/api/client/update/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => ['flu_used' => 0],
    ];

    $res = idcserver_JSONCurl($params, $data, 'PUT');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? '流量统计已重置'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '流量重置失败'];
    }
}

// 获取IPv6绑定数量（对齐Python的API端点）
function idcserver_getIPv6BindingCount($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200 && isset($res['data']) && is_array($res['data'])) {
        return count($res['data']);
    }

    return 0;
}

// 添加IPv4独立绑定（对齐Python的API端点）
function idcserver_ipv4add($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $description = trim($post['description'] ?? '');

    $ipv4_limit = intval($params['configoptions']['ipv4_limit'] ?? 1);
    $current_count = idcserver_getIPv4BindingCount($params);
    
    if ($current_count >= $ipv4_limit) {
        return ['status' => 'error', 'msg' => "IPv4绑定数量已达到限制（{$ipv4_limit}个），无法添加更多绑定"];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    // 构建NCConfig结构（对齐Python）
    $nic_data = [
        'nic_type' => 'bridge',
        'mac_addr' => generateRandomMac(),
        'ip4_addr' => '', // 由后端自动分配
        'ip6_addr' => '',
        'nic_tips' => $description,
    ];

    $data = [
        'url'  => '/api/client/ipaddr/create/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => $nic_data,
    ];

    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? 'IPv4绑定添加成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'IPv4绑定添加失败'];
    }
}

// 删除IPv4独立绑定（对齐Python的API端点）
function idcserver_ipv4del($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $nic_index = intval($post['nic_index'] ?? -1);

    if ($nic_index < 0) {
        return ['status' => 'error', 'msg' => '缺少网卡索引参数'];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/ipaddr/delete/' . $hs_name . '/' . $vm_uuid . '/' . $nic_index,
        'type' => 'application/x-www-form-urlencoded',
        'data' => '',
    ];

    $res = idcserver_Curl($params, $data, 'DELETE');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? 'IPv4绑定删除成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'IPv4绑定删除失败'];
    }
}

// 获取IPv4绑定列表（对齐Python的API端点）
function idcserver_ipv4list($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded', 
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200) {
        $ipv4_limit = intval($params['configoptions']['ipv4_limit'] ?? 1);
        $current_count = count($res['data'] ?? []);
        
        return [
            'status' => 'success', 
            'data' => [
                'list' => $res['data'] ?? [],
                'limit' => $ipv4_limit,
                'current' => $current_count,
            ],
        ];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'IPv4绑定列表获取失败'];
    }
}

// 获取IPv4绑定数量（对齐Python的API端点）
function idcserver_getIPv4BindingCount($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200 && isset($res['data'])) {
        return count($res['data']);
    }

    return 0;
}

// 添加IPv6独立绑定（对齐Python的API端点）
function idcserver_ipv6add($params)
{
    $network_mode = $params['configoptions']['network_mode'] ?? 'mode1';
    if (!in_array($network_mode, ['mode4', 'mode6', 'mode7'])) {
        return ['status' => 'error', 'msg' => 'IPv6独立绑定功能未启用，请联系管理员配置为模式4、模式6或模式7。'];
    }
    
    parse_str(file_get_contents("php://input"), $post);

    $description = trim($post['description'] ?? '');

    $ipv6_limit = intval($params['configoptions']['ipv6_limit'] ?? 1);
    $current_count = idcserver_getIPv6BindingCount($params);
    
    if ($current_count >= $ipv6_limit) {
        return ['status' => 'error', 'msg' => "IPv6绑定数量已达到限制（{$ipv6_limit}个），无法添加更多绑定"];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    // 构建NCConfig结构（对齐Python）
    $nic_data = [
        'nic_type' => 'bridge',
        'mac_addr' => generateRandomMac(),
        'ip4_addr' => '',
        'ip6_addr' => '', // 由后端自动分配
        'nic_tips' => $description,
    ];

    $data = [
        'url'  => '/api/client/ipaddr/create/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => $nic_data,
    ];

    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? 'IPv6绑定添加成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'IPv6绑定添加失败'];
    }
}

// 删除IPv6独立绑定（对齐Python的API端点）
function idcserver_ipv6del($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $nic_index = intval($post['nic_index'] ?? -1);

    if ($nic_index < 0) {
        return ['status' => 'error', 'msg' => '缺少网卡索引参数'];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/ipaddr/delete/' . $hs_name . '/' . $vm_uuid . '/' . $nic_index,
        'type' => 'application/x-www-form-urlencoded',
        'data' => '',
    ];

    $res = idcserver_Curl($params, $data, 'DELETE');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => $res['msg'] ?? 'IPv6绑定删除成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? 'IPv6绑定删除失败'];
    }
}

// 获取IPv6绑定列表（对齐Python的API端点）
function idcserver_ipv6list($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/ipaddr/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded', 
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'data' => $res['data'] ?? [], 'msg' => $res['msg'] ?? ''];
    } else {
        return ['status' => 'error', 'data' => [], 'msg' => $res['msg'] ?? '获取IPv6绑定列表失败'];
    }
}

// 添加反向代理（对齐Python的API端点和WebProxy结构）
function idcserver_proxyadd($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $domain = trim($post['domain'] ?? '');
    $lan_port = intval($post['container_port'] ?? 80);
    $description = trim($post['description'] ?? '');
    $is_https = ($post['ssl_enabled'] ?? 'false') === 'true';

    if (empty($domain)) {
        return ['status' => 'error', 'msg' => '请输入域名'];
    }

    // 验证域名格式
    if (!preg_match('/^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/', $domain)) {
        return ['status' => 'error', 'msg' => '域名格式无效'];
    }

    // 检查数量限制
    $proxy_limit = intval($params['configoptions']['proxy_limit'] ?? 1);
    $current_count = idcserver_getProxyCount($params);
    
    if ($current_count >= $proxy_limit) {
        return ['status' => 'error', 'msg' => "已达到反向代理数量上限（{$proxy_limit}个），无法继续添加"];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    // 构建WebProxy结构（对齐Python）
    $proxy_data = [
        'web_addr' => $domain,
        'lan_port' => $lan_port,
        'lan_addr' => '', // 由后端自动填充
        'wan_addr' => '', // 由后端自动填充
        'is_https' => $is_https,
        'web_tips' => $description,
    ];

    $data = [
        'url'  => '/api/client/proxys/create/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/json',
        'data' => $proxy_data,
    ];

    $res = idcserver_JSONCurl($params, $data, 'POST');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => '反向代理添加成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '添加反向代理失败'];
    }
}

// 删除反向代理（对齐Python的API端点）
function idcserver_proxydel($params)
{
    parse_str(file_get_contents("php://input"), $post);

    $proxy_index = intval($post['proxy_index'] ?? -1);

    if ($proxy_index < 0) {
        return ['status' => 'error', 'msg' => '缺少代理索引参数'];
    }

    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];

    $data = [
        'url'  => '/api/client/proxys/delete/' . $hs_name . '/' . $vm_uuid . '/' . $proxy_index,
        'type' => 'application/x-www-form-urlencoded',
        'data' => '',
    ];

    $res = idcserver_Curl($params, $data, 'DELETE');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'msg' => '反向代理删除成功'];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '删除反向代理失败'];
    }
}

// 获取反向代理列表（对齐Python的API端点）
function idcserver_proxylist($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/proxys/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'data' => $res['data'] ?? [], 'msg' => $res['msg'] ?? ''];
    } else {
        return ['status' => 'error', 'data' => [], 'msg' => $res['msg'] ?? '获取反向代理列表失败'];
    }
}

// 检查域名是否可用
function idcserver_proxycheck($params)
{
    parse_str(file_get_contents("php://input"), $post);
    
    $domain = trim($post['domain'] ?? '');
    
    if (empty($domain)) {
        return ['status' => 'error', 'msg' => '请输入域名'];
    }
    
    $data = [
        'url'  => '/api/proxy/check?domain=' . urlencode($domain),
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];
    
    $res = idcserver_Curl($params, $data, 'GET');
    
    if (isset($res['code']) && $res['code'] == 200) {
        return ['status' => 'success', 'data' => $res['data'] ?? [], 'msg' => $res['msg'] ?? ''];
    } else {
        return ['status' => 'error', 'msg' => $res['msg'] ?? '检查域名失败'];
    }
}

// 获取反向代理数量（对齐Python的API端点）
function idcserver_getProxyCount($params)
{
    $hs_name = $params['configoptions']['host_name'] ?? 'default';
    $vm_uuid = $params['domain'];
    
    $data = [
        'url'  => '/api/client/proxys/detail/' . $hs_name . '/' . $vm_uuid,
        'type' => 'application/x-www-form-urlencoded',
        'data' => [],
    ];

    $res = idcserver_Curl($params, $data, 'GET');

    if (isset($res['code']) && $res['code'] == 200 && is_array($res['data'])) {
        return count($res['data']);
    }

    return 0;
}