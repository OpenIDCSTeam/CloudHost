// Package driver IPTablesAPI：基于 iptables（nat/filter 的 DOCKER 链）的端口映射管理。
//
// 对应 Python 版 HostServer/OCInterfaceAPI/IPTablesAPI.py。
// 用于 LXD 容器与 SmolVM（microVM）等以宿主机 iptables 做端口映射的场景。
//
// 每条映射固定下发三条规则（与 Python 一致，保证 docker/lxc 网络下可达）：
//  1. nat 表 DOCKER 链   ：DNAT 外部端口 → 容器地址:端口
//  2. filter 表 DOCKER 链：ACCEPT 目标为容器的流量
//  3. filter 表 FORWARD 链：ACCEPT 经 DNAT 后进入 FORWARD 的流量
//
// 注意：三条规则按顺序下发，任一失败都会回滚已下发的规则，避免出现半成品规则。
package driver

import (
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
)

// iptablesAPI iptables 端口映射管理器
type iptablesAPI struct {
	runner *hostCommandRunner
	config *model.HSConfig
}

// newIPTablesAPI 创建 iptables 管理器
func newIPTablesAPI(cfg *model.HSConfig) *iptablesAPI {
	return &iptablesAPI{
		runner: newHostCommandRunner(cfg),
		config: cfg,
	}
}

var reDPT = regexp.MustCompile(`dpt:(\d+)`)

// hostPorts 返回 nat 表 DOCKER 链中已 DNAT 的目标端口集合
func (i *iptablesAPI) hostPorts() map[int]bool {
	out, err := i.runner.runShell("iptables -t nat -L DOCKER -n", 30*time.Second)
	if err != nil {
		return map[int]bool{}
	}
	return parseIPTablesDPT(out)
}

// parseIPTablesDPT 解析 `iptables -t nat -L DOCKER -n` 输出中的 DNAT 目标端口。
//
// 行示例：
//
//	DNAT tcp -- 0.0.0.0/0 0.0.0.0/0 tcp dpt:2222 to:172.17.0.2:22
//
// 提取为纯函数以便单元测试（不依赖宿主机环境）。
func parseIPTablesDPT(out string) map[int]bool {
	ports := make(map[int]bool)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "dpt:") || !strings.Contains(line, "DNAT") {
			continue
		}
		if m := reDPT.FindStringSubmatch(line); m != nil {
			if p, convErr := strconv.Atoi(m[1]); convErr == nil && p > 0 {
				ports[p] = true
			}
		}
	}
	return ports
}

// allocatePort 在配置端口范围内随机分配一个未被 iptables 占用的端口
func (i *iptablesAPI) allocatePort() int {
	start, end := i.config.PortsStart, i.config.PortsClose
	if start <= 0 || end <= start {
		start, end = 10000, 60000
	}
	port := start + rand.Intn(end-start+1)
	existing := i.hostPorts()
	for n := 0; n < 100 && existing[port]; n++ {
		port = start + rand.Intn(end-start+1)
	}
	return port
}

// portComment 生成规则注释（与 Python 的 comment 格式一致）
func portComment(containerIP string, wanPort, lanPort int, vmName string) string {
	if vmName != "" {
		return fmt.Sprintf("%s-%d-%d_%s", containerIP, wanPort, lanPort, vmName)
	}
	return fmt.Sprintf("%s-%d-%d", containerIP, wanPort, lanPort)
}

// addPortMapping 添加端口映射（三条规则，失败自动回滚）
func (i *iptablesAPI) addPortMapping(containerIP string, lanPort, wanPort int, vmName string) error {
	if lanPort <= 0 {
		return fmt.Errorf("内网端口无效: %d", lanPort)
	}
	if strings.TrimSpace(containerIP) == "" {
		return fmt.Errorf("容器地址为空")
	}
	if wanPort <= 0 {
		wanPort = i.allocatePort()
	}
	comment := portComment(containerIP, wanPort, lanPort, vmName)

	// 1) nat DOCKER：DNAT
	if err := i.runner.runArgs("iptables", "-t", "nat", "-A", "DOCKER",
		"-p", "tcp", "--dport", strconv.Itoa(wanPort),
		"-m", "comment", "--comment", comment,
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", containerIP, lanPort)); err != nil {
		return fmt.Errorf("添加 DOCKER DNAT 规则失败: %w", err)
	}

	// 2) filter DOCKER：ACCEPT
	if err := i.runner.runArgs("iptables", "-A", "DOCKER",
		"-p", "tcp", "-d", containerIP,
		"--dport", strconv.Itoa(lanPort),
		"-m", "comment", "--comment", comment,
		"-j", "ACCEPT"); err != nil {
		_ = i.removePortMapping(containerIP, lanPort, wanPort)
		return fmt.Errorf("添加 DOCKER ACCEPT 规则失败: %w", err)
	}

	// 3) filter FORWARD：ACCEPT
	if err := i.runner.runArgs("iptables", "-I", "FORWARD",
		"-d", containerIP,
		"-p", "tcp", "--dport", strconv.Itoa(lanPort),
		"-m", "comment", "--comment", comment,
		"-j", "ACCEPT"); err != nil {
		_ = i.removePortMapping(containerIP, lanPort, wanPort)
		return fmt.Errorf("添加 FORWARD ACCEPT 规则失败: %w", err)
	}

	return nil
}

// removePortMapping 删除端口映射（三条规则，逐个删除，单条失败不阻断）
//
// 添加时规则携带 `-m comment --comment <ip>-<wan>-<lan>_<tips>`，而删除调用
// 拿不到 tips（nat_tips 属于虚拟机配置，随请求变化）。用完整规格 -D 会因
// comment 不匹配而「删除失败但返回成功」，规则长期残留并导致端口永远被占用。
// 这里改为按 `iptables -S` 的行号删除，与注释内容无关。
func (i *iptablesAPI) removePortMapping(containerIP string, lanPort, wanPort int) error {
	if strings.TrimSpace(containerIP) == "" || lanPort <= 0 || wanPort <= 0 {
		return fmt.Errorf("删除端口映射参数不完整")
	}

	// nat 表：精确匹配本端口 DNAT 到本容器的规则
	i.removeRuleByMatch("nat", "DOCKER", func(line string) bool {
		return strings.Contains(line, "--dport "+strconv.Itoa(wanPort)) &&
			strings.Contains(line, "-j DNAT") &&
			strings.Contains(line, "--to-destination "+containerIP+":"+strconv.Itoa(lanPort))
	})

	// filter DOCKER：目标容器的 ACCEPT
	i.removeRuleByMatch("", "DOCKER", func(line string) bool {
		return strings.Contains(line, "-d "+containerIP) &&
			strings.Contains(line, "--dport "+strconv.Itoa(lanPort)) &&
			strings.Contains(line, "-j ACCEPT")
	})

	// filter FORWARD：DNAT 后转发放行
	i.removeRuleByMatch("", "FORWARD", func(line string) bool {
		return strings.Contains(line, "-d "+containerIP) &&
			strings.Contains(line, "--dport "+strconv.Itoa(lanPort)) &&
			strings.Contains(line, "-j ACCEPT")
	})

	return nil
}

// removeRuleByMatch 在指定表的链中删除第一条匹配规则的记录。
//
// `iptables -D <chain> <num>` 的 num 是 1 起始的行号；这里循环删除直到
// 不存在匹配项，最多 4 轮（同一端口的多条历史残留一并清理）。
func (i *iptablesAPI) removeRuleByMatch(table, chain string, match func(string) bool) {
	for pass := 0; pass < 4; pass++ {
		cmd := "iptables "
		if table != "" {
			cmd += "-t " + table + " "
		}
		cmd += "-S " + chain
		out, err := i.runner.runShell(cmd, 30*time.Second)
		if err != nil {
			return
		}
		target := 0
		num := 0
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "-A "+chain+" ") {
				continue
			}
			num++
			if match(line) {
				target = num
				break
			}
		}
		if target == 0 {
			return
		}
		args := []string{"iptables"}
		if table != "" {
			args = append(args, "-t", table)
		}
		args = append(args, "-D", chain, strconv.Itoa(target))
		if err := i.runner.runArgs(args...); err != nil {
			log.Printf("[iptables] 删除 %s/%s 第 %d 条规则失败: %v", table, chain, target, err)
			return
		}
	}
}

// listPorts 以 PortForwardEntry 形式返回 iptables 中已有的映射（仅含端口信息）
func (i *iptablesAPI) listPorts() []PortForwardEntry {
	ports := i.hostPorts()
	entries := make([]PortForwardEntry, 0, len(ports))
	for p := range ports {
		entries = append(entries, PortForwardEntry{WanPort: p, Protocol: "TCP"})
	}
	return entries
}
