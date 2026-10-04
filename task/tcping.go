package task

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

const (
	tcpConnectTimeout = time.Second * 1
	maxRoutine        = 1000
	defaultRoutines   = 200
	defaultPort       = 443
	defaultPingTimes  = 4
)

var (
	Routines      = defaultRoutines
	TCPPort   int = defaultPort
	PingTimes int = defaultPingTimes
)

type Ping struct {
	wg      *sync.WaitGroup
	m       *sync.Mutex
	ips     []*utils.IPAddr
	csv     utils.PingDelaySet
	control chan bool
	bar     *utils.Bar
}

func checkPingDefault() {
	if Routines <= 0 {
		Routines = defaultRoutines
	}
	// 注意：这里只做兜底，不再把非法值重置为 443。
	// 因为现在支持「每个测速目标自带端口」（CSV 的 port 列 / IP:端口 写法），
	// 全局 TCPPort 仅作为未指定端口的目标的回退值，允许随意设置。
	if TCPPort <= 0 || TCPPort >= 65536 {
		TCPPort = defaultPort
	}
	if PingTimes <= 0 {
		PingTimes = defaultPingTimes
	}
}

func NewPing() *Ping {
	checkPingDefault()
	ips := loadIPRanges()
	return &Ping{
		wg:      &sync.WaitGroup{},
		m:       &sync.Mutex{},
		ips:     ips,
		csv:     make(utils.PingDelaySet, 0),
		control: make(chan bool, Routines),
		bar:     utils.NewBar(len(ips), "可用:", ""),
	}
}

func (p *Ping) Run() utils.PingDelaySet {
	if len(p.ips) == 0 {
		return p.csv
	}
	if Httping {
		utils.Cyan.Printf("开始延迟测速（模式：HTTP, 端口：%s, 范围：%v ~ %v ms, 丢包：%.2f)\n", p.portSummary(), utils.InputMinDelay.Milliseconds(), utils.InputMaxDelay.Milliseconds(), utils.InputMaxLossRate)
	} else {
		utils.Cyan.Printf("开始延迟测速（模式：TCP, 端口：%s, 范围：%v ~ %v ms, 丢包：%.2f)\n", p.portSummary(), utils.InputMinDelay.Milliseconds(), utils.InputMaxDelay.Milliseconds(), utils.InputMaxLossRate)
	}
	for _, ip := range p.ips {
		p.wg.Add(1)
		p.control <- false
		go p.start(ip)
	}
	p.wg.Wait()
	p.bar.Done()
	sort.Sort(p.csv)
	return p.csv
}

func (p *Ping) start(ip *utils.IPAddr) {
	defer p.wg.Done()
	p.tcpingHandler(ip)
	<-p.control
}

// bool connectionSucceed float32 time
func (p *Ping) tcping(ip *utils.IPAddr) (bool, time.Duration) {
	startTime := time.Now()
	// 优先使用该目标自带的端口（来自 CSV 的 port 列或 IP:端口 写法），否则回退 [-tp]
	fullAddress := ip.Addr(portOf(ip))
	conn, err := net.DialTimeout("tcp", fullAddress, tcpConnectTimeout)
	if err != nil {
		return false, 0
	}
	duration := time.Since(startTime)
	// 测速只关心握手耗时，不需要优雅关闭。
	// 直接 Close 会让本地端口进入 60 秒 TIME_WAIT，大量候选 × 每个多次探测
	// 会耗尽临时端口池，之后所有连接都报 "can't assign requested address"。
	// SO_LINGER=0 让内核直接发 RST，端口立即可复用。
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
	return true, duration
}

// pingReceived pingTotalTime
func (p *Ping) checkConnection(ip *utils.IPAddr) (recv int, totalDelay time.Duration, colo string) {
	if Httping {
		recv, totalDelay, colo = p.httping(ip)
		return
	}
	colo = "" // TCPing 不获取 colo
	for i := 0; i < PingTimes; i++ {
		if ok, delay := p.tcping(ip); ok {
			recv++
			totalDelay += delay
		}
	}
	return
}

func (p *Ping) appendIPData(data *utils.PingData) {
	p.m.Lock()
	defer p.m.Unlock()
	p.csv = append(p.csv, utils.CloudflareIPData{
		PingData: data,
	})
}

// handle tcping
func (p *Ping) tcpingHandler(ip *utils.IPAddr) {
	recv, totalDlay, colo := p.checkConnection(ip)
	nowAble := len(p.csv)
	if recv != 0 {
		nowAble++
	}
	p.bar.Grow(1, strconv.Itoa(nowAble))
	if recv == 0 {
		return
	}
	data := &utils.PingData{
		IP:       ip,
		Port:     portOf(ip),
		Sended:   PingTimes,
		Received: recv,
		Delay:    totalDlay / time.Duration(recv),
		Colo:     colo,
	}
	p.appendIPData(data)
}

// portSummary 汇总本次测速实际会用到的端口，用于日志展示。
// 当测速目标各自带端口（CSV 的 port 列）时，会列出实际用到的端口集合。
func (p *Ping) portSummary() string {
	set := make(map[int]struct{})
	wholeRange := false
	for _, ip := range p.ips {
		if ip.Port > 0 {
			set[ip.Port] = struct{}{}
		} else {
			wholeRange = true
		}
	}
	if len(set) == 0 {
		return strconv.Itoa(TCPPort)
	}
	ports := make([]int, 0, len(set))
	for v := range set {
		ports = append(ports, v)
	}
	sort.Ints(ports)
	parts := make([]string, 0, len(ports))
	for _, v := range ports {
		parts = append(parts, strconv.Itoa(v))
	}
	summary := strings.Join(parts, ",")
	if wholeRange { // 存在没有指定端口的目标，它们会用 [-tp]
		summary += "(部分目标未指定端口，使用 -tp " + strconv.Itoa(TCPPort) + ")"
	}
	return summary
}

// portOf 返回某个测速目标实际要使用的端口：目标自带端口优先，否则回退全局 [-tp]
func portOf(ip *utils.IPAddr) int {
	if ip != nil && ip.Port > 0 {
		return ip.Port
	}
	return TCPPort
}
