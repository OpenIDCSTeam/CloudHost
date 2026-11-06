<?php
requirer('whm.lib.php','easypanel');

function easypanel_ConfigOptions() {

	# Should return an array of the module options for each product - maximum of 24

    $configarray = array(
		 "CDN"				=> array("Type" => "yesno", "Description" => "CDN网站"),				//1
		 "空间类型"			=> array("Type" => "text", "Size" => "5","Description" => "语言模板,如php"),							//2
		 "web配额" 			=> array( "Type" => "text", "Size" => "5", "Description" => "MB" ),	//3
		 "数据库类型"			=> array( "Type" => "dropdown", "Options" => "mysql,sqlsrv"),		//4
		 "数据库配额" 		=> array( "Type" => "text", "Size" => "5", "Description" => "MB" ),	//5
		 "FTP" 				=> array( "Type" => "yesno", "Description" => "是否允许ftp" ),		//6
		 "独立日志" 			=> array( "Type" => "yesno", "Description" => "是否开启独立日志" ),	//7
	     "绑定域名数" 		=> array( "Type" => "text", "Size" => "5"),							//8
	     "连接数"			=> array( "Type" => "text", "Size" => "5"),							//9
	     "带宽限制"			=> array( "Type" => "text", "Size" => "5","Description" => "K/S"),	//10
		 "默认绑定到子目录" 	=> array( "Type" => "text","Size"=>"20"),							//11
		 "允许绑定子目录" 		=> array( "Type" => "yesno", "Description" => "是否允许绑定域名到子目录"),//12
		 "最多绑定子目录数" 	=> array( "Type" => "text","Size"=>"5"),							//13
    	 "流量限制"			=> array( "Type" => "text","Size"=>"5","Description" => "GB/月"),	//14
    	 "管理变量"			=> array( "Type" => "text","Size"=>"12"),							//15
    	 "工作数"			=> array( "Type" => "text","Size"=>"5"),							//16
    	 "附加参数"			=> array("Type" => "text",'Size'=>'12'),							//17
		 "EP端口"			=> array("Type" => "text",'Size'=>'7',"Description" => "输入为自定义端口,不输入为默认3312端口"),
		 "语言引擎"			=> array("Type" => "text","Description" => "如php52,php53,如果语言模板没有语言引擎,可为空"),
		 "模块"				=> array("Type" => "dropdown", "Options" => ",php,iis","Description" => "如果使用空间类型和语言引擎的话请不要选!!选中将无法使用,并且选择没安装的话就无法使用了"), 
	);
	return $configarray;
}
function easypanel_make_whm($params) {
	if ($params["serverhostname"]) {
		$domain = $params["serverhostname"];
	}
	else {
		$domain = $params["serverip"];
	}
	if(empty($params["configoption18"]))
		$port='3312';
	else
		$port=$params["configoption18"];
	$whm = new WhmClient();
	$whm->setUrl('http://'.$domain.':'.$port.'/');
	$whm->setSecurityKey($params["serveraccesshash"]);
	return $whm;	
}
function easypanel_call($whmCall,$params)
{
	if(empty($params["serveraccesshash"]))
		return '没有填写安全码,请将安全码填写到服务器哈希值里';
	$whm = easypanel_make_whm($params);
    $result = $whm->call($whmCall,300);
    if ($result===false) {
    	return "不能连接到主机";
    }
	if(!method_exists($result,'getCode'))
		return $result;
    if ($result->getCode()==200) {
    	return "成功";
    }
    return (string)$result->status;
}
function easypanel_update_account($params,$edit)
{
	$whmCall = new WhmCall('add_vh');
    $whmCall->addParam('name', $params["username"]);
    $whmCall->addParam('passwd',$params["password"]);
	$whmCall->addParam('cdn',($params["configoption1"]== 'on'?1:0));
	$whmCall->addParam('templete',$params["configoption2"]);
    $whmCall->addParam('web_quota',$params["configoption3"]);
	$whmCall->addParam('db_type',$params["configoption4"]);
    $whmCall->addParam('db_quota',$params["configoption5"]);
    $whmCall->addParam('ftp',($params["configoption6"]== 'on'?1:0));
	$whmCall->addParam('log_file',($params["configoption7"]== 'on'?1:0));
    $whmCall->addParam('domain',$params["configoption8"]);
    $whmCall->addParam('max_connect',$params["configoption9"]);
    $whmCall->addParam('speed_limit',intval($params["configoption10"])*1024);	
	$whmCall->addParam('subdir',$params["configoption11"]);
	$whmCall->addParam('subdir_flag',($params["configoption12"]== 'on'?1:0));
	$whmCall->addParam('max_subdir',$params["configoption13"]);
	$whmCall->addParam('flow',$params["configoption14"]);
	$whmCall->addParam('envs',$params["configoption15"]);
	$whmCall->addParam('max_worker',$params["configoption16"]);
	$whmCall->addParam('subtemplete',$params["configoption19"]);
	$whmCall->addParam('module',$params["configoption20"]);
    $whmCall->addParam('vhost_domains',$params["domain"]);
    $whmCall->addParam('htaccess',1);
    $whmCall->addParam('access',1);
    if (trim($params["configoption17"]) != "") {
    	/*附加参数处理*/
    		$explode = explode('&', $params["configoption17"]);
    		//多个参数
    		if (is_array($explode)) {
    			foreach($explode as $e) {
    				$k = explode('=', $e);
    				if ($k[0]=='c' || $k[0]=='a' || $k[0] == 's' || $k[0] == 'r') {
    					continue;
    				}
    				$whmCall->addParam($k[0],$k[1]);
    			}
    			//一个参数
    		}else {
    			$k = explode('=',$params["configoption17"]);
    			if (is_array($k)) {
    				if ($k[0]=='c' || $k[0]=='a' || $k[0] == 's' || $k[0] == 'r') {
    					continue;
    				}
    				$whmCall->addParam($k[0],$k[1]);
    			}
    		}
    }
    
    if ($edit) {
    	$whmCall->addParam('edit',1);
    }
    $whmCall->addParam('init',1);
   	return easypanel_call($whmCall,$params);
}
function easypanel_CreateAccount($params) {//开通
	if ($params['username']=="") {
		return "username cann't be empty";
	}
	return easypanel_update_account($params,false);
}

function easypanel_TerminateAccount($params) {//删除

	$whmCall = new WhmCall('del_vh');
	$whmCall->addParam('name', $params["username"]);
	return easypanel_call($whmCall,$params);
}

function easypanel_SuspendAccount($params) {//状态启动

	$whmCall = new WhmCall('update_vh');
	$whmCall->addParam('name', $params["username"]);
	$whmCall->addParam('status',1);
	return easypanel_call($whmCall,$params);
}

function easypanel_UnsuspendAccount($params) {//状态停止	
	$whmCall = new WhmCall('update_vh');
	$whmCall->addParam('name', $params["username"]);
	$whmCall->addParam('status',0);
	return easypanel_call($whmCall,$params);
}

function easypanel_ChangePassword($params) {//修改密码

	$whmCall = new WhmCall('change_password');
	$whmCall->addParam('name', $params["username"]);
	$whmCall->addParam('passwd',$params["password"]);
	return easypanel_call($whmCall,$params);
}

function easypanel_ChangePackage($params) {//升降产品
	return easypanel_update_account($params,true);
}

function easypanel_ClientArea($params) {//多出操作按钮
	$lang=plug_lang_get('easypanel','','2');
	if ($params["serverhostname"]) {
		$domain = $params["serverhostname"];
	}
	else {
		$domain = $params["serverip"];
	}
	if(empty($params["configoption18"]))
		$port='3312';
	else
		$port=$params["configoption18"];
	$code = array(
				'<a href="javascript:document.easypanellogin.submit();" target="_blank">'.$lang['直接登录(自定义密码无效)'].'</a>',
				'<form name="easypanellogin" action="http://'.$domain.':'.$port.'/vhost/?c=session&a=login" method="post" target="_blank"><input type="hidden" name="username" value="'.$params["username"].'" /><input type="hidden" name="passwd" value="'.$params["password"].'" /></form>',
				'<a href="http://'.$domain.':'.$port.'/vhost/" target="_blank">'.$lang['管理地址(http)'].'</a>'
				);
	return $code;

}

function easypanel_AdminLink($params) {//后台直接登录
	if ($params["serverhostname"]) {
		$domain = $params["serverhostname"];
	}
	else {
		$domain = $params["serverip"];
	}
	if(empty($params["configoption18"]))
		$port='3312';
	else
		$port=$params["configoption18"];
	$code = '<form action="http://'.$domain.':'.$port.'/admin/?c=session&a=login" method="post" target="_blank"><input type="hidden" name="username" value="'.$params["serverusername"].'" /><input type="hidden" name="passwd" value="'.$params["serverpassword"].'" /><input type="submit" value="登录管理" /></form>';
	return $code;

}

function easypanel_LoginLink($params) {//看不出
	if ($params["serverhostname"]) {
		$domain = $params["serverhostname"];
	}
	else {
		$domain = $params["serverip"];
	}
	if(empty($params["configoption18"]))
		$port='3312';
	else
		$port=$params["configoption18"];
	echo "<a href=\"http://".$domain.":".$port."/vhost/?username=".$params["username"]."\" target=\"_blank\" style=\"color:#cc0000\">登录easypanel</a>";

}


function easypanel_ClientAreaLIB($goods,$server) {//看不出
$easypanel['disk']=$goods['配置选项3'];
$easypanel['sql']=$goods['配置选项5'];
$easypanel['domain']=$goods['配置选项8'];
if($easypanel['disk']=='')
$easypanel['disk']='NONE';
if($easypanel['disk']=='0')
$easypanel['disk']='NONE';
if($easypanel['sql']=='')
$easypanel['sql']='NONE';
if($easypanel['sql']=='0')
$easypanel['sql']='NONE';
if($easypanel['domain']=='')
$easypanel['domain']='UNLIMITED';
if($easypanel['domain']=='0')
$easypanel['domain']='UNLIMITED';
TEMPLATE::assign('easypanel',$easypanel);
}

add_swap_plug('产品控制面板详情','easypanel_ClientAreaLIB');
?>