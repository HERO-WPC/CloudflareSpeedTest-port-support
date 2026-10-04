package task

import (
	"net"
	"strconv"
	"strings"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

// 判断某个字段是否可作为「IP 列」的表头
func isIPHeaderField(field string) bool {
	f := strings.ToLower(strings.TrimSpace(field))
	switch f {
	case "ip", "ipaddr", "ipv4", "ipv6", "address", "addr",
		"ip地址", "地址", "ip 地址":
		return true
	}
	return false
}

// 判断某个字段是否可作为「端口列」的表头
func isPortHeaderField(field string) bool {
	f := strings.ToLower(strings.TrimSpace(field))
	switch f {
	case "port", "ports", "端口", "端口号", "目标端口", "反代端口":
		return true
	}
	return false
}

// 解析单个字段，尽量同时取出 IP 和端口。
//
// 支持：104.16.1.1 / 104.16.1.1:8443 / [2400:cb00::1]:8443 /
// 104.16.0.0/12（IP 段，端口由调用方补充）/ 104.16.0.0/12:8443。
// 取不出 IP 时返回 ok=false。
func parseIPField(field string) (ip net.IP, port int, ok bool) {
	s := strings.TrimSpace(field)
	if s == "" {
		return nil, 0, false
	}

	// 1. 含 CIDR 的写法（104.16.0.0/12 或 104.16.0.0/12:8443）：
	//    在第一个 '/' 处切开，右侧可能是 "12" 或 "12:8443"。
	if i := strings.IndexByte(s, '/'); i > 0 {
		base := strings.TrimSpace(s[:i])
		rest := strings.TrimSpace(s[i+1:])
		if p := net.ParseIP(base); p != nil && validCIDRMask(rest, p) {
			pv := 0
			// rest 形如 "12:8443" 时，冒号后面才是端口
			if j := strings.IndexByte(rest, ':'); j >= 0 {
				if v, err := strconv.Atoi(strings.TrimSpace(rest[j+1:])); err == nil && v > 0 && v <= 65535 {
					pv = v
				}
			}
			return p, pv, true
		}
	}

	// 2. 先按 host:port 解析（net.SplitHostPort 能正确处理 [IPv6]:port，
	//    且纯 IP 如 2400:cb00::1 会因为冒号过多而报错，不会误判）
	if host, portStr, err := net.SplitHostPort(s); err == nil {
		if p := net.ParseIP(strings.TrimSpace(host)); p != nil {
			if v, err := strconv.Atoi(strings.TrimSpace(portStr)); err == nil && v > 0 && v <= 65535 {
				return p, v, true
			}
		}
	}

	// 3. 没有端口：按纯 IP 解析（允许 [::1] 这种带方括号的残留写法）。
	//    含 '/' 的写法已在第 1 步处理过，走到这里说明掩码非法，直接判定为非法。
	if !strings.Contains(s, "/") {
		if p := net.ParseIP(normalizeIPText(s)); p != nil {
			return p, 0, true
		}
	}
	return nil, 0, false
}

// validCIDRMask 校验 '/' 后面的部分是否是一个合法的掩码（可带端口，如 "12:8443"）。
// 这样 1.1.1.1/abc、1.1.1.1/99 这类错误写法会被拒绝，而不会被当成合法 IP 蒙混过关。
func validCIDRMask(rest string, ip net.IP) bool {
	mask := rest
	if j := strings.IndexByte(mask, ':'); j >= 0 {
		mask = strings.TrimSpace(mask[:j])
	}
	v, err := strconv.Atoi(strings.TrimSpace(mask))
	if err != nil {
		return false
	}
	bits := 128
	if ip.To4() != nil {
		bits = 32
	}
	return v >= 0 && v <= bits
}

// normalizeIPText 把一段文本整理成「可以被 net.ParseIP 接受的纯 IP」：
// 去掉 CIDR 后缀（/12）、去掉可能残留的方括号（[::1]）。
func normalizeIPText(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

// 判断单字段是否是合法的端口值（1~65535）
func parsePortValue(field string) (int, bool) {
	v, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil || v <= 0 || v > 65535 {
		return 0, false
	}
	return v, true
}

// 判断单字段是否只包含一个 IP（不含端口），用于推断 CSV 的 IP 列
func isPlainIPField(field string) bool {
	s := normalizeIPText(field)
	if s == "" {
		return false
	}
	return net.ParseIP(s) != nil
}

// 去掉 UTF-8 BOM（Excel 导出的 CSV 常带）
func trimBOM(s string) string {
	return strings.TrimPrefix(s, "\ufeff")
}

// 探测分隔符：优先逗号（CSV），其次制表符、分号，最后退回空白
func detectDelimiter(lines []string) rune {
	for _, line := range lines {
		line = strings.TrimSpace(trimBOM(line))
		if line == "" {
			continue
		}
		switch {
		case strings.Contains(line, ","):
			return ','
		case strings.Contains(line, "\t"):
			return '\t'
		case strings.Contains(line, ";"):
			return ';'
		default:
			return 0 // 0 表示按任意空白切分
		}
	}
	return ','
}

// 按分隔符切分一行
func splitLine(line string, delim rune) []string {
	if delim == 0 {
		return strings.Fields(line)
	}
	return strings.Split(line, string(delim))
}

// ipColumn 描述从输入文件中发现的 IP / 端口列
type ipColumn struct {
	ip   int
	port int // -1 表示没有端口列
}

// 从文件内容中解析出所有测速目标。
// 解析策略（按顺序尝试，全部失败则返回空结果由调用方报错）：
//  1. 有表头：任意列名匹配 ip/IP地址 与 port/端口，按列取值（列顺序、列数量任意）；
//  2. 无表头多列：自动定位「只含 IP 的那一列」作为 IP 列，并把只含
//     1~65535 数字的列作为端口列（若该行没有端口列则端口留空，回退 -tp）；
//  3. 单列：每行一个 IP / IP 段 / IP:端口。
//
// 这样既兼容 CFST 自己导出的 result.csv（多列、含表头），
// 也兼容 ip.txt（无表头、单列 IP 段）。
func parseIPList(text string) []utils.IPAddr {
	var out []utils.IPAddr

	lines := strings.Split(trimBOM(text), "\n")
	delim := detectDelimiter(lines)

	// 先把每一行切成字段，跳过空行与注释行
	var rows [][]string
	for _, line := range lines {
		line = strings.TrimSpace(trimBOM(line))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := splitLine(line, delim)
		// 清理每个字段的首尾空白与引号
		for i, f := range fields {
			f = strings.TrimSpace(f)
			f = strings.Trim(f, `"'`)
			fields[i] = strings.TrimSpace(f)
		}
		rows = append(rows, fields)
	}
	if len(rows) == 0 {
		return out
	}

	col, startRow := locateColumns(rows, delim)

	for _, fields := range rows[startRow:] {
		if col.ip >= len(fields) {
			continue
		}
		ip, implicitPort, ok := parseIPField(fields[col.ip])
		if !ok {
			continue
		}
		port := 0
		if implicitPort > 0 {
			// IP 字段自身带了端口（如 1.1.1.1:8443）优先
			port = implicitPort
		} else if col.port >= 0 && col.port < len(fields) {
			if v, ok := parsePortValue(fields[col.port]); ok {
				port = v
			}
		}
		out = append(out, utils.IPAddr{IP: ip, Port: port})
	}
	return out
}

// 定位 IP / 端口列，并返回数据起始行号
func locateColumns(rows [][]string, delim rune) (col ipColumn, startRow int) {
	col = ipColumn{ip: -1, port: -1}

	// 1. 表头匹配
	header := rows[0]
	ipIdx, portIdx := -1, -1
	for i, f := range header {
		if ipIdx < 0 && isIPHeaderField(f) {
			ipIdx = i
			continue
		}
		if portIdx < 0 && isPortHeaderField(f) {
			portIdx = i
		}
	}
	if ipIdx >= 0 {
		return ipColumn{ip: ipIdx, port: portIdx}, 1
	}

	// 2. 无表头：自动推断
	// 单列时直接当作 IP 列（兼容原有 ip.txt）
	if len(header) == 1 {
		return ipColumn{ip: 0, port: -1}, 0
	}

	// 逐列统计：该列有多少行是「纯 IP」
	ipVotes := make([]int, len(header))
	portVotes := make([]int, len(header))
	total := 0
	for _, fields := range rows {
		if len(fields) < 2 {
			continue
		}
		total++
		for i, f := range fields {
			if i >= len(ipVotes) {
				continue
			}
			if isPlainIPField(f) {
				ipVotes[i]++
			} else if _, ok := parsePortValue(f); ok {
				portVotes[i]++
			}
		}
	}
	if total == 0 {
		// 所有行都是单列，按单列 IP 处理
		return ipColumn{ip: 0, port: -1}, 0
	}
	// 选出纯 IP 最多的列
	best, bestVotes := -1, 0
	for i, v := range ipVotes {
		if v > bestVotes {
			best, bestVotes = i, v
		}
	}
	if best < 0 || bestVotes == 0 {
		return ipColumn{ip: -1, port: -1}, 0
	}
	// 端口列：取端口票数最多、且不是 IP 列的那一列
	pBest, pVotes := -1, 0
	for i, v := range portVotes {
		if i == best {
			continue
		}
		if v > pVotes {
			pBest, pVotes = i, v
		}
	}
	// 端口列必须至少覆盖一半的数据行，避免误判（如把「已发送=4」当作端口）
	if pVotes == 0 || pVotes*2 < total {
		pBest = -1
	}
	return ipColumn{ip: best, port: pBest}, 0
}
