package task

import (
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

const defaultInputFile = "ip.txt"

var (
	// TestAll test all ip
	TestAll = false
	// IPFile is the filename of IP Rangs
	IPFile = defaultInputFile
	IPText string
)

func InitRandSeed() {
	rand.Seed(time.Now().UnixNano())
}

func isIPv4(ip string) bool {
	return strings.Contains(ip, ".")
}

func randIPEndWith(num byte) byte {
	if num == 0 { // 对于 /32 这种单独的 IP
		return byte(0)
	}
	return byte(rand.Intn(int(num)))
}

type IPRanges struct {
	ips     []*utils.IPAddr
	mask    string
	firstIP net.IP
	ipNet   *net.IPNet
	port    int // 当前正在解析的这一行所指定的端口（0 表示未指定，使用 -tp）
}

func newIPRanges() *IPRanges {
	return &IPRanges{
		ips: make([]*utils.IPAddr, 0),
	}
}

// 如果是单独 IP 则加上子网掩码，反之则获取子网掩码(r.mask)
func (r *IPRanges) fixIP(ip string) string {
	// 如果不含有 '/' 则代表不是 IP 段，而是一个单独的 IP，因此需要加上 /32 /128 子网掩码
	if i := strings.IndexByte(ip, '/'); i < 0 {
		if isIPv4(ip) {
			r.mask = "/32"
		} else {
			r.mask = "/128"
		}
		ip += r.mask
	} else {
		r.mask = ip[i:]
	}
	return ip
}

// 解析 IP 段，获得 IP、IP 范围、子网掩码
func (r *IPRanges) parseCIDR(ip string) {
	var err error
	if r.firstIP, r.ipNet, err = net.ParseCIDR(r.fixIP(ip)); err != nil {
		log.Fatalln("ParseCIDR err", err)
	}
}

func (r *IPRanges) appendIPv4(d byte) {
	r.appendIP(net.IPv4(r.firstIP[12], r.firstIP[13], r.firstIP[14], d))
}

func (r *IPRanges) appendIP(ip net.IP) {
	r.ips = append(r.ips, &utils.IPAddr{IP: ip, Port: r.port})
}

// 返回第四段 ip 的最小值及可用数目
func (r *IPRanges) getIPRange() (minIP, hosts byte) {
	minIP = r.firstIP[15] & r.ipNet.Mask[3] // IP 第四段最小值

	// 根据子网掩码获取主机数量
	m := net.IPv4Mask(255, 255, 255, 255)
	for i, v := range r.ipNet.Mask {
		m[i] ^= v
	}
	total, _ := strconv.ParseInt(m.String(), 16, 32) // 总可用 IP 数
	if total > 255 {                                 // 矫正 第四段 可用 IP 数
		hosts = 255
		return
	}
	hosts = byte(total)
	return
}

func (r *IPRanges) chooseIPv4() {
	if r.mask == "/32" { // 单个 IP 则无需随机，直接加入自身即可
		r.appendIP(r.firstIP)
	} else {
		minIP, hosts := r.getIPRange()    // 返回第四段 IP 的最小值及可用数目
		for r.ipNet.Contains(r.firstIP) { // 只要该 IP 没有超出 IP 网段范围，就继续循环随机
			if TestAll { // 如果是测速全部 IP
				for i := 0; i <= int(hosts); i++ { // 遍历 IP 最后一段最小值到最大值
					r.appendIPv4(byte(i) + minIP)
				}
			} else { // 随机 IP 的最后一段 0.0.0.X
				r.appendIPv4(minIP + randIPEndWith(hosts))
			}
			r.firstIP[14]++ // 0.0.(X+1).X
			if r.firstIP[14] == 0 {
				r.firstIP[13]++ // 0.(X+1).X.X
				if r.firstIP[13] == 0 {
					r.firstIP[12]++ // (X+1).X.X.X
				}
			}
		}
	}
}

func (r *IPRanges) chooseIPv6() {
	if r.mask == "/128" { // 单个 IP 则无需随机，直接加入自身即可
		r.appendIP(r.firstIP)
	} else {
		var tempIP uint8                  // 临时变量，用于记录前一位的值
		for r.ipNet.Contains(r.firstIP) { // 只要该 IP 没有超出 IP 网段范围，就继续循环随机
			r.firstIP[15] = randIPEndWith(255) // 随机 IP 的最后一段
			r.firstIP[14] = randIPEndWith(255) // 随机 IP 的最后一段

			targetIP := make([]byte, len(r.firstIP))
			copy(targetIP, r.firstIP)
			r.appendIP(targetIP) // 加入 IP 地址池

			for i := 13; i >= 0; i-- { // 从倒数第三位开始往前随机
				tempIP = r.firstIP[i]              // 保存前一位的值
				r.firstIP[i] += randIPEndWith(255) // 随机 0~255，加到当前位上
				if r.firstIP[i] >= tempIP {        // 如果当前位的值大于等于前一位的值，说明随机成功了，可以退出该循环
					break
				}
			}
		}
	}
}

// addEntry 把一个测速目标（IP + 可选端口）展开为待测速的 IP 列表。
// 单个 IP 直接加入；IP 段则按 随机一个 / 全部(-allip) 展开，端口沿用该行指定的端口。
func (r *IPRanges) addEntry(entry utils.IPAddr) {
	r.port = entry.Port
	// 用原始 CIDR 文本重新解析，以保留掩码信息
	r.parseCIDR(entry.IP.String())
	if isIPv4(entry.IP.String()) {
		r.chooseIPv4()
	} else {
		r.chooseIPv6()
	}
}

func loadIPRanges() []*utils.IPAddr {
	ranges := newIPRanges()

	if IPText != "" { // 从参数中获取 IP 段数据
		// 支持 -ip 1.1.1.1,2.2.2.2/24,2606:4700::/32 以及带端口的 1.1.1.1:8443
		for _, IP := range strings.Split(IPText, ",") {
			IP = strings.TrimSpace(IP) // 去除首尾的空白字符（空格、制表符、换行符等）
			if IP == "" {              // 跳过空的（即开头、结尾或连续多个 ,, 的情况）
				continue
			}
			if ip, port, ok := parseIPField(IP); ok {
				ranges.addEntry(utils.IPAddr{IP: ip, Port: port})
			} else {
				log.Fatalln("[-ip] 参数中存在无法解析的 IP：", IP)
			}
		}
		return ranges.ips
	}

	// 从文件中获取数据（兼容 ip.txt 单列 IP 段，以及含 ip/port 列的 CSV）
	if IPFile == "" {
		IPFile = defaultInputFile
	}
	content, err := os.ReadFile(IPFile)
	if err != nil {
		log.Fatal(err)
	}

	// 逐个测速目标展开（单个 IP 或 IP 段，端口随目标走）
	for _, entry := range parseIPList(string(content)) {
		ranges.addEntry(entry)
	}

	if len(ranges.ips) == 0 {
		log.Fatalf("[%s] 中没有解析到任何可用的 IP，请检查文件格式（支持 ip.txt 单列 IP 段，或含 ip、port 列的 CSV）。\n", IPFile)
	}
	return ranges.ips
}
