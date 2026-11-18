// Package driver MEmu（逍遥安卓模拟器）驱动生命周期实现（memuc 命令行）。
//
// 与 Python 版 HostServer/MemuAndroid.py 语义对齐，并在实测基础上补齐：
//
//  1. 虚拟机标识统一为 "memu_<index>"（Python 约定），调用 memuc 时剥离前缀；
//  2. 创建后从 memuc 输出的 "index:N" 回读索引并重命名，不再遍历 map 猜；
//  3. memuc 的输出是 Windows ANSI（简体中文环境为 GBK），必须转码，
//     否则实例标题会写成乱码；
//  4. memuc 的 start/stop/remove 失败时多数只打印 "ERROR: ..." 且退出码仍为 0，
//     必须解析输出，否则会出现「接口返回成功、模拟器其实没启动」；
//  5. 新建实例目录与基础镜像目录缺少 Hyper-V 虚拟机账户（S-1-5-83-0）的 ACL 时，
//     启动会以 0x80070005 失败（memuhv.log 记 H8），启动前自动修复并重试；
//  6. 安卓版本号与版本名称双向映射，创建时校验本机镜像是否存在。
package driver

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/model"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/paths"
	"github.com/OpenIDCSTeam/HostAgent-Go/internal/security"
)

// memuKeyPrefix 虚拟机标识前缀（与 Python 版 f"memu_{index}" 一致）
const memuKeyPrefix = "memu_"

// memuImageLabels memuc 安卓版本号 → 展示名称。
//
// memuc create 支持 44 / 51 / 71 / 76 / 90 / 96 / 120 七个版本，
// 名称同时作为「操作系统」下拉项与实例窗口标题。
var memuImageLabels = map[string]string{
	"44":  "Android 4.4",
	"51":  "Android 5.1",
	"71":  "Android 7.1",
	"76":  "Android 7.1 (64-bit)",
	"90":  "Android 9",
	"96":  "Android 9 (64-bit)",
	"120": "Android 12",
}

// memucIndexRe 从 "index:3" 形式的输出中提取索引
var memucIndexRe = regexp.MustCompile(`(?im)^\s*index\s*[:=]\s*(\d+)\s*$`)

// memuImageCodeRe 从实例磁盘文件名（MEmu71-2024...-disk2.vmdk）中提取安卓版本号
var memuImageCodeRe = regexp.MustCompile(`(?i)^MEmu(\d+)-\d+FFF-disk\d+\.vmdk$`)

// decodeMemucOutput 把 memuc 的控制台输出转为 UTF-8。
//
// memuc.exe 使用 Windows ANSI 代码页输出，简体中文系统为 GBK；
// 直接按 UTF-8 解码会得到「ҵ»》ģ»」这类乱码并写进数据库。
func decodeMemucOutput(raw string) string {
	if raw == "" {
		return raw
	}
	b := []byte(raw)
	if utf8.Valid(b) {
		return raw
	}
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), b)
	if err != nil {
		return raw
	}
	return string(out)
}

// memuOutputError 从命令输出中提取错误行（memuc 失败时退出码可能仍为 0）
func memuOutputError(out string) string {
	for _, line := range splitLines(out) {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		lower := strings.ToLower(l)
		if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "failed") ||
			strings.HasPrefix(lower, "fail ") || strings.HasPrefix(lower, "failure") {
			return l
		}
		// adb shell 的失败信息（如 `/system/bin/sh: locksettings: not found`）
		if strings.Contains(lower, "not found") || strings.Contains(lower, "permission denied") ||
			strings.Contains(lower, "no such file or directory") ||
			strings.HasPrefix(lower, "/system/bin/sh:") {
			return l
		}
	}
	return ""
}

// memuRootOf 返回 Microvirt 数据目录（即 MEmu 目录的父目录）。
func memuRootOf(cfg *model.HSConfig) string {
	if cfg != nil {
		launch := strings.TrimSpace(cfg.LaunchPath)
		if launch != "" {
			if info, err := os.Stat(launch); err == nil {
				if info.IsDir() {
					if strings.EqualFold(filepath.Base(launch), "MEmu") {
						return filepath.Dir(launch)
					}
					return launch
				}
				// launch_path 直接指向 memuc.exe
				return filepath.Dir(filepath.Dir(launch))
			}
		}
	}
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		return filepath.Join(appData, "Microvirt")
	}
	return ""
}

// memuExeDir 返回 memuc.exe 所在目录
func (d *MemuDriver) memuExeDir() string {
	launch := strings.TrimSpace(d.Config.LaunchPath)
	if launch != "" {
		if info, err := os.Stat(launch); err == nil {
			if info.IsDir() {
				return launch
			}
			return filepath.Dir(launch)
		}
	}
	root := memuRootOf(d.Config)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "MEmu")
}

// memucBin 解析 memuc 可执行文件路径（优先取 launch_path 配置，与 Python 一致）
func (d *MemuDriver) memucBin() string {
	dir := d.memuExeDir()
	if dir != "" {
		for _, name := range []string{"memuc.exe", "memuc"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return "memuc"
}

// execMemuc 执行 memuc 命令（原始输出，未转码）
func (d *MemuDriver) execMemuc(args ...string) (string, error) {
	full := append([]string{d.memucBin()}, args...)
	out, _, err := security.SafeExecWithTimeout(full, 2*time.Minute)
	return out, err
}

// runMemuc 执行 memuc 命令：转码输出、统一识别 "ERROR:" 文本失败。
func (d *MemuDriver) runMemuc(args ...string) (string, error) {
	raw, err := d.execMemuc(args...)
	out := decodeMemucOutput(raw)
	if err != nil {
		if msg := memuOutputError(out); msg != "" {
			return out, errors.New(msg)
		}
		return out, err
	}
	if msg := memuOutputError(out); msg != "" {
		return out, errors.New(msg)
	}
	return out, nil
}

// runAdb 通过 memuc adb 执行设备命令。
//
// adb 成功时会先打印 "already connected to 127.0.0.1:21503"，不能当作错误；
// 设备侧报错则以 "error:" 开头，需要识别。
func (d *MemuDriver) runAdb(vmIndex string, args ...string) (string, error) {
	full := append([]string{"adb", "-i", vmIndex, "--"}, args...)
	return d.runMemuc(full...)
}

// adb 保留旧调用形式（截图等实现按 (string, error) 使用）
func (d *MemuDriver) adb(vmIndex string, args ...string) (string, error) {
	return d.runAdb(vmIndex, args...)
}

// extractIndex 从 vm_uuid（memu_N 或纯数字 N）提取纯数字索引
func extractMemuIndex(vmUUID string) string {
	if strings.HasPrefix(vmUUID, memuKeyPrefix) {
		return vmUUID[len(memuKeyPrefix):]
	}
	return vmUUID
}

// vmIndex 返回某虚拟机对应的 memuc 索引
func (d *MemuDriver) vmIndex(vmUUID string) string {
	return extractMemuIndex(vmUUID)
}

// instanceDir 返回实例目录：索引 0 对应 "MEmu"，其余为 "MEmu_<index>"。
func (d *MemuDriver) instanceDir(index string) string {
	root := memuRootOf(d.Config)
	if root == "" {
		return ""
	}
	name := "MEmu"
	if index != "0" {
		name = "MEmu_" + index
	}
	return filepath.Join(root, "MEmu", "MemuHyperv VMs", name)
}

// imageDir 返回指定安卓版本的基础镜像目录
func (d *MemuDriver) imageDir(code string) string {
	root := memuRootOf(d.Config)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "MEmu", "image", code)
}

// memuInstance memuc listvms 条目
type memuInstance struct {
	Index   string
	Title   string
	Running bool
	PID     string
}

// listInstances 解析 memuc listvms 输出
// 格式: index,title,top-level window handle,attach window handle,pid[,disk usage]
//
// 第 4 列为「是否已启动」（0/1），第 5 列为进程 PID；
// 旧实现只取前两列，导致每次查询都要额外执行 isvmrunning。
func (d *MemuDriver) listInstances() []memuInstance {
	out, err := d.runMemuc("listvms")
	if err != nil {
		return nil
	}
	return parseMemuListvms(out)
}

// parseMemuListvms 解析 listvms 文本（独立成函数便于测试）
func parseMemuListvms(out string) []memuInstance {
	var result []memuInstance
	for _, line := range splitLines(out) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "index") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		inst := memuInstance{
			Index: strings.TrimSpace(fields[0]),
			Title: strings.TrimSpace(fields[1]),
		}
		if len(fields) >= 4 {
			inst.Running = strings.TrimSpace(fields[3]) == "1"
		}
		if len(fields) >= 5 {
			inst.PID = strings.TrimSpace(fields[4])
		}
		if inst.Index == "" {
			continue
		}
		result = append(result, inst)
	}
	return result
}

// GetPower 通过 memuc isvmrunning 获取模拟器状态
func (d *MemuDriver) GetPower(vmUUID string) string {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return "未知"
	}
	out, err := d.runMemuc("isvmrunning", "-i", idx)
	if err != nil {
		return "未知"
	}
	text := strings.ToLower(out)
	switch {
	case strings.Contains(text, "not running"):
		return "已关机"
	case strings.Contains(text, "running"):
		return "运行中"
	default:
		return "未知"
	}
}

// memuPowerFlag 将 MEmu 状态文本转为枚举
func memuPowerFlag(status string) model.VMPowers {
	switch status {
	case "运行中", "Running", "running":
		return model.VMPowerStarted
	case "已关机", "已关闭", "Stopped", "stopped":
		return model.VMPowerStopped
	default:
		return model.VMPowerUnknown
	}
}

// memuIsRunningText 判断 isvmrunning 输出是否为「运行中」。
//
// 注意 "Not Running" 也包含 "running"，必须先排除否定形式，
// 否则等待启动的轮询会立刻误判成功。
func memuIsRunningText(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(s, "running") && !strings.Contains(s, "not running")
}

// waitMemuRunning 等待实例进入 Running 状态。
//
// memuc start -b 会立即返回，核心进程随后才启动；镜像首次启动或权限异常时
// 可能永远起不来，只根据命令退出码判断会误报成功。
func (d *MemuDriver) waitMemuRunning(index string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if memuIsRunningText(mustMemuText(d.execMemuc("isvmrunning", "-i", index))) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Second)
	}
}

// waitMemuStopped 等待实例退出运行
func (d *MemuDriver) waitMemuStopped(index string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		out := strings.ToLower(mustMemuText(d.execMemuc("isvmrunning", "-i", index)))
		if strings.Contains(out, "not running") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Second)
	}
}

// mustMemuText 返回转码后的命令输出（忽略错误）
func mustMemuText(out string, _ error) string {
	return decodeMemucOutput(out)
}

// instanceConfig 读取实例的 CPU / 内存配置（失败时返回 0）。
//
// memuc 的取值语法是 `getconfigex <key> -i <index>`，
// 顺序写反只会得到 "No value set!"，实例规格会一直显示默认值。
func (d *MemuDriver) instanceConfig(index string) (cpus int, memory int) {
	read := func(key string) int {
		out, err := d.runMemuc("getconfigex", key, "-i", index)
		if err != nil {
			return 0
		}
		return parseMemucValue(out)
	}
	return read("cpus"), read("memory")
}

// parseMemucValue 解析 "Value: 3" 形式的输出
func parseMemucValue(out string) int {
	line := firstNonEmptyLine(out)
	if i := strings.LastIndex(line, ":"); i >= 0 {
		line = strings.TrimSpace(line[i+1:])
	}
	n, _ := strconv.Atoi(line)
	return n
}

// firstNonEmptyLine 返回输出中第一行非空内容
func firstNonEmptyLine(s string) string {
	for _, line := range splitLines(s) {
		if v := strings.TrimSpace(line); v != "" {
			return v
		}
	}
	return ""
}

// instanceAndroidCode 尝试从实例目录的磁盘文件名推断安卓版本号
func (d *MemuDriver) instanceAndroidCode(index string) string {
	dir := d.instanceDir(index)
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if m := memuImageCodeRe.FindStringSubmatch(e.Name()); m != nil {
			return m[1]
		}
	}
	return ""
}

// memuAndroidCode 把系统名/版本号映射为 memuc 的安卓版本号。
//
// 支持三种输入：下拉项名称（Android 7.1）、版本号（71）、
// 以及历史数据里的 android7.1 / android7.1_64 形式。
func memuAndroidCode(osName string) string {
	key := strings.ToLower(strings.TrimSpace(osName))
	key = strings.ReplaceAll(key, " ", "")
	key = strings.ReplaceAll(key, "（", "(")
	key = strings.ReplaceAll(key, "）", ")")

	if _, ok := memuImageLabels[key]; ok {
		return key
	}
	for code, label := range memuImageLabels {
		if strings.EqualFold(strings.ReplaceAll(label, " ", ""), key) {
			return code
		}
	}
	alias := map[string]string{
		"android4.4":      "44",
		"android5.1":      "51",
		"android7.1":      "71",
		"android7.1_64":   "76",
		"android7.164":    "76",
		"android7.1(64位)": "76",
		"android9(64位)":   "96",
		"android9":        "90",
		"android9_64":     "96",
		"android964":      "96",
		"android12":       "120",
		"44":              "44",
		"51":              "51",
		"71":              "71",
		"76":              "76",
		"90":              "90",
		"96":              "96",
		"120":             "120",
	}
	if v, ok := alias[key]; ok {
		return v
	}
	return ""
}

// availableImages 返回本机已安装的安卓版本号（image/<code> 下存在 vmdk）。
func (d *MemuDriver) availableImages() []string {
	var codes []string
	for code := range memuImageLabels {
		dir := d.imageDir(code)
		if dir == "" {
			continue
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "*.vmdk")); len(matches) > 0 {
			codes = append(codes, code)
		}
	}
	// 按版本号数值排序，保证下拉项顺序稳定
	for i := 0; i < len(codes); i++ {
		for j := i + 1; j < len(codes); j++ {
			a, _ := strconv.Atoi(codes[i])
			b, _ := strconv.Atoi(codes[j])
			if b < a {
				codes[i], codes[j] = codes[j], codes[i]
			}
		}
	}
	return codes
}

// GetOSImages 返回本机可创建的安卓版本（名称 → 版本号）。
//
// MEmu 的「系统镜像」不是配置文件里的 ISO，而是安装目录 image/<code> 下的
// 基础磁盘；不覆盖此方法时创建页面的系统下拉框为空，无法创建实例。
func (d *MemuDriver) GetOSImages() (map[string]interface{}, error) {
	result := make(map[string]interface{})
	for _, code := range d.availableImages() {
		result[memuImageLabels[code]] = code
	}
	if len(result) == 0 {
		return result, fmt.Errorf("未在 %s 下找到可用的安卓镜像", d.imageDir("*"))
	}
	return result, nil
}

// GetVMList 获取 Android 实例列表（键为 memu_<index>）
func (d *MemuDriver) GetVMList() (map[string]*model.VMConfig, error) {
	result := d.GetVMSaving()

	for _, inst := range d.listInstances() {
		key := memuKeyPrefix + inst.Index
		label, fromImage := d.instanceLabel(inst)
		if cfg, ok := result[key]; ok {
			cfg.VmFlag = memuPowerFlag(powerFromRunning(inst.Running))
			if fromImage || strings.TrimSpace(cfg.OsName) == "" {
				cfg.OsName = label
			}
		} else {
			cfg := model.NewVMConfig()
			cfg.VmUUID = key
			cfg.OsName = label
			cfg.VmFlag = memuPowerFlag(powerFromRunning(inst.Running))
			result[key] = cfg
		}
	}
	return result, nil
}

// powerFromRunning 把 listvms 的运行标志转成文本
func powerFromRunning(running bool) string {
	if running {
		return "运行中"
	}
	return "已关机"
}

// instanceLabel 返回实例展示名称：优先安卓版本，其次实例标题。
// instanceLabel 返回实例展示名称与来源：优先从实例磁盘文件名解析安卓版本，
// 解析不到时退回实例标题。fromImage 为 true 表示名称来自镜像（可信）。
func (d *MemuDriver) instanceLabel(inst memuInstance) (label string, fromImage bool) {
	if code := d.instanceAndroidCode(inst.Index); code != "" {
		if l, ok := memuImageLabels[code]; ok {
			return l, true
		}
	}
	return inst.Title, false
}

// ScanVMs 扫描并同步实例列表（按 filter_name 前缀过滤标题）
func (d *MemuDriver) ScanVMs() (*model.ZMessage, error) {
	instances := d.listInstances()
	prefix := d.Config.FilterName

	type scanItem struct {
		inst      memuInstance
		key       string
		label     string
		fromImage bool
		cpus      int
		mem       int
	}
	items := make([]scanItem, 0, len(instances))
	for _, inst := range instances {
		label, fromImage := d.instanceLabel(inst)
		if prefix != "" && !strings.HasPrefix(inst.Title, prefix) && !strings.HasPrefix(label, prefix) {
			continue
		}
		items = append(items, scanItem{inst: inst, key: memuKeyPrefix + inst.Index, label: label, fromImage: fromImage})
	}

	// 读取规格会产生外部命令调用，必须在锁外完成，避免阻塞其它虚拟机操作
	for i := range items {
		if _, ok := d.GetVMSaving()[items[i].key]; ok {
			continue
		}
		items[i].cpus, items[i].mem = d.instanceConfig(items[i].inst.Index)
	}

	d.mu.Lock()
	added := 0
	for _, item := range items {
		flag := memuPowerFlag(powerFromRunning(item.inst.Running))
		if cfg, ok := d.VmSaving[item.key]; ok {
			cfg.VmFlag = flag
			if item.fromImage || strings.TrimSpace(cfg.OsName) == "" {
				cfg.OsName = item.label
			}
			continue
		}
		cfg := model.NewVMConfig()
		cfg.VmUUID = item.key
		cfg.OsName = item.label
		cfg.VmFlag = flag
		// 首次纳管时读取实例规格，避免列表里 CPU/内存恒为默认值
		if item.cpus > 0 {
			cfg.CpuNum = item.cpus
		}
		if item.mem > 0 {
			cfg.MemNum = item.mem
		}
		d.VmSaving[item.key] = cfg
		added++
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMDetect",
		Message: fmt.Sprintf("扫描完成：共 %d 个，新增 %d 个", len(items), added)}, nil
}

// PowerControl 电源控制
func (d *MemuDriver) PowerControl(vmUUID string, action model.VMPowers) (*model.ZMessage, error) {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "VMPowers", Message: "模拟器不存在"}, nil
	}

	switch action {
	case model.VMPowerSStart:
		return d.startVM(idx, vmUUID), nil
	case model.VMPowerSClose:
		return d.stopVM(idx, vmUUID, true), nil
	case model.VMPowerHClose:
		return d.stopVM(idx, vmUUID, false), nil
	case model.VMPowerSReset, model.VMPowerHReset:
		return d.rebootVM(idx, vmUUID), nil
	case model.VMPowerAPause, model.VMPowerAWaked:
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: "逍遥模拟器不支持暂停/恢复操作"}, nil
	default:
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("不支持的电源操作: %s", action)}, nil
	}
}

// startAndWait 启动实例并等待其进入 Running。
//
// memuc start 是异步的：关机后立刻 start 偶尔会因磁盘尚未释放而静默失败，
// 因此先等 8 秒确认核心进程已起来，未起来则补发一次。
func (d *MemuDriver) startAndWait(index string, timeout time.Duration) (string, error) {
	out, err := d.runMemuc("start", "-i", index, "-b")
	if err != nil {
		valid, _ := d.runMemuc("isvmrunning", "-i", index)
		if !memuIsRunningText(valid) {
			return out, wrapMemuStartError(err)
		}
	}
	if d.waitMemuRunning(index, 8*time.Second) {
		return out, nil
	}

	time.Sleep(2 * time.Second)
	retryOut, retryErr := d.runMemuc("start", "-i", index, "-b")
	if retryErr == nil && d.waitMemuRunning(index, timeout) {
		return retryOut, nil
	}
	if memuIsRunningText(mustMemuText(d.execMemuc("isvmrunning", "-i", index))) {
		return retryOut, nil
	}
	if retryErr != nil {
		return retryOut, wrapMemuStartError(retryErr)
	}
	return retryOut, fmt.Errorf("%s", d.startFailureDetail(index, retryOut))
}

// wrapMemuStartError 为 exit status 5 补充可操作的排查提示。
//
// MEmu 需要创建窗口，Windows 服务（Session 0）或受限沙箱里运行 HostAgent 时，
// memuc start 会直接以退出码 5 失败且不输出任何内容。
func wrapMemuStartError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "exit status 5") {
		return fmt.Errorf("%w（exit 5：MEmu 需要登录用户的交互式桌面会话，请勿以 Windows 服务方式运行 HostAgent）", err)
	}
	return err
}

// startVM 启动实例，并在 Hyper-V 权限异常时自动修复后重试。
func (d *MemuDriver) startVM(idx, vmUUID string) *model.ZMessage {
	if d.GetPower(vmUUID) == "运行中" {
		return &model.ZMessage{Success: true, Action: "VMPowers", Message: "模拟器已在运行"}
	}

	_, err := d.startAndWait(idx, 90*time.Second)

	if err != nil {
		// H8（0x80070005）缺 ACL、H0（0x800706D9）防火墙服务未启动：
		// 完整修复一次后重试，避免用户必须手动运行修复脚本。
		if repaired := d.repairHyperVForStart(idx); repaired {
			if _, retryErr := d.startAndWait(idx, 90*time.Second); retryErr == nil {
				d.setVMPower(vmUUID, model.VMPowerStarted)
				return &model.ZMessage{Success: true, Action: "VMPowers",
					Message: "模拟器已启动（已自动修复 Hyper-V 运行环境）"}
			} else {
				err = retryErr
			}
		}
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("启动失败：%v", err)}
	}

	d.setVMPower(vmUUID, model.VMPowerStarted)
	return &model.ZMessage{Success: true, Action: "VMPowers", Message: "模拟器已启动"}
}

// stopVM 关闭实例。
//
// 逍遥模拟器没有可靠的「软关机」通道：adb reboot -p 只会重启 Android，
// 模拟器核心进程仍在，而且命令可能长时间不返回。因此软/强关统一走
// memuc stop，并在返回前确认实例确实已停止。
func (d *MemuDriver) stopVM(idx, vmUUID string, soft bool) *model.ZMessage {
	if d.GetPower(vmUUID) == "已关机" {
		return &model.ZMessage{Success: true, Action: "VMPowers", Message: "模拟器已关机"}
	}

	if _, err := d.runMemuc("stop", "-i", idx); err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("关机失败：%v", err)}
	}
	if !d.waitMemuStopped(idx, 30*time.Second) {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: "关机超时：模拟器仍在运行"}
	}

	d.setVMPower(vmUUID, model.VMPowerStopped)
	return &model.ZMessage{Success: true, Action: "VMPowers", Message: "模拟器已关机"}
}

// rebootVM 重启实例
func (d *MemuDriver) rebootVM(idx, vmUUID string) *model.ZMessage {
	out, err := d.runMemuc("reboot", "-i", idx)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMPowers",
			Message: fmt.Sprintf("重启失败：%v", err)}
	}
	// reboot 后核心进程可能短暂退出，等待其重新就绪
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if memuIsRunningText(mustMemuText(d.execMemuc("isvmrunning", "-i", idx))) {
			d.setVMPower(vmUUID, model.VMPowerStarted)
			return &model.ZMessage{Success: true, Action: "VMPowers", Message: "模拟器已重启"}
		}
		time.Sleep(2 * time.Second)
	}
	return &model.ZMessage{Success: false, Action: "VMPowers",
		Message: fmt.Sprintf("重启超时，模拟器未恢复运行：%s", firstNonEmptyLine(out))}
}

// setVMPower 更新驱动内存中的电源状态（由 API 层负责落库）
func (d *MemuDriver) setVMPower(vmUUID string, power model.VMPowers) {
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.VmFlag = power
	}
	d.mu.Unlock()
}

// startFailureDetail 组装启动失败原因：memuc 输出 + memuhv.log 最后一条错误
func (d *MemuDriver) startFailureDetail(index string, out string) string {
	msg := "模拟器核心进程未启动"
	if e := memuOutputError(out); e != "" {
		msg = e
	} else if line := firstNonEmptyLine(out); line != "" {
		msg = line
	}
	if detail := d.lastHVError(index); detail != "" {
		msg += "；" + detail
	}
	return msg
}

// lastHVError 读取 memuhv.log 中最后一条 Hyper-V 错误
func (d *MemuDriver) lastHVError(index string) string {
	dir := d.instanceDir(index)
	if dir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, "memuhv.log"))
	if err != nil {
		return ""
	}
	text := decodeMemucOutput(string(raw))
	lines := splitLines(text)
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-40; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.Contains(line, "0x80070005") {
			return "Hyper-V 拒绝访问虚拟磁盘（0x80070005），请执行「修复运行环境」"
		}
		if strings.HasPrefix(line, "H8") || strings.HasPrefix(line, "H0") {
			if strings.Contains(line, "0x800706D9") {
				return "Windows 防火墙服务未运行，Hyper-V 无法创建虚拟网络"
			}
			return "Hyper-V 启动错误，请执行「修复运行环境」"
		}
	}
	return ""
}

// CreateVM 创建 Android 实例
func (d *MemuDriver) CreateVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	code := memuAndroidCode(cfg.OsName)
	if code == "" {
		// 未选择版本时按本机已安装镜像兜底
		images := d.availableImages()
		if len(images) == 0 {
			return &model.ZMessage{Success: false, Action: "VMCreate",
				Message: "本机未安装任何安卓镜像，请先在逍遥模拟器中下载镜像"}, nil
		}
		code = images[len(images)-1]
	}
	if matches, _ := filepath.Glob(filepath.Join(d.imageDir(code), "*.vmdk")); len(matches) == 0 {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("本机未安装 %s（image/%s）镜像", memuImageLabels[code], code)}, nil
	}
	label := memuImageLabels[code]

	out, err := d.runMemuc("create", code)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("创建模拟器失败：%v", err)}, nil
	}

	newIndex := ""
	if m := memucIndexRe.FindStringSubmatch(out); m != nil {
		newIndex = m[1]
	}
	if newIndex == "" {
		// 兼容 "index:3" 之外的输出：取当前列表里最大的索引（新建实例一定是最大）
		newIndex = d.maxInstanceIndex()
	}
	if newIndex == "" {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: "无法获取新建模拟器索引"}, nil
	}

	// CPU / 内存
	if cfg.CpuNum > 0 {
		_, _ = d.runMemuc("setconfigex", "-i", newIndex, "cpus", strconv.Itoa(cfg.CpuNum))
	}
	if cfg.MemNum > 0 {
		_, _ = d.runMemuc("setconfigex", "-i", newIndex, "memory", strconv.Itoa(cfg.MemNum))
	}
	// 主机级扩展参数（与 Python 从主机 extend_data 读取一致）
	renderSet := false
	if d.Config != nil && d.Config.ExtendData != nil {
		for _, k := range []string{"graphics_render_mode", "enable_su", "enable_audio", "fps"} {
			if v, ok := d.Config.ExtendData[k]; ok && v != nil && fmt.Sprintf("%v", v) != "" {
				_, _ = d.runMemuc("setconfigex", "-i", newIndex, k, fmt.Sprintf("%v", v))
				if k == "graphics_render_mode" {
					renderSet = true
				}
			}
		}
	}
	// 默认 DirectX 渲染（1）：部分 NVIDIA 驱动下 OpenGL（0）会让
	// adb screencap 返回整屏白色，截图与 VNC 控制台都只剩白屏。
	if !renderSet {
		_, _ = d.runMemuc("setconfigex", "-i", newIndex, "graphics_render_mode", "1")
	}
	// 重命名为安卓版本名（与 Python 版 rename(title) 一致）
	if _, err := d.runMemuc("rename", "-i", newIndex, label); err != nil {
		// 重命名失败不影响实例可用性，记录到结果里
		_ = err
	}

	// 新建实例目录与基础镜像目录必须补 Hyper-V 虚拟机账户 ACL，否则启动即 0x80070005
	d.ensureVMDiskACL(newIndex)

	startErr := error(nil)
	if _, startErr = d.startAndWait(newIndex, 120*time.Second); startErr != nil {
		// 首次启动最常见的原因是 Hyper-V 权限/防火墙未就绪
		if repaired := d.repairHyperVForStart(newIndex); repaired {
			if _, retryErr := d.startAndWait(newIndex, 120*time.Second); retryErr == nil {
				startErr = nil
			} else {
				startErr = retryErr
			}
		}
	}
	if startErr != nil {
		return &model.ZMessage{Success: false, Action: "VMCreate",
			Message: fmt.Sprintf("模拟器已创建（索引 %s），但启动失败：%v", newIndex, startErr)}, nil
	}

	key := memuKeyPrefix + newIndex
	cfg.VmUUID = key
	cfg.OsName = label
	cfg.VmFlag = model.VMPowerStarted
	d.mu.Lock()
	d.VmSaving[key] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMCreate", Message: "模拟器创建成功",
		Results: map[string]interface{}{"vm_uuid": key, "index": newIndex}}, nil
}

// repairHyperVForStart 启动失败时执行一次完整修复，返回是否值得重试。
func (d *MemuDriver) repairHyperVForStart(index string) bool {
	// 先补本实例相关目录，成本低；再跑完整检查（防火墙 + 所有目录）。
	aclFixed := 0
	if rep := d.ensureVMDiskACL(index); rep != nil {
		aclFixed = rep.Fixed
	}
	rep := d.repairRuntime(true)
	if rep == nil {
		return aclFixed > 0
	}
	return aclFixed > 0 || rep.Fixed > 0 || strings.Contains(rep.Firewall, "已启动")
}

// maxInstanceIndex 返回当前实例列表中最大的数字索引
func (d *MemuDriver) maxInstanceIndex() string {
	max := -1
	for _, inst := range d.listInstances() {
		n, err := strconv.Atoi(inst.Index)
		if err == nil && n > max {
			max = n
		}
	}
	if max < 0 {
		return ""
	}
	return strconv.Itoa(max)
}

// DeleteVM 删除 Android 实例
func (d *MemuDriver) DeleteVM(vmUUID string, removeBackup bool) (*model.ZMessage, error) {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "VMDelete", Message: "模拟器不存在"}, nil
	}

	if d.GetPower(vmUUID) == "运行中" {
		_, _ = d.runMemuc("stop", "-i", idx)
		d.waitMemuStopped(idx, 30*time.Second)
	}

	if _, err := d.runMemuc("remove", "-i", idx); err != nil {
		return &model.ZMessage{Success: false, Action: "VMDelete",
			Message: fmt.Sprintf("删除失败：%v", err)}, nil
	}

	d.mu.Lock()
	delete(d.VmSaving, vmUUID)
	d.mu.Unlock()
	d.closeVNCServer(idx)

	return &model.ZMessage{Success: true, Action: "VMDelete", Message: "模拟器已删除"}, nil
}

// UpdateVM 更新实例配置
func (d *MemuDriver) UpdateVM(cfg *model.VMConfig, oldCfg *model.VMConfig) (*model.ZMessage, error) {
	idx := d.vmIndex(cfg.VmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "VMUpdate", Message: "模拟器不存在"}, nil
	}

	// 前端提交的 os_name 是镜像下拉项的 sys_file（可能是版本号），
	// 重命名与落库统一成可读的版本名称。
	if code := memuAndroidCode(cfg.OsName); code != "" {
		cfg.OsName = memuImageLabels[code]
	}

	cpuChanged := cfg.CpuNum != oldCfg.CpuNum && cfg.CpuNum > 0
	memChanged := cfg.MemNum != oldCfg.MemNum && cfg.MemNum > 0
	nameChanged := strings.TrimSpace(cfg.OsName) != "" && cfg.OsName != oldCfg.OsName

	// 规格变更必须关机后写入；仅改名称可热更新
	wasRunning := d.GetPower(cfg.VmUUID) == "运行中"
	if (cpuChanged || memChanged) && wasRunning {
		_, _ = d.runMemuc("stop", "-i", idx)
		if !d.waitMemuStopped(idx, 30*time.Second) {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: "修改配置前关闭模拟器失败"}, nil
		}
		// 等核心进程完全退出并释放 vmdk，否则紧接着 start 可能静默失败
		time.Sleep(2 * time.Second)
	}

	if cpuChanged {
		if _, err := d.runMemuc("setconfigex", "-i", idx, "cpus", strconv.Itoa(cfg.CpuNum)); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("设置 CPU 失败：%v", err)}, nil
		}
	}
	if memChanged {
		if _, err := d.runMemuc("setconfigex", "-i", idx, "memory", strconv.Itoa(cfg.MemNum)); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("设置内存失败：%v", err)}, nil
		}
	}
	if nameChanged {
		if _, err := d.runMemuc("rename", "-i", idx, cfg.OsName); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("重命名失败：%v", err)}, nil
		}
	}

	if wasRunning {
		if _, err := d.startAndWait(idx, 120*time.Second); err != nil {
			return &model.ZMessage{Success: false, Action: "VMUpdate",
				Message: fmt.Sprintf("配置已保存，但重新启动失败：%v", err)}, nil
		}
	}

	d.mu.Lock()
	if wasRunning {
		cfg.VmFlag = model.VMPowerStarted
	} else if cfg.VmFlag == model.VMPowerUnknown {
		cfg.VmFlag = model.VMPowerStopped
	}
	d.VmSaving[cfg.VmUUID] = cfg
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMUpdate", Message: "模拟器配置更新成功"}, nil
}

// ReinstallVM 重装实例（删除后重建）
func (d *MemuDriver) ReinstallVM(cfg *model.VMConfig) (*model.ZMessage, error) {
	idx := d.vmIndex(cfg.VmUUID)
	if idx != "" {
		if d.GetPower(cfg.VmUUID) == "运行中" {
			_, _ = d.runMemuc("stop", "-i", idx)
			d.waitMemuStopped(idx, 30*time.Second)
		}
		if _, err := d.runMemuc("remove", "-i", idx); err != nil {
			return &model.ZMessage{Success: false, Action: "VMSetups",
				Message: fmt.Sprintf("删除旧实例失败：%v", err)}, nil
		}
		d.mu.Lock()
		delete(d.VmSaving, cfg.VmUUID)
		d.mu.Unlock()
		d.closeVNCServer(idx)
	}
	return d.CreateVM(cfg)
}

// CreateBackup 创建备份（memuc export 导出 OVA）
func (d *MemuDriver) CreateBackup(vmUUID string, tips string) (*model.ZMessage, error) {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "VMBackup", Message: "模拟器不存在"}, nil
	}
	// memuc export 对运行中的实例会一直等待，必须要求先关机
	if d.GetPower(vmUUID) == "运行中" {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: "请先关闭模拟器再备份（memuc 不支持导出运行中的实例）"}, nil
	}

	backupDir := strings.TrimSpace(d.Config.BackupPath)
	if backupDir == "" {
		backupDir = filepath.Join(paths.Temp(), "backups")
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: "创建备份目录失败: " + err.Error()}, nil
	}

	fileName := fmt.Sprintf("%s_%s.ova", vmUUID, time.Now().Format("20060102_150405"))
	backupFile := filepath.Join(backupDir, fileName)

	if _, err := d.runMemuc("export", "-i", idx, backupFile); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: fmt.Sprintf("备份失败：%v", err)}, nil
	}
	if _, err := os.Stat(backupFile); err != nil {
		return &model.ZMessage{Success: false, Action: "VMBackup",
			Message: "备份命令已执行，但未生成备份文件（请先关闭模拟器再备份）"}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.Backups = append(cfg.Backups, model.VMBackup{
			BackupName: fileName, BackupTime: time.Now(), BackupTips: tips,
		})
	}
	d.mu.Unlock()

	return &model.ZMessage{Success: true, Action: "VMBackup",
		Message: "备份成功: " + fileName,
		Results: map[string]interface{}{"backup_name": fileName}}, nil
}

// RestoreBackup 还原备份（memuc import 导入 OVA）
func (d *MemuDriver) RestoreBackup(vmUUID string, backupName string) (*model.ZMessage, error) {
	backupDir := strings.TrimSpace(d.Config.BackupPath)
	if backupDir == "" {
		backupDir = filepath.Join(paths.Temp(), "backups")
	}
	name, err := sanitizeFileName(backupName)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "Restores", Message: "备份名称无效"}, nil
	}
	backupFile := filepath.Join(backupDir, name)
	if _, statErr := os.Stat(backupFile); statErr != nil {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: "备份文件不存在: " + name}, nil
	}

	if _, err := d.runMemuc("import", backupFile); err != nil {
		return &model.ZMessage{Success: false, Action: "Restores",
			Message: fmt.Sprintf("恢复失败：%v", err)}, nil
	}
	return &model.ZMessage{Success: true, Action: "Restores",
		Message: "恢复成功，请重新扫描模拟器"}, nil
}

// MountHDD 逍遥模拟器不支持挂载额外硬盘（与 Python 版一致）
func (d *MemuDriver) MountHDD(vmUUID string, hdd *model.SDConfig, mount bool) (*model.ZMessage, error) {
	return &model.ZMessage{Success: false, Action: "HDDMount",
		Message: "逍遥模拟器不支持挂载额外硬盘"}, nil
}

// MountISO 通过 ADB 安装 / 卸载 APK（替代 ISO 挂载，与 Python 版一致）
func (d *MemuDriver) MountISO(vmUUID string, iso *model.IMConfig, mount bool) (*model.ZMessage, error) {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "ISOMount", Message: "模拟器不存在"}, nil
	}

	if mount {
		apkPath := strings.TrimSpace(iso.IsoPath)
		if apkPath == "" {
			apkPath = filepath.Join(d.Config.DvdromPath, iso.IsoFile)
		}
		if _, err := os.Stat(apkPath); err != nil {
			return &model.ZMessage{Success: false, Action: "ISOMount",
				Message: "APK文件不存在: " + apkPath}, nil
		}
		// 优先用 memuc installapp（自带 UI 安装流程），失败再退回 adb install
		if _, err := d.runMemuc("installapp", "-i", idx, apkPath); err != nil {
			if out, adbErr := d.runAdb(idx, "install", "-r", apkPath); adbErr != nil {
				return &model.ZMessage{Success: false, Action: "ISOMount",
					Message: fmt.Sprintf("APK安装失败：%v (%s)", adbErr, firstNonEmptyLine(out))}, nil
			}
		}
		d.mu.Lock()
		if cfg, ok := d.VmSaving[vmUUID]; ok {
			if cfg.IsoAll == nil {
				cfg.IsoAll = make(map[string]model.IMConfig)
			}
			cfg.IsoAll[iso.IsoName] = *iso
		}
		d.mu.Unlock()
		return &model.ZMessage{Success: true, Action: "ISOMount", Message: "APK安装成功"}, nil
	}

	// 卸载：以 iso_name 作为包名
	pkg := iso.IsoName
	if _, err := d.runAdb(idx, "uninstall", pkg); err != nil {
		return &model.ZMessage{Success: false, Action: "ISOMount",
			Message: fmt.Sprintf("APK卸载失败：%v", err)}, nil
	}
	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		delete(cfg.IsoAll, iso.IsoName)
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "ISOMount", Message: "APK卸载成功"}, nil
}

// SetPassword 修改 Android 锁屏 PIN（ADB locksettings）
func (d *MemuDriver) SetPassword(vmUUID string, password string) (*model.ZMessage, error) {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "VMPasswd", Message: "模拟器不存在"}, nil
	}

	out, err := d.runAdb(idx, "shell", "locksettings", "set-pin", password)
	if err != nil {
		detail := memuOutputError(out)
		if detail == "" {
			detail = err.Error()
		}
		return &model.ZMessage{Success: false, Action: "VMPasswd",
			Message: fmt.Sprintf("密码修改失败（当前 Android 镜像不支持 locksettings）：%s", detail)}, nil
	}

	d.mu.Lock()
	if cfg, ok := d.VmSaving[vmUUID]; ok {
		cfg.OsPass = password
	}
	d.mu.Unlock()
	return &model.ZMessage{Success: true, Action: "VMPasswd", Message: "锁屏密码修改成功"}, nil
}

// GetVNCInfo 获取图形控制台信息。
//
// 逍遥模拟器没有内置 VNC 服务，这里由 HostAgent 内置一个 RFB 服务：
// 通过 ADB 抓帧推流、把鼠标/键盘事件转成 Android 触摸与按键。
// 返回 127.0.0.1:<动态端口>，前端 noVNC 经 6080 代理即可直接操作。
func (d *MemuDriver) GetVNCInfo(vmUUID string, ipAddr string) (*model.ZMessage, error) {
	idx := d.vmIndex(vmUUID)
	if idx == "" {
		return &model.ZMessage{Success: false, Action: "VNCGets", Message: "模拟器不存在"}, nil
	}

	srv, err := d.ensureVNCServer(idx)
	if err != nil {
		return &model.ZMessage{Success: false, Action: "VNCGets",
			Message: fmt.Sprintf("启动 VNC 服务失败：%v", err)}, nil
	}

	results := map[string]interface{}{
		"host": "127.0.0.1",
		"port": srv.Port(),
		"type": "vnc",
		// 同时给出 ADB 连接方式，便于脚本/调试直连
		"adb_port": 21503 + atoiSafe(idx)*10,
		"connect":  fmt.Sprintf("adb connect 127.0.0.1:%d", 21503+atoiSafe(idx)*10),
	}
	return &model.ZMessage{Success: true, Action: "VNCGets", Message: "VNC控制台信息获取成功",
		Results: results}, nil
}

// atoiSafe 字符串转整数，失败返回 0
func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// GetUSBList 列举已连接的 ADB 设备（与 Python 版 USBShows 一致）
func (d *MemuDriver) GetUSBList() (map[string]*model.USBInfos, error) {
	result := make(map[string]*model.USBInfos)
	out, err := d.runMemuc("adb", "--", "devices")
	if err != nil {
		return result, nil
	}
	for _, line := range splitLines(out) {
		if strings.HasPrefix(line, "List") || strings.HasPrefix(strings.ToLower(line), "already connected") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == "device" {
			serial := parts[0]
			result[serial] = &model.USBInfos{
				UsbKey:  serial,
				VidUUID: serial,
				UsbHint: "ADB设备: " + serial,
			}
		}
	}
	return result, nil
}

// GetPCIList 逍遥模拟器无 PCI 直通能力
func (d *MemuDriver) GetPCIList() (map[string]*model.VFConfig, error) {
	return make(map[string]*model.VFConfig), nil
}

// GetGPUList 逍遥模拟器无 GPU 直通能力
func (d *MemuDriver) GetGPUList() (map[string]*model.VFConfig, error) {
	return make(map[string]*model.VFConfig), nil
}

// SetupPCI 逍遥模拟器不支持 PCI 直通，明确拒绝
func (d *MemuDriver) SetupPCI(vmUUID string, cfg *model.VFConfig, pciKey string, add bool) (*model.ZMessage, error) {
	return &model.ZMessage{Success: false, Action: "PCISetup",
		Message: "逍遥模拟器不支持 PCI 设备直通"}, nil
}

// SetupUSB 逍遥模拟器 USB 直通请通过 ADB 管理（与 Python 版实现一致）
func (d *MemuDriver) SetupUSB(vmUUID string, info *model.USBInfos, usbKey string, add bool) (*model.ZMessage, error) {
	return &model.ZMessage{Success: false, Action: "USBSetup",
		Message: "逍遥模拟器USB直通请通过ADB管理"}, nil
}

// MountUSB 逍遥模拟器不支持 USB 存储挂载
func (d *MemuDriver) MountUSB(vmUUID string, usbKey string, mount bool) (*model.ZMessage, error) {
	return &model.ZMessage{Success: false, Action: "USBMount",
		Message: "逍遥模拟器不支持 USB 存储挂载"}, nil
}

// GetScreenshot 通过 ADB 截取模拟器屏幕
func (d *MemuDriver) GetScreenshot(vmUUID string) (string, error) {
	if _, ok := d.VmSaving[vmUUID]; !ok {
		return "", fmt.Errorf("模拟器不存在")
	}
	return d.captureMemuScreenshot(d.vmIndex(vmUUID))
}

// Crontab 定时任务：同步实例状态
func (d *MemuDriver) Crontab() bool {
	for _, inst := range d.listInstances() {
		key := memuKeyPrefix + inst.Index
		flag := memuPowerFlag(powerFromRunning(inst.Running))
		d.mu.Lock()
		if cfg, ok := d.VmSaving[key]; ok {
			cfg.VmFlag = flag
		}
		d.mu.Unlock()
	}
	return true
}

// Shutdown 关闭驱动时一并释放 VNC 监听端口
func (d *MemuDriver) Shutdown() {
	d.closeAllVNCServers()
	d.BaseDriver.Shutdown()
}

// captureMemuFrame 通过 ADB 抓取一帧 PNG 并解码。
//
// 使用 exec-out 直接从设备取回 PNG（无需先写 /sdcard 再 pull），
// 单帧约 1 秒；adb 会先打印 "already connected to ..."，需要跳过前缀。
func (d *MemuDriver) captureMemuFrame(vmIndex string) ([]byte, error) {
	raw, err := d.execMemuc("adb", "-i", vmIndex, "--", "exec-out", "screencap", "-p")
	if err != nil {
		return nil, err
	}
	idx := bytes.Index([]byte(raw), []byte("\x89PNG"))
	if idx < 0 {
		msg := firstNonEmptyLine(decodeMemucOutput(raw))
		if msg == "" {
			msg = "未返回图像数据"
		}
		return nil, errors.New(msg)
	}
	return []byte(raw)[idx:], nil
}
