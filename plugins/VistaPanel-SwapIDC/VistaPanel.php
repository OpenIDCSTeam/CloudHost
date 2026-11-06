<?php
function VistaPanel_ConfigOptions() {
	$configarray = array( "WHM包名" => array( "Type" => "text", "Size" => "25" ),
                          "最多FTP帐号" => array( "Type" => "text", "Size" => "5" ), 
						  "空间限制" => array( "Type" => "text", "Size" => "5", "Description" => "MB" ), 
						  "最多Email帐号" => array( "Type" => "text", "Size" => "5" ), 
						  "流量限制" => array( "Type" => "text", "Size" => "5", "Description" => "MB" ), 
						  "专用IP" => array( "Type" => "yesno" ), 
						  "Shell权限" => array( "Type" => "yesno", "Description" => "给予Shell权限" ), 
						  "最多SQL数据库" => array( "Type" => "text", "Size" => "5" ), 
						  "CGI权限" => array( "Type" => "yesno", "Description" => "给予CGI权限" ), 
						  "最多子域名" => array( "Type" => "text", "Size" => "5" ), 
						  "Frontpage扩展" => array( "Type" => "yesno", "Description" => "给予Frontpage扩展" ), 
						  "最多域名停放" => array( "Type" => "text", "Size" => "5" ), 
						  "cPanel主题" => array( "Type" => "text", "Size" => "15" ), 
						  "最多附加域名" => array( "Type" => "text", "Size" => "5" ), 
						  "经销商用户数量限制" => array( "Type" => "text", "Size" => "5", "Description" => "输入经销商可以销售的最大数量账户" ), 
						  "经销商权限限制" => array( "Type" => "yesno", "Description" => "选中来限制经销商的资源使用" ), 
						  "经销商空间限制" => array( "Type" => "text", "Size" => "7", "Description" => "MB" ), 
						  "经销商流量限制" => array( "Type" => "text", "Size" => "7", "Description" => "MB" ), 
						  "允许空间超买" => array( "Type" => "yesno", "Description" => "MB" ), 
						  "允许流量超买" => array( "Type" => "yesno", "Description" => "MB" ), 
						  "经销商ACL列表" => array( "Type" => "text", "Size" => "20" ), 
						  "添加包名前缀" => array( "Type" => "yesno", "Description" => "在包名前面加上 用户名_包名 如果是购买的分销主机基本都是这样的格式" ), 
						  "配置域名服务器" => array( "Type" => "yesno", "Description" => "安装 ns1/ns2 NS服务器" ), 
						  "经销商所有权" => array( "Type" => "yesno", "Description" => "设置经销商有自己的账户" ),
						  "VistaPanel 登陆地址" => array( "Type" => "text", "Description" => "必填* 每个VistaPanel都有自己的登陆地址,要输入完整地址结尾要带/login.php" )
						);
	return $configarray;
}


function VistaPanel_ClientArea($params) {
	$lang=plug_lang_get('VistaPanel','','2');
	$code = array('<a href="javascript:document.vistapanellogin.submit();" target="_blank">'.$lang['直接登录(自定义密码无效)'].'</a>');
	return $code;
}


function VistaPanel_AdminLink($params) {
	$whmport = "2087";
	$http = "https";
	if ($params["serverhostname"]) {
		$domain = $params["serverhostname"];
	}
	else {
		$domain = $params["serverip"];
	}

	$code = "<form action=\"" . $http . "://" . $domain . ":" . $whmport . "/login/\" method=\"post\" target=\"_blank\"><input type=\"hidden\" name=\"user\" value=\"" . $params["serverusername"] . "\" /><input type=\"hidden\" name=\"pass\" value=\"" . $params["serverpassword"] . "\" /><input type=\"submit\" value=\"WHM\" /></form>";
	return $code;
}


function VistaPanel_costrrpl($val) {
	$val = str_replace( "MB", "", $val );
	$val = str_replace( "Accounts", "", $val );
	$val = $val = trim( $val );

	if ($val == "Yes") {
		$val = "on";
	}
	else {
		if ($val == "No") {
			$val = "";
		}
		else {
			if ($val == "Unlimited") {
				$val = "unlimited";
			}
		}
	}

	return $val;
}


function VistaPanel_CreateAccount($params) {

	if ($params["configoption6"]) {
		$dedicatedip = "y";
	}
	else {
		$dedicatedip = "n";
	}


	if ($params["configoption9"]) {
		$cgiaccess = "y";
	}
	else {
		$cgiaccess = "n";
	}


	if ($params["configoption7"]) {
		$shellaccess = "y";
	}
	else {
		$shellaccess = "n";
	}


	if ($params["configoption11"]) {
		$fpextensions = "y";
	}
	else {
		$fpextensions = "n";
	}


	if ($params["configoption22"]) {
		$prefix = $params["serverusername"] . "_";
	}
	else {
		$prefix = "";
	}

	$postfields = array();
	$postfields["username"] = $params["username"];
	$postfields["password"] = $params["password"];
	$postfields["domain"] = $params["domain"];
	$postfields["plan"] = $prefix . $params["configoption1"];
	$postfields["savepkg"] = 0;
	$postfields["featurelist"] = "default";
	$postfields["quota"] = $params["configoption3"];
	$postfields["bwlimit"] = $params["configoption5"];
	$postfields["ip"] = $dedicatedip;
	$postfields["cgi"] = $cgiaccess;
	$postfields["frontpage"] = $fpextensions;
	$postfields["hasshell"] = $shellaccess;
	$postfields["contactemail"] = $params["clientsdetails"]["email"];
	$postfields["cpmod"] = $params["configoption13"];
	$postfields["maxftp"] = $params["configoption2"];
	$postfields["maxsql"] = $params["configoption8"];
	$postfields["maxpop"] = $params["configoption4"];

	if ($mailinglists) {
		$postfields["maxlst"] = $mailinglists;
	}

	$postfields["maxsub"] = $params["configoption10"];
	$postfields["maxpark"] = $params["configoption12"];
	$postfields["maxaddon"] = $params["configoption14"];

	if ($languageco) {
		$postfields["language"] = $languageco;
	}

	$postfields["reseller"] = 0;
	$VistaPanelrequest = "/xml-api/createacct?";
	foreach ($postfields as $k => $v) {
		$VistaPanelrequest .= "" . $k . "=" . urlencode( $v ) . "&";
	}

	$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

	if (!is_array( $output )) {
		return $output;
	}


	if (!$output["CREATEACCT"]["RESULT"]["STATUS"]) {
		$error = $output["CREATEACCT"]["RESULT"]["STATUSMSG"];

		if (!$error) {
			$error = "出现未知错误";
		}

		return $error;
	}


	if ($dedicatedip == "y") {
		$newaccountip = $output["CREATEACCT"]["RESULT"]["OPTIONS"]["IP"];
		update_query( "服务", array( "专用ip" => $newaccountip ), array( "id" => $params["serviceid"] ) );
	}


	if ($params["type"] == "reselleraccount") {
		$makeowner = ($params["configoption24"] ? 1 : 0);
		$VistaPanelrequest = "/xml-api/setupreseller?user=" . $params["username"] . "&makeowner=" . $makeowner;
		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

		if (!is_array( $output )) {
			return $output;
		}


		if (!$output["SETUPRESELLER"]["RESULT"]["STATUS"]) {
			$error = $output["SETUPRESELLER"]["RESULT"]["STATUSMSG"];

			if (!$error) {
				$error = "出现未知错误";
			}

			return $error;
		}

		$VistaPanelrequest = "/xml-api/setresellerlimits?user=" . $params["username"];

		if ($params["configoption16"]) {
			$VistaPanelrequest .= "&enable_resource_limits=1&diskspace_limit=" . urlencode( $params["configoption17"] ) . "&bandwidth_limit=" . urlencode( $params["configoption18"] );

			if ($params["configoption19"]) {
				$VistaPanelrequest .= "&enable_overselling_diskspace=1";
			}


			if ($params["configoption20"]) {
				$VistaPanelrequest .= "&enable_overselling_bandwidth=1";
			}
		}


		if ($params["configoption15"]) {
			$VistaPanelrequest .= "&enable_account_limit=1&account_limit=" . urlencode( $params["configoption15"] );
		}

		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

		if (!is_array( $output )) {
			return $output;
		}


		if (!$output["SETRESELLERLIMITS"]["RESULT"]["STATUS"]) {
			$error = $output["SETRESELLERLIMITS"]["RESULT"]["STATUSMSG"];

			if (!$error) {
				$error = "出现未知错误";
			}

			return $error;
		}

		$VistaPanelrequest = "/xml-api/setacls?reseller=" . $params["username"] . "&acllist=" . urlencode( $params["configoption21"] );
		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

		if (!is_array( $output )) {
			return $output;
		}


		if (!$output["SETACLS"]["RESULT"]["STATUS"]) {
			$error = $output["SETACLS"]["RESULT"]["STATUSMSG"];

			if (!$error) {
				$error = "出现未知错误";
			}

			return $error;
		}


		if ($params["configoption23"]) {
			$VistaPanelrequest = "/xml-api/setresellernameservers?user=" . $params["username"] . "&nameservers=ns1." . $params["domain"] . ",ns2." . $params["domain"];
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

			if (!is_array( $output )) {
				return $output;
			}


			if (!$output["SETRESELLERNAMESERVERS"]["RESULT"]["STATUS"]) {
				$error = $output["SETRESELLERNAMESERVERS"]["RESULT"]["STATUSMSG"];

				if (!$error) {
					$error = "出现未知错误";
				}

				return $error;
			}
		}
	}

	return "成功";
}


function VistaPanel_SuspendAccount($params) {
	if (!$params["username"]) {
		return "无法执行,因为没有获取到帐户的用户名";
	}


	if ($params["type"] == "reselleraccount") {
		$VistaPanelrequest = "/scripts/suspendreseller?reseller=" . $params["username"] . "&resalso=1";
		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest, true );

		if (strpos( $output, "Curl Error" ) == true) {
			$result = $output;
		}
		else {
			if (strpos( $output, "<form action=\"/login/\" method=\"POST\">" ) == true) {
				$result = "登录失败";
			}
			else {
				if (strpos( $output, "account has been suspended" ) == true) {
					$result = "成功";
				}
				else {
					if (strpos( $output, "Account Already Suspended" ) == true) {
						$result = "帐户已经暂停";
					}
					else {
						if (strpos( $output, "You do not have permission to suspend that account" ) == true) {
							$result = "您没有权限暂停帐户";
						}
						else {
							$result = "出现未知错误";
						}
					}
				}
			}
		}

		return $result;
	}

	$VistaPanelrequest = "/xml-api/suspendacct?user=" . $params["username"] . "&reason=" . urlencode('Maturity has not been renewed');
	$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

	if (!is_array( $output )) {
		return $output;
	}


	if (!$output["SUSPENDACCT"]["RESULT"]["STATUS"]) {
		$error = $output["SUSPENDACCT"]["RESULT"]["STATUSMSG"];

		if (!$error) {
			$error = "出现未知错误";
		}

		return $error;
	}

	return "成功";
}


function VistaPanel_UnsuspendAccount($params) {
	if (!$params["username"]) {
		return "无法执行,因为没有获取到帐户的用户名";
	}


	if ($params["type"] == "reselleraccount") {
		$VistaPanelrequest = "/scripts/suspendreseller?reseller=" . $params["username"] . "&resalso=1&un=1";
		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest, true );

		if (strpos( $output, "Curl Error" ) == true) {
			$result = $output;
		}
		else {
			if (strpos( $output, "<form action=\"/login/\" method=\"POST\">" ) == true) {
				$result = "登录失败";
			}
			else {
				if (strpos( $output, "does not exist" ) == true) {
					$result = "帐户不存在";
				}
				else {
					if (strpos( $output, "Complete!" ) == true) {
						$result = "成功";
					}
					else {
						$result = "出现未知错误";
					}
				}
			}
		}

		return $result;
	}

	$VistaPanelrequest = "/xml-api/unsuspendacct?user=" . $params["username"];
	$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

	if (!is_array( $output )) {
		return $output;
	}


	if (!$output["UNSUSPENDACCT"]["RESULT"]["STATUS"]) {
		$error = $output["UNSUSPENDACCT"]["RESULT"]["STATUSMSG"];

		if (!$error) {
			$error = "出现未知错误";
		}

		return $error;
	}

	return "成功";
}


function VistaPanel_TerminateAccount($params) {
	if (!$params["username"]) {
		return "无法执行,因为没有获取到帐户的用户名";
	}


	if ($params["type"] == "reselleraccount") {
		$VistaPanelrequest = "/xml-api/terminatereseller?reseller=" . $params["username"] . "&terminatereseller=1&verify=I%20understand%20this%20will%20irrevocably%20remove%20all%20the%20accounts%20owned%20by%20the%20reseller%20" . $params["username"];
		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

		if (!is_array( $output )) {
			return $output;
		}


		if (!$output["TERMINATERESELLER"]["RESULT"]["STATUS"]) {
			$error = $output["TERMINATERESELLER"]["RESULT"]["STATUSMSG"];

			if (!$error) {
				$error = "出现未知错误";
			}

			return $error;
		}
	}
	else {
		$VistaPanelrequest = "/xml-api/removeacct?user=" . $params["username"];
		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

		if (!is_array( $output )) {
			return $output;
		}


		if (!$output["REMOVEACCT"]["RESULT"]["STATUS"]) {
			$error = $output["REMOVEACCT"]["RESULT"]["STATUSMSG"];

			if (!$error) {
				$error = "出现未知错误";
			}

			return $error;
		}
	}

	return "成功";
}


function VistaPanel_ChangePassword($params) {
	$VistaPanelrequest = "/xml-api/passwd?user=" . $params["username"] . "&pass=" . urlencode( $params["password"] );
	$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

	if (!is_array( $output )) {
		return $output;
	}


	if (!$output["PASSWD"]["PASSWD1"]["STATUS"]) {
		$error = $output["PASSWD"]["PASSWD1"]["STATUSMSG"];

		if (!$error) {
			$error = "出现未知错误";
		}

		return $error;
	}

	return "成功";
}


function VistaPanel_ChangePackage($params) {

	if ($params["configoption22"]) {
		$prefix = $params["serverusername"] . "_";
	}
	else {
		$prefix = "";
	}

	$VistaPanelrequest = "/xml-api/listresellers";
	$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest, true );
	$parser = xml_parser_create();
	xml_parser_set_option( $parser, XML_OPTION_CASE_FOLDING, 0 );
	xml_parser_set_option( $parser, XML_OPTION_SKIP_WHITE, 1 );
	xml_parse_into_struct( $parser, $output, $xml_values );
	xml_parser_free( $parser );
	$rusernames = array();
	foreach ($xml_values as $vals) {

		if ($vals["tag"] == "reseller") {
			$rusernames[] = $vals["value"];
			continue;
		}
	}


	if ($params["type"] == "reselleraccount") {
		if (!in_array( $params["username"], $rusernames )) {
			$makeowner = ($params["configoption24"] ? 1 : 0);
			$VistaPanelrequest = "/xml-api/setupreseller?user=" . $params["username"] . "&makeowner=" . $makeowner;
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

			if (!is_array( $output )) {
				return $output;
			}


			if (!$output["SETUPRESELLER"]["RESULT"]["STATUS"]) {
				$error = $output["SETUPRESELLER"]["RESULT"]["STATUSMSG"];

				if (!$error) {
					$error = "出现未知错误";
				}

				return $error;
			}
		}


		if ($params["configoption21"]) {
			$VistaPanelrequest = "/xml-api/setacls?reseller=" . $params["username"] . "&acllist=" . urlencode( $params["configoption21"] );
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

			if (!is_array( $output )) {
				return $output;
			}


			if (!$output["SETACLS"]["RESULT"]["STATUS"]) {
				$error = $output["SETACLS"]["RESULT"]["STATUSMSG"];

				if (!$error) {
					$error = "出现未知错误";
				}

				return $error;
			}
		}

		$VistaPanelrequest = "/xml-api/setresellerlimits?user=" . $params["username"];

		if ($params["configoption16"]) {
			$VistaPanelrequest .= "&enable_resource_limits=1&diskspace_limit=" . urlencode( $params["configoption17"] ) . "&bandwidth_limit=" . urlencode( $params["configoption18"] );

			if ($params["configoption19"]) {
				$VistaPanelrequest .= "&enable_overselling_diskspace=1";
			}


			if ($params["configoption20"]) {
				$VistaPanelrequest .= "&enable_overselling_bandwidth=1";
			}
		}
		else {
			$VistaPanelrequest .= "&enable_resource_limits=0";
		}


		if ($params["configoption15"]) {
			if ($params["configoption15"] == "unlimited") {
				$VistaPanelrequest .= "&enable_account_limit=1&account_limit=";
			}
			else {
				$VistaPanelrequest .= "&enable_account_limit=1&account_limit=" . urlencode( $params["configoption15"] );
			}
		}
		else {
			$VistaPanelrequest .= "&enable_account_limit=0&account_limit=";
		}

		$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

		if (!is_array( $output )) {
			return $output;
		}


		if (!$output["SETRESELLERLIMITS"]["RESULT"]["STATUS"]) {
			$error = $output["SETRESELLERLIMITS"]["RESULT"]["STATUSMSG"];

			if (!$error) {
				$error = "出现未知错误";
			}

			return $error;
		}


		if ($params["configoption21"]) {
			$VistaPanelrequest = "/xml-api/setacls?reseller=" . $params["username"] . "&acllist=" . $params["configoption21"];
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

			if (!is_array( $output )) {
				return $output;
			}


			if (!$output["SETACLS"]["RESULT"]["STATUS"]) {
				$error = $output["SETACLS"]["RESULT"]["STATUSMSG"];

				if (!$error) {
					$error = "出现未知错误";
				}

				return $error;
			}
		}
	}
	else {
		if (in_array( $params["username"], $rusernames )) {
			$VistaPanelrequest = "/xml-api/unsetupreseller?user=" . $params["username"];
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );
		}


		if ($params["configoption1"] != "Custom") {
			$VistaPanelrequest = "/xml-api/changepackage?user=" . $params["username"] . "&pkg=" . urlencode( $prefix . $params["configoption1"] );
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $VistaPanelrequest );

			if (!is_array( $output )) {
				return $output;
			}


			if (!$output["CHANGEPACKAGE"]["RESULT"]["STATUS"]) {
				$error = $output["CHANGEPACKAGE"]["RESULT"]["STATUSMSG"];

				if (!$error) {
					$error = "出现未知错误";
				}

				return $error;
			}
		}
	}


		if (isset( $params["configoption3"] )) {
			$params["configoption3"] = cpanel_costrrpl( $params["configoption3"] );
			$cpanelrequest = "/scripts/editquota?user=" . $params["username"] . "&quota=" . $params["configoption3"] . "";
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $cpanelrequest, true );
		}


		if (isset( $params["configoption5"] )) {
			$params["configoption5"] = cpanel_costrrpl( $params["configoption5"] );
			$cpanelrequest = "/scripts2/dolimitbw?user=" . $params["username"] . "&bwlimit=" . $params["configoption5"] . "";
			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $cpanelrequest, true );
		}

		$cpanelrequest = "";

		if (isset( $params["configoption2"] )) {
			$params["configoption2"] = cpanel_costrrpl( $params["configoption2"] );
			$cpanelrequest .= "MAXFTP=" . $params["configoption2"] . "&";
		}


		if (isset( $params["configoption4"] )) {
			$params["configoption4"] = cpanel_costrrpl( $params["configoption4"] );
			$cpanelrequest .= "MAXPOP=" . $params["configoption4"] . "&";
		}


		if (isset( $params["configoption8"] )) {
			$params["configoption8"] = cpanel_costrrpl( $params["configoption8"] );
			$cpanelrequest .= "MAXSQL=" . $params["configoption8"] . "&";
		}


		if (isset( $params["configoption10"] )) {
			$params["configoption10"] = cpanel_costrrpl( $params["configoption10"] );
			$cpanelrequest .= "MAXSUB=" . $params["configoption10"] . "&";
		}


		if (isset( $params["configoption12"] )) {
			$params["configoption12"] = cpanel_costrrpl( $params["configoption12"] );
			$cpanelrequest .= "MAXPARK=" . $params["configoption12"] . "&";
		}


		if (isset( $params["configoption14"] )) {
			$params["configoption14"] = cpanel_costrrpl( $params["configoption14"] );
			$cpanelrequest .= "MAXADDON=" . $params["configoption14"] . "&";
		}


		if (isset( $params["configoption9"] )) {
			$params["configoption9"] = cpanel_costrrpl( $params["configoption9"] );
			$cpanelrequest .= "HASCGI=" . $params["configoption9"] . "&";
		}


		if (isset( $params["configoption7"] )) {
			$params["configoption7"] = cpanel_costrrpl( $params["configoption7"] );
			$cpanelrequest .= "shell=" . $params["configoption7"] . "&";
		}


		if ($cpanelrequest) {
			$cpanelrequest = "/xml-api/modifyacct?user=" . $params["username"] . "&domain=" . $params["domain"] . "&" . $cpanelrequest;

			if ($params["configoption13"]) {
				$cpanelrequest .= "CPTHEME=" . $params["configoption13"];
			}

			$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $cpanelrequest );
		}


		if (isset( $params["configoption6"] )) {
			$params["configoption6"] = cpanel_costrrpl( $params["configoption6"] );

			if ($params["configoption6"]) {
				$currentip = "";
				$alreadydedi = false;
				$cpanelrequest = "/xml-api/accountsummary?user=" . $params["username"];
				$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $cpanelrequest );
				$currentip = $output["ACCOUNTSUMMARY"]["ACCT"]["IP"];
				$cpanelrequest = "/xml-api/listips";
				$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $cpanelrequest );
				foreach ($output["LISTIPS"] as $result) {

					if (( $result["IP"] == $currentip && $result["MAINADDR"] != "1" )) {
						$alreadydedi = true;
						continue;
					}
				}


				if (!$alreadydedi) {
					foreach ($output["LISTIPS"] as $result) {
						$active = $result["ACTIVE"];
						$dedicated = $result["DEDICATED"];
						$ipaddr = $result["IP"];
						$used = $result["USED"];

						if (( ( $active && $dedicated ) && !$used )) {
							break;
							continue;
						}
					}

					$cpanelrequest = "/xml-api/setsiteip?user=" . $params["username"] . "&ip=" . $ipaddr;
					$output = VistaPanel_req( $params["serversecure"], $params["serverip"], $params["serverusername"], $params["serverpassword"], $params["serveraccesshash"], $cpanelrequest );

					if ($output["SETSITEIP"]["RESULT"]["STATUS"]) {
						update_query( "服务", array( "专用ip" => $ipaddr ), array( "id" => $params["serviceid"] ) );
					}
				}
			}
		}
	
	
	return "成功";
}


function VistaPanel_LoginLink($params) {
	if ($params["serversecure"]) {
		$whmport = "2087";
		$http = "https";
	}
	else {
		$whmport = "2086";
		$http = "http";
	}


	if ($params["serverhostname"]) {
		$domain = $params["serverhostname"];
	}
	else {
		$domain = $params["serverip"];
	}

	$code = "<a href=\"" . $http . "://" . $domain . ":" . $whmport . "/xferVistaPanel/" . $params["username"] . "\" target=\"_blank\" class=\"moduleloginlink\">login to control panel</a>";
	return $code;
}






function VistaPanel_req($usessl, $host, $user, $pass, $accesshash, $request, $notxml = "") {
	
	$cleanaccesshash = preg_replace( "'(
|
)'", "", $accesshash );

	if ($cleanaccesshash) {
		$authstr = "WHM " . $user . ":" . $cleanaccesshash;
	}
	else {
		$authstr = "Basic " . base64_encode( $user . ":" . $pass );
	}

	$results = array();
	$ch = curl_init();

	if ($usessl) {
		curl_setopt( $ch, CURLOPT_SSL_VERIFYPEER, 0 );
		curl_setopt( $ch, CURLOPT_SSL_VERIFYHOST, 0 );
		curl_setopt( $ch, CURLOPT_URL, "https://" . $host . ":2087" . $request );
	}
	else {
		curl_setopt( $ch, CURLOPT_URL, "http://" . $host . ":2086" . $request );
	}

	curl_setopt( $ch, CURLOPT_HEADER, 0 );
	curl_setopt( $ch, CURLOPT_RETURNTRANSFER, 1 );
	$curlheaders[0] = "Authorization: " . $authstr;
	curl_setopt( $ch, CURLOPT_HTTPHEADER, $curlheaders );
	curl_setopt( $ch, CURLOPT_TIMEOUT, 400 );
	$data = curl_exec( $ch );

	if (curl_errno( $ch )) {
		$results = "(Curl Error) " . curl_error( $ch ) . " - code: " . curl_errno( $ch ) . "";
	}
	else {
		if ($notxml) {
			$results = $data;
		}
		else {
			if (strpos( $data, "Brute Force Protection" ) == true) {
				$results = "WHM施加了强力保护块协助联系的cPanel";
			}
			else {
				if (strpos( $data, "<form action=\"/login/\" method=\"POST\">" ) == true) {
					$results = "登录失败";
				}
				else {
					if (strpos( $data, "SSL encryption is required" ) == true) {
						$results = "SSL需要登录";
					}
					else {
						if (substr( $data, 0, 1 ) != "<") {
							$data = substr( $data, strpos( $data, "<" ) );
						}

						$results = XMLtoArray( $data );

						if ($results["VistaPanelRESULT"]["DATA"]["REASON"] == "Access denied") {
							$results = "登录失败";
						}
					}
				}
			}
		}
	}

	curl_close( $ch );
	$action = explode( "?", $request );
	$action = $action[0];
	$action = str_replace( "/xml-api/", "", $action );
	unset( $data );
	return $results;
}

function VistaPanel_ClientAreaLIB($goods,&$server) {
$VistaPanel['登陆地址']=$goods['配置选项25'];
$VistaPanel['用户名']=$server['用户名'];
$VistaPanel['密码']=$server['密码'];
$server['主机名']='';
$server['ip地址']='';
TEMPLATE::assign('VistaPanel',$VistaPanel);
}

add_swap_plug('产品控制面板详情','VistaPanel_ClientAreaLIB');
?>