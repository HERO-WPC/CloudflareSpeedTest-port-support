package utils

import (
	"encoding/csv"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"
)

const (
	defaultOutput         = "result.csv"
	maxDelay              = 9999 * time.Millisecond
	minDelay              = 0 * time.Millisecond
	maxLossRate   float32 = 1.0
)

var (
	InputMaxDelay    = maxDelay
	InputMinDelay    = minDelay
	InputMaxLossRate = maxLossRate
	Output           = defaultOutput
	PrintNum         = 10
	Debug            = false // 是否开启调试模式
)

// 是否打印测试结果
func NoPrintResult() bool {
	return PrintNum == 0
}

// 是否输出到文件
func noOutput() bool {
	return Output == "" || Output == " "
}

// IPAddr 表示一个「测速目标」：IP 地址 + 该目标要使用的端口。
//
// 端口为 0 表示「该行没有指定端口」，此时统一回退到命令行参数 [-tp]（默认 443）。
// 端口随目标走（而不是放在全局 map 里），因此同一个 IP 的不同端口会被当作
// 两个独立的目标分别测速，互不干扰。
type IPAddr struct {
	IP   net.IP
	Port int
}

// String 返回纯 IP 地址字符串（不含端口），保持与原有日志/输出格式兼容。
func (a *IPAddr) String() string {
	if a == nil || a.IP == nil {
		return ""
	}
	return a.IP.String()
}

// Addr 返回可直接用于拨号的地址（IPv4 为 ip:port，IPv6 为 [ip]:port）。
// port <= 0 时回退到默认端口 443。
func (a *IPAddr) Addr(port int) string {
	if port <= 0 {
		port = 443
	}
	return net.JoinHostPort(a.IP.String(), strconv.Itoa(port))
}

type PingData struct {
	IP       *IPAddr
	Sended   int
	Received int
	Delay    time.Duration
	Colo     string
	Port     int // 该目标实际使用的端口
}

type CloudflareIPData struct {
	*PingData
	lossRate      float32
	DownloadSpeed float64
}

// 计算丢包率
func (cf *CloudflareIPData) getLossRate() float32 {
	if cf.lossRate == 0 {
		pingLost := cf.Sended - cf.Received
		cf.lossRate = float32(pingLost) / float32(cf.Sended)
	}
	return cf.lossRate
}

// 输出端口：优先使用实际使用的端口，未记录时回退默认 443
func (cf *CloudflareIPData) port() int {
	if cf.Port > 0 {
		return cf.Port
	}
	return 443
}

// toString 输出 CSV 的一行：IP 地址,端口,已发送,...
// 端口放在 IP 地址之后，保证第 1 列仍然是纯 IP（兼容 3proxy/Hosts/DDNS 等脚本）
func (cf *CloudflareIPData) toString() []string {
	result := make([]string, 8)
	result[0] = cf.IP.String()
	result[1] = strconv.Itoa(cf.port())
	result[2] = strconv.Itoa(cf.Sended)
	result[3] = strconv.Itoa(cf.Received)
	result[4] = strconv.FormatFloat(float64(cf.getLossRate()), 'f', 2, 32)
	result[5] = strconv.FormatFloat(cf.Delay.Seconds()*1000, 'f', 2, 32)
	result[6] = strconv.FormatFloat(cf.DownloadSpeed/1024/1024, 'f', 2, 32)
	// 如果 Colo 为空，则使用 "N/A" 表示
	if cf.Colo == "" {
		result[7] = "N/A"
	} else {
		result[7] = cf.Colo
	}
	return result
}

func ExportCsv(data []CloudflareIPData) {
	if noOutput() || len(data) == 0 {
		return
	}
	fp, err := os.Create(Output)
	if err != nil {
		log.Fatalf("创建文件[%s]失败：%v", Output, err)
		return
	}
	defer fp.Close()
	w := csv.NewWriter(fp) //创建一个新的写入文件流
	_ = w.Write([]string{"IP 地址", "端口", "已发送", "已接收", "丢包率", "平均延迟", "下载速度(MB/s)", "地区码"})
	_ = w.WriteAll(convertToString(data))
	w.Flush()
}

func convertToString(data []CloudflareIPData) [][]string {
	result := make([][]string, 0)
	for _, v := range data {
		result = append(result, v.toString())
	}
	return result
}

// 延迟丢包排序
type PingDelaySet []CloudflareIPData

// 延迟条件过滤
func (s PingDelaySet) FilterDelay() (data PingDelaySet) {
	if InputMaxDelay > maxDelay || InputMinDelay < minDelay { // 当输入的延迟条件不在默认范围内时，不进行过滤
		return s
	}
	if InputMaxDelay == maxDelay && InputMinDelay == minDelay { // 当输入的延迟条件为默认值时，不进行过滤
		return s
	}
	for _, v := range s {
		if v.Delay > InputMaxDelay { // 平均延迟上限，延迟大于条件最大值时，后面的数据都不满足条件，直接跳出循环
			break
		}
		if v.Delay < InputMinDelay { // 平均延迟下限，延迟小于条件最小值时，不满足条件，跳过
			continue
		}
		data = append(data, v) // 延迟满足条件时，添加到新数组中
	}
	return
}

// 丢包条件过滤
func (s PingDelaySet) FilterLossRate() (data PingDelaySet) {
	if InputMaxLossRate >= maxLossRate { // 当输入的丢包条件为默认值时，不进行过滤
		return s
	}
	for _, v := range s {
		if v.getLossRate() > InputMaxLossRate { // 丢包几率上限
			break
		}
		data = append(data, v) // 丢包率满足条件时，添加到新数组中
	}
	return
}

func (s PingDelaySet) Len() int {
	return len(s)
}
func (s PingDelaySet) Less(i, j int) bool {
	iRate, jRate := s[i].getLossRate(), s[j].getLossRate()
	if iRate != jRate {
		return iRate < jRate
	}
	return s[i].Delay < s[j].Delay
}
func (s PingDelaySet) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// 下载速度排序
type DownloadSpeedSet []CloudflareIPData

func (s DownloadSpeedSet) Len() int {
	return len(s)
}
func (s DownloadSpeedSet) Less(i, j int) bool {
	return s[i].DownloadSpeed > s[j].DownloadSpeed
}
func (s DownloadSpeedSet) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s DownloadSpeedSet) Print() {
	if NoPrintResult() {
		return
	}
	if len(s) <= 0 { // IP数组长度(IP数量) 大于 0 时继续
		fmt.Println("\n[信息] 完整测速结果 IP 数量为 0，跳过输出结果。")
		return
	}
	dateString := convertToString(s) // 转为多维数组 [][]String
	if len(dateString) < PrintNum {  // 如果IP数组长度(IP数量) 小于  打印次数，则次数改为IP数量
		PrintNum = len(dateString)
	}
	headFormat := "%-16s%-7s%-5s%-5s%-5s%-6s%-12s%-5s\n"
	dataFormat := "%-18s%-9s%-8s%-8s%-8s%-10s%-16s%-8s\n"
	for i := 0; i < PrintNum; i++ { // 如果要输出的 IP 中包含 IPv6，那么就需要调整一下间隔
		if len(dateString[i][0]) > 15 {
			headFormat = "%-40s%-7s%-5s%-5s%-5s%-6s%-12s%-5s\n"
			dataFormat = "%-42s%-9s%-8s%-8s%-8s%-10s%-16s%-8s\n"
			break
		}
	}
	Cyan.Printf(headFormat, "IP 地址", "端口", "已发送", "已接收", "丢包率", "平均延迟", "下载速度(MB/s)", "地区码")
	for i := 0; i < PrintNum; i++ {
		fmt.Printf(dataFormat, dateString[i][0], dateString[i][1], dateString[i][2], dateString[i][3], dateString[i][4], dateString[i][5], dateString[i][6], dateString[i][7])
	}
	if !noOutput() {
		fmt.Printf("\n完整测速结果已写入 %v 文件，可使用记事本/表格软件查看。\n", Output)
	}
}
