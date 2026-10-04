package task

import (
	"testing"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

// 断言解析结果的 IP 与端口序列
func check(t *testing.T, got []utils.IPAddr, wantIP []string, wantPort []int) {
	t.Helper()
	if len(got) != len(wantIP) {
		t.Fatalf("数量不匹配: got %d %v, want %d %v", len(got), got, len(wantIP), wantIP)
	}
	for i := range got {
		if got[i].String() != wantIP[i] {
			t.Errorf("第 %d 项 IP 不匹配: got %s, want %s", i, got[i].String(), wantIP[i])
		}
		if got[i].Port != wantPort[i] {
			t.Errorf("第 %d 项端口不匹配: got %d, want %d", i, got[i].Port, wantPort[i])
		}
	}
}

// 场景1：用户的主要需求 —— 多列表头 CSV，只需识别 ip、port 两列
func TestParseCSVHeaderManyColumns(t *testing.T) {
	text := "IP 地址,端口,已发送,已接收,丢包率,平均延迟,下载速度(MB/s),地区码\n" +
		"104.16.1.1,8443,4,4,0.00,146.23,28.64,LAX\n" +
		"104.16.1.2,2053,4,4,0.00,150.00,20.00,HKG\n"
	got := parseIPList(text)
	check(t, got, []string{"104.16.1.1", "104.16.1.2"}, []int{8443, 2053})
}

// 场景2：列顺序颠倒，port 在前 ip 在后
func TestParseCSVHeaderReversedColumns(t *testing.T) {
	text := "port,ip,comment\n8443,1.1.1.1,foo\n2053,1.0.0.1,bar\n"
	got := parseIPList(text)
	check(t, got, []string{"1.1.1.1", "1.0.0.1"}, []int{8443, 2053})
}

// 场景3：表头在中间夹杂无关列，且含 BOM
func TestParseCSVBOMAndNoisyColumns(t *testing.T) {
	text := "\ufeff运营商,地区,ip,端口,备注\n电信,香港,104.17.0.1,8080,好\n联通,日本,104.17.0.2,443,一般\n"
	got := parseIPList(text)
	check(t, got, []string{"104.17.0.1", "104.17.0.2"}, []int{8080, 443})
}

// 场景4：无表头 CSV，自动定位 IP 列与端口列
func TestParseCSVNoHeader(t *testing.T) {
	text := "104.16.1.1,8443\n104.16.1.2,2053\n104.16.1.3,8080\n"
	got := parseIPList(text)
	check(t, got, []string{"104.16.1.1", "104.16.1.2", "104.16.1.3"}, []int{8443, 2053, 8080})
}

// 场景5：兼容原有 ip.txt（单列 IP / IP段，无端口）
func TestParseLegacyIPText(t *testing.T) {
	text := "173.245.48.0/20\n104.16.0.0/12\n1.1.1.1\n"
	got := parseIPList(text)
	check(t, got, []string{"173.245.48.0", "104.16.0.0", "1.1.1.1"}, []int{0, 0, 0})
}

// 场景6：单列 IP:端口 写法（IPv4 / IPv6）
func TestParseHostPortSingleColumn(t *testing.T) {
	text := "104.16.1.1:8443\n[2400:cb00::1]:2053\n1.1.1.1\n"
	got := parseIPList(text)
	check(t, got, []string{"104.16.1.1", "2400:cb00::1", "1.1.1.1"}, []int{8443, 2053, 0})
}

// 场景7：IPv6 在 CSV 中
func TestParseCSVIPv6(t *testing.T) {
	text := "ip,port\n[2606:4700::1],8443\n2400:cb00::2,2053\n"
	got := parseIPList(text)
	check(t, got, []string{"2606:4700::1", "2400:cb00::2"}, []int{8443, 2053})
}

// 场景8：注释行、空行、制表符分隔
func TestParseSkipsCommentsAndTabs(t *testing.T) {
	text := "# 这是注释\n\nip\tport\n1.1.1.1\t8443\n\n1.0.0.1\t2053\n"
	got := parseIPList(text)
	check(t, got, []string{"1.1.1.1", "1.0.0.1"}, []int{8443, 2053})
}

// 场景9：端口非法/越界时回退为 0（后续用 -tp）
func TestParseInvalidPortFallsBack(t *testing.T) {
	text := "ip,port\n1.1.1.1,70000\n1.0.0.1,abc\n1.1.1.2,0\n"
	got := parseIPList(text)
	check(t, got, []string{"1.1.1.1", "1.0.0.1", "1.1.1.2"}, []int{0, 0, 0})
}

// 场景10：多列且端口列缺失，不应把「已发送/延迟」误判成端口
func TestParseNoPortColumnNotMisdetected(t *testing.T) {
	text := "IP 地址,已发送,已接收,丢包率,平均延迟,下载速度(MB/s),地区码\n" +
		"104.16.1.1,4,4,0.00,146.23,28.64,LAX\n" +
		"104.16.1.2,4,3,0.25,150.00,20.00,HKG\n"
	got := parseIPList(text)
	check(t, got, []string{"104.16.1.1", "104.16.1.2"}, []int{0, 0})
}

// 场景11：-ip 参数里带的端口
func TestParseIPFieldArg(t *testing.T) {
	cases := []struct {
		in   string
		ip   string
		port int
		ok   bool
	}{
		{"1.1.1.1", "1.1.1.1", 0, true},
		{"1.1.1.1:8443", "1.1.1.1", 8443, true},
		{"104.16.0.0/12", "104.16.0.0", 0, true},
		{"104.16.0.0/12:8443", "104.16.0.0", 8443, true},
		{"[2400:cb00::1]:2053", "2400:cb00::1", 2053, true},
		{"1.1.1.1:0", "", 0, false},
		{"1.1.1.1:99999", "", 0, false},
		{"1.1.1.1/abc", "", 0, false},
		{"1.1.1.1/33", "", 0, false},
		{"hello", "", 0, false},
	}
	for _, c := range cases {
		ip, port, ok := parseIPField(c.in)
		if ok != c.ok {
			t.Errorf("%q ok 不匹配: got %v want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ip.String() != c.ip || port != c.port {
			t.Errorf("%q 解析错误: got %s:%d, want %s:%d", c.in, ip, port, c.ip, c.port)
		}
	}
}

// 场景12：同一个 IP 的不同端口必须是两个独立目标（这是旧 fork 的 map 方案做不到的）
func TestSameIPSameportDuplicates(t *testing.T) {
	text := "ip,port\n1.1.1.1,8443\n1.1.1.1,2053\n"
	got := parseIPList(text)
	check(t, got, []string{"1.1.1.1", "1.1.1.1"}, []int{8443, 2053})
}
