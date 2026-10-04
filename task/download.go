package task

import (
	"context"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

const (
	bufferSize                     = 1024
	defaultURL                     = "https://cf.xiu2.xyz/url"
	defaultTimeout                 = 10 * time.Second
	defaultDisableDownload         = false
	defaultTestNum                 = 10
	defaultMinSpeed        float64 = 0.0
)

var (
	URL     = defaultURL
	Timeout = defaultTimeout
	Disable = defaultDisableDownload

	TestCount = defaultTestNum
	MinSpeed  = defaultMinSpeed
)

func checkDownloadDefault() {
	if URL == "" {
		URL = defaultURL
	}
	if Timeout <= 0 {
		Timeout = defaultTimeout
	}
	if TestCount <= 0 {
		TestCount = defaultTestNum
	}
	if MinSpeed <= 0.0 {
		MinSpeed = defaultMinSpeed
	}
}

func TestDownloadSpeed(ipSet utils.PingDelaySet) (speedSet utils.DownloadSpeedSet) {
	checkDownloadDefault()
	if Disable {
		return utils.DownloadSpeedSet(ipSet)
	}
	if len(ipSet) <= 0 { // IP 数组长度(IP数量) 大于 0 时才会继续下载测速
		utils.Yellow.Println("[信息] 延迟测速结果 IP 数量为 0，跳过下载测速。")
		return
	}
	testNum := TestCount                        // 等待下载测速的队列数量 先默认等于 下载测速数量(-dn）
	if len(ipSet) < TestCount || MinSpeed > 0 { // 如果延迟测速并过滤后的 IP 数组长度(IP数量) 小于 下载测速数量(-dn），（即 -dn 预期数量是不够的），或者指定了 下载测速下限 (-sl) 条件（这就可能要全部下载测速一遍，直到找齐预期数量或测完为止），则 等待下载测速的队列数量 修正为 IP 数量
		testNum = len(ipSet)
	}
	if testNum < TestCount { // 如果 等待下载测速的队列数量 小于 下载测速数量(-dn），（显然 -dn 预期数量是不够的），所以 下载测速数量(-dn）修正为 等待下载测速的队列数量
		TestCount = testNum
	}

	utils.Cyan.Printf("开始下载测速（下限：%.2f MB/s, 数量：%d, 队列：%d）\n", MinSpeed, TestCount, testNum)
	// 非标准端口提示：-url 默认是 https(443)，若目标端口不是 443，下载测速很可能连不通
	warnNonStandardPort(ipSet)
	// 控制 下载测速进度条 与 延迟测速进度条 长度一致（强迫症）
	bar_a := len(strconv.Itoa(len(ipSet)))
	bar_b := "     "
	for i := 0; i < bar_a; i++ {
		bar_b += " "
	}
	bar := utils.NewBar(TestCount, bar_b, "")
	for i := 0; i < testNum; i++ {
		speed, colo := downloadHandler(ipSet[i].IP)
		ipSet[i].DownloadSpeed = speed
		if ipSet[i].Colo == "" { // 只有当 Colo 是空的时候，才写入，否则代表之前是 httping 测速并获取过了
			ipSet[i].Colo = colo
		}
		// 在每个 IP 下载测速后，以 [下载速度下限] 条件过滤结果
		if speed >= MinSpeed*1024*1024 {
			bar.Grow(1, "")
			speedSet = append(speedSet, ipSet[i]) // 高于下载速度下限时，添加到新数组中
			if len(speedSet) == TestCount {       // 凑够满足条件的 IP 时（下载测速数量 -dn），就跳出循环
				break
			}
		}
	}
	bar.Done()
	if MinSpeed == 0.00 { // 如果没有指定下载速度下限，则直接返回所有测速数据
		speedSet = utils.DownloadSpeedSet(ipSet)
	} else if utils.Debug && len(speedSet) == 0 { // 如果指定了下载速度下限，且是调试模式下，且没有找到任何一个满足条件的 IP 时，返回所有测速数据，供用户查看当前的测速结果，以便适当调低预期测速条件
		utils.Yellow.Println("[调试] 没有满足 下载速度下限 条件的 IP，忽略条件返回所有测速数据（方便下次测速时调整条件）。")
		speedSet = utils.DownloadSpeedSet(ipSet)
	}
	// 按速度排序
	sort.Sort(speedSet)
	return
}

func getDialContext(ip *utils.IPAddr) func(ctx context.Context, network, address string) (net.Conn, error) {
	// 优先使用该目标自带的端口（来自 CSV 的 port 列或 IP:端口 写法），否则回退 [-tp]
	fakeSourceAddr := ip.Addr(portOf(ip))
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fakeSourceAddr)
	}
}

// 统一的请求报错调试输出
func printDownloadDebugInfo(ip *utils.IPAddr, err error, statusCode int, url, lastRedirectURL string, response *http.Response) {
	finalURL := url // 默认的最终 URL，这样当 response 为空时也能输出
	if lastRedirectURL != "" {
		finalURL = lastRedirectURL // 如果 lastRedirectURL 不是空，说明重定向过，优先输出最后一次要重定向至的目标
	} else if response != nil && response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String() // 如果 response 不为 nil，且 Request 和 URL 都不为 nil，则获取最后一次成功的响应地址
	}
	if url != finalURL { // 如果 URL 和最终地址不一致，说明有重定向，是该重定向后的地址引起的错误
		if statusCode > 0 { // 如果状态码大于 0，说明是后续 HTTP 状态码引起的错误
			utils.Red.Printf("[调试] IP: %s, 下载测速终止，HTTP 状态码: %d, 下载测速地址: %s, 出错的重定向后地址: %s\n", ip.String(), statusCode, url, finalURL)
		} else {
			utils.Red.Printf("[调试] IP: %s, 下载测速失败，错误信息: %v, 下载测速地址: %s, 出错的重定向后地址: %s\n", ip.String(), err, url, finalURL)
		}
	} else { // 如果 URL 和最终地址一致，说明没有重定向
		if statusCode > 0 { // 如果状态码大于 0，说明是后续 HTTP 状态码引起的错误
			utils.Red.Printf("[调试] IP: %s, 下载测速终止，HTTP 状态码: %d, 下载测速地址: %s\n", ip.String(), statusCode, url)
		} else {
			utils.Red.Printf("[调试] IP: %s, 下载测速失败，错误信息: %v, 下载测速地址: %s\n", ip.String(), err, url)
		}
	}
}

// warnNonStandardPort 当存在非 443 端口的测速目标时，提示用户下载测速可能失败及解决方式。
func warnNonStandardPort(ipSet utils.PingDelaySet) {
	seen := make(map[int]struct{})
	for _, v := range ipSet {
		if p := portOf(v.IP); p != 443 {
			seen[p] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return
	}
	ports := make([]string, 0, len(seen))
	for p := range seen {
		ports = append(ports, strconv.Itoa(p))
	}
	sort.Strings(ports)
	utils.Yellow.Printf("[提示] 检测到非 443 端口（%s），而下载测速地址为 [%s]。\n", strings.Join(ports, ","), URL)
	utils.Yellow.Printf("       若非标端口不支持下载测速（结果全为 0.00），请改用 [-dd] 只做延迟测速，或把 [-url] 指向一个支持该端口访问的地址。\n")
}

// downloadHandler 在 Timeout 窗口内持续下载，返回平均速度与机房码。
//
// 单个请求的字节数有限（测速端点通常有上限），在快节点上零点几秒就下完，
// 这么短的样本受 TCP 慢启动和调度抖动影响极大——同一个 IP 连测两次能差好几倍。
// 所以窗口没跑满就继续发下一次请求，把样本时间拉够，速度按「总字节 / 总耗时」算。
func downloadHandler(ip *utils.IPAddr) (float64, string) {
	deadline := time.Now().Add(Timeout)
	var (
		totalRead int64
		colo      string
		started   = time.Now()
	)
	for time.Now().Before(deadline) {
		n, c, ok := downloadOnce(ip, deadline)
		totalRead += n
		if colo == "" {
			colo = c
		}
		if !ok || n == 0 {
			break
		}
	}
	elapsed := time.Since(started)
	if totalRead <= 0 || elapsed <= 0 {
		return 0.0, colo
	}
	return float64(totalRead) / elapsed.Seconds(), colo
}

// downloadOnce 发一次请求并读到底（或读到 deadline），
// 返回读到的字节数、机房码，以及是否值得继续下一轮。
func downloadOnce(ip *utils.IPAddr, deadline time.Time) (int64, string, bool) {
	var lastRedirectURL string // 用于记录最后一次重定向目标，以便在访问错误时输出
	tr := &http.Transport{DialContext: getDialContext(ip)}
	// 每个 IP 一个 Transport，用完回收，避免占满本地临时端口
	defer tr.CloseIdleConnections()
	// 客户端超时按窗口剩余时间收窄：第二轮之后窗口所剩无几，
	// 仍用完整 Timeout 会跑过头，把已经读到的数据白白丢掉。
	budget := time.Until(deadline)
	if budget <= 0 {
		return 0, "", false
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   budget,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			lastRedirectURL = req.URL.String() // 记录每次重定向的目标，以便在访问错误时输出
			if len(via) > 10 {                 // 限制最多重定向 10 次
				if utils.Debug { // 调试模式下，输出更多信息
					utils.Red.Printf("[调试] IP: %s, 下载测速地址重定向次数过多，终止测速，下载测速地址: %s\n", ip.String(), req.URL.String())
				}
				return http.ErrUseLastResponse
			}
			if req.Header.Get("Referer") == defaultURL { // 当使用默认下载测速地址时，重定向不携带 Referer
				req.Header.Del("Referer")
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", URL, nil)
	if err != nil {
		if utils.Debug { // 调试模式下，输出更多信息
			utils.Red.Printf("[调试] IP: %s, 下载测速请求创建失败，错误信息: %v, 下载测速地址: %s\n", ip.String(), err, URL)
		}
		return 0, "", false
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_12_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/98.0.4758.80 Safari/537.36")

	response, err := client.Do(req)
	if err != nil {
		if utils.Debug { // 调试模式下，输出更多信息
			printDownloadDebugInfo(ip, err, 0, URL, lastRedirectURL, response)
		}
		return 0, "", false
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		if utils.Debug { // 调试模式下，输出更多信息
			printDownloadDebugInfo(ip, nil, response.StatusCode, URL, lastRedirectURL, response)
		}
		return 0, "", false
	}

	// 通过头部参数获取地区码
	colo := getHeaderColo(response.Header)

	// 把这一次响应体读完（或读到窗口结束），
	// 速度由 downloadHandler 按总字节和总耗时统一计算。
	buffer := make([]byte, bufferSize)
	var contentRead int64
	for {
		if time.Now().After(deadline) {
			return contentRead, colo, false
		}
		n, err := response.Body.Read(buffer)
		contentRead += int64(n)
		if err != nil {
			// io.EOF 表示这一次下完了，可以再来一轮把窗口填满；
			// 其它错误说明这条连接不好使，不必再试。
			return contentRead, colo, err == io.EOF
		}
	}
}
