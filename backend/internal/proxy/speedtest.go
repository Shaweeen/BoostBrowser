package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
	"gopkg.in/yaml.v3"

	"boost-browser/backend/internal/config"
	"boost-browser/backend/internal/logger"
)

// ─── Clash 标准测速 URL ───
// 使用 HTTP 与 Clash 客户端保持一致

const defaultTestURL = "http://www.gstatic.com/generate_204"

var defaultSpeedTestURLs = []string{
	"http://www.gstatic.com/generate_204",
	"http://connectivitycheck.gstatic.com/generate_204",
	"http://cp.cloudflare.com/generate_204",
	"http://www.msftconnecttest.com/connecttest.txt",
}

// SpeedTestConfig 测速参数
type SpeedTestConfig struct {
	Timeout    time.Duration
	TCPTimeout time.Duration
	URLs       []string
}

var DefaultSpeedTestConfig = SpeedTestConfig{
	// 手动验证必须有可预期的响应时间。协议候选并发探测，并共享每个
	// 候选的短总预算；不再出现 HTTP/HTTPS/SOCKS5 逐个等待几十秒。
	Timeout:    6 * time.Second,
	TCPTimeout: 3 * time.Second,
}

// ─── 对外入口 ───

// SpeedTest 使用 mihomo 代理适配器进行测速。
// 采用 unified-delay 策略：先建立连接（预热），再单独计时 HTTP 往返，
// 与 Clash 客户端 unified-delay: true 的延迟结果一致。
func SpeedTest(
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	cfg *SpeedTestConfig,
) TestResult {
	log := logger.New("SpeedTest")

	if cfg == nil {
		c := DefaultSpeedTestConfig
		cfg = &c
	}

	// 查找代理配置
	src := ""
	for _, item := range proxies {
		if strings.EqualFold(item.ProxyId, proxyId) {
			src = strings.TrimSpace(item.ProxyConfig)
			break
		}
	}
	if src == "" {
		return TestResult{ProxyId: proxyId, Ok: false, Error: "代理配置为空"}
	}

	if strings.ToLower(src) == "direct://" {
		return TestResult{ProxyId: proxyId, Ok: true, LatencyMs: 0, ResolvedConfig: src}
	}
	if LooksLikeStandardProxyConfig(src) {
		normalized, err := NormalizeStandardProxyConfig(src, "http")
		if err != nil {
			return TestResult{ProxyId: proxyId, Ok: false, Error: fmt.Sprintf("代理格式无效: %v", err)}
		}
		src = normalized
	}

	testURLs := cfg.URLs
	if len(testURLs) == 0 {
		testURLs = defaultSpeedTestURLs
	}

	// 将代理配置转换为 mihomo mapping
	mapping, err := proxyConfigToMapping(src)
	if err != nil {
		log.Warn("代理配置解析失败，降级到 TCP ping",
			logger.F("proxy_id", proxyId),
			logger.F("error", err.Error()),
		)
		return tcpPingFallback(proxyId, src, cfg.TCPTimeout, log)
	}

	// 标准 HTTP/HTTPS/SOCKS5 允许供应商未标注或标错协议。候选协议并发
	// 检查，首个真实 HTTP 响应即返回，最坏耗时受单一总预算约束。
	if LooksLikeStandardProxyConfig(src) {
		if detected, detectedResult, ok := detectWorkingStandardProxy(src, testURLs, cfg.Timeout); ok {
			detectedResult.ProxyId = proxyId
			detectedResult.ResolvedConfig = detected
			return detectedResult
		} else if detectedResult.Error != "" {
			detectedResult.ProxyId = proxyId
			result := detectedResult
			if fallback := tcpPingFallback(proxyId, src, cfg.TCPTimeout, log); fallback.Ok {
				result.LatencyMs = fallback.LatencyMs
				result.Error = "代理端口可连接，但无法通过代理访问互联网: " + result.Error
			}
			return result
		}
	}

	// 使用 mihomo adapter.ParseProxy 创建代理实例
	proxyInstance, err := adapter.ParseProxy(mapping)
	if err != nil {
		log.Warn("mihomo 代理创建失败，降级到 TCP ping",
			logger.F("proxy_id", proxyId),
			logger.F("error", err.Error()),
			logger.F("type", mapping["type"]),
		)
		return tcpPingFallback(proxyId, src, cfg.TCPTimeout, log)
	}

	// 稳定优先：多 URL、多方法、最多 2 轮重试。
	// 原来固定 HEAD gstatic + 复用同一 TCP 连接，部分代理会偶发关闭连接，导致同一个代理一会失败一会成功。
	result := robustHTTPProxyTest(proxyId, proxyInstance, testURLs, cfg.Timeout)
	if result.Ok {
		result.ResolvedConfig = src
		return result
	}

	// TCP 端口开放不等于代理可以访问互联网。旧逻辑在 HTTP/SOCKS
	// 认证失败时仍显示绿色延迟，用户随后启动浏览器才发现没有网络。
	if fallback := tcpPingFallback(proxyId, src, cfg.TCPTimeout, log); fallback.Ok {
		result.LatencyMs = fallback.LatencyMs
		if result.Error == "" {
			result.Error = "代理端口可连接，但无法验证互联网访问"
		} else {
			result.Error = "代理端口可连接，但无法通过代理访问互联网: " + result.Error
		}
	}
	return result
}

func robustHTTPProxyTest(proxyId string, px C.Proxy, testURLs []string, timeout time.Duration) TestResult {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if len(testURLs) == 0 {
		testURLs = defaultSpeedTestURLs
	}

	lastErr := ""
	deadline := time.Now().Add(timeout)
	// GET 的兼容性高于 HEAD。最多尝试三个独立连通性站点，每次不超过
	// 3 秒，且所有尝试共享 timeout 总预算。
	for index, testURL := range testURLs {
		if index >= 3 {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		requestTimeout := remaining
		if requestTimeout > 3*time.Second {
			requestTimeout = 3 * time.Second
		}
		result := singleHTTPProxyTest(proxyId, px, testURL, http.MethodGet, requestTimeout)
		if result.Ok {
			return result
		}
		lastErr = result.Error
	}
	if lastErr == "" {
		lastErr = "代理测试失败"
	}
	return TestResult{ProxyId: proxyId, Ok: false, Error: lastErr}
}

func singleHTTPProxyTest(proxyId string, px C.Proxy, testURL string, method string, timeout time.Duration) TestResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
			meta, err := addressToMeta(address)
			if err != nil {
				return nil, err
			}
			return px.DialContext(ctx, &meta)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   timeout,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, testURL, nil)
	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, Error: err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/144 Safari/537.36")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, LatencyMs: latency, Error: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1024)

	// 代理连通性测试以“能通过代理拿到 HTTP 响应”为准。
	// 部分测试站会对不同出口返回 204/200/301/403，不能只认 200/204，否则会造成假失败。
	if isUsableProxyResponseStatus(resp.StatusCode) {
		return TestResult{ProxyId: proxyId, Ok: true, LatencyMs: latency}
	}
	return TestResult{ProxyId: proxyId, Ok: false, LatencyMs: latency, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
}

func isUsableProxyResponseStatus(statusCode int) bool {
	return (statusCode >= 100 && statusCode < 400) ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusMethodNotAllowed
}

func addressToMeta(address string) (C.Metadata, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return C.Metadata{}, err
	}
	port64, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return C.Metadata{}, err
	}
	meta := C.Metadata{Host: host, DstPort: uint16(port64)}
	if addr, err := netip.ParseAddr(host); err == nil {
		meta.DstIP = addr
	}
	return meta, nil
}

// unifiedDelayTest 模拟 Clash unified-delay 模式：
// 1. 通过代理建立到目标的 TCP 连接（预热，不计入延迟）
// 2. 发送第一次 HTTP 请求预热连接（不计入延迟）
// 3. 在已建立的连接上发送第二次 HTTP 请求，只计这次的 RTT
// 这样测出的延迟 = 纯 HTTP 往返时间，和 Clash unified-delay: true 一致。
func unifiedDelayTest(proxyId string, px C.Proxy, testURL string, timeout time.Duration) TestResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 解析目标地址
	addr, err := urlToMeta(testURL)
	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, Error: fmt.Sprintf("URL 解析失败: %v", err)}
	}

	// 步骤 1：通过代理 DialContext 建立连接（预热）
	conn, err := px.DialContext(ctx, &addr)
	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, Error: fmt.Sprintf("代理连接失败: %v", err)}
	}
	defer conn.Close()

	// 构造复用此连接的 HTTP client
	transport := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return conn, nil
		},
		DisableKeepAlives: false,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	// 步骤 2：第一次请求预热（不计时）
	req1, _ := http.NewRequestWithContext(ctx, http.MethodHead, testURL, nil)
	resp1, err := client.Do(req1)
	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, Error: err.Error()}
	}
	resp1.Body.Close()

	// 步骤 3：第二次请求计时（纯 HTTP RTT）
	start := time.Now()
	req2, _ := http.NewRequestWithContext(ctx, http.MethodHead, testURL, nil)
	resp2, err := client.Do(req2)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, LatencyMs: latency, Error: err.Error()}
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK && resp2.StatusCode != http.StatusNoContent {
		return TestResult{ProxyId: proxyId, Ok: false, LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp2.StatusCode)}
	}

	return TestResult{ProxyId: proxyId, Ok: true, LatencyMs: latency}
}

// urlToMeta 将 URL 转换为 mihomo Metadata
func urlToMeta(rawURL string) (C.Metadata, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return C.Metadata{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return C.Metadata{}, fmt.Errorf("不支持的 URL scheme")
	}
	host := u.Hostname()
	if host == "" {
		return C.Metadata{}, fmt.Errorf("URL host 为空")
	}
	portNum := uint16(80)
	if u.Scheme == "https" {
		portNum = 443
	}
	if port := u.Port(); port != "" {
		port64, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return C.Metadata{}, err
		}
		portNum = uint16(port64)
	}

	meta := C.Metadata{
		Host:    host,
		DstPort: portNum,
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		meta.DstIP = addr
	}
	return meta, nil
}

// ─── 代理配置转换为 mihomo mapping ───

func proxyConfigToMapping(src string) (map[string]any, error) {
	src = strings.TrimSpace(src)
	l := strings.ToLower(src)

	// http/https 直连代理
	if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
		normalized, err := NormalizeStandardProxyConfig(src, "http")
		if err != nil {
			return nil, err
		}
		src = normalized
		l = strings.ToLower(src)
		mapping, err := parseStandardProxy(src, "http")
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(l, "https://") {
			mapping["tls"] = true
			mapping["skip-cert-verify"] = true
		}
		return mapping, nil
	}
	// socks5 直连代理
	if strings.HasPrefix(l, "socks5://") || strings.HasPrefix(l, "socks://") || strings.HasPrefix(l, "socket://") {
		normalized, err := NormalizeStandardProxyConfig(src, "socks5")
		if err != nil {
			return nil, err
		}
		return parseStandardProxy(normalized, "socks5")
	}

	// URI 格式（vmess:// vless:// 等）暂不支持直接转 mapping，降级
	if strings.Contains(l, "://") && !strings.Contains(l, "type:") {
		return nil, fmt.Errorf("URI 格式暂不支持: %s", l[:min(30, len(l))])
	}

	// Clash YAML 格式 → 直接解析
	return parseClashYAMLToMapping(src)
}

func parseStandardProxy(src string, proxyType string) (map[string]any, error) {
	u, err := url.Parse(src)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("无法解析地址: %s", src)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("无法解析端口: %s", src)
	}

	mapping := map[string]any{
		"name":   "speedtest-proxy",
		"type":   proxyType,
		"server": u.Hostname(),
		"port":   port,
	}
	if u.User != nil && u.User.Username() != "" {
		password, _ := u.User.Password()
		mapping["username"] = u.User.Username()
		mapping["password"] = password
	}
	return mapping, nil
}

func parseClashYAMLToMapping(src string) (map[string]any, error) {
	var payload interface{}
	if err := yaml.Unmarshal([]byte(src), &payload); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %v", err)
	}

	node := pickClashNode(payload)
	if node == nil {
		return nil, fmt.Errorf("无法提取 Clash 节点")
	}

	if _, ok := node["name"]; !ok {
		node["name"] = "speedtest-proxy"
	}

	return node, nil
}

func DetectWorkingStandardProxyConfig(src string, cfg *SpeedTestConfig) (string, error) {
	return detectWorkingStandardProxyConfigWithDialer(src, cfg, nil)
}

func detectWorkingStandardProxyConfigWithDialer(src string, cfg *SpeedTestConfig, upstreamDialer C.Dialer) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("代理配置为空")
	}
	if cfg == nil {
		c := DefaultSpeedTestConfig
		cfg = &c
	}
	testURLs := cfg.URLs
	if len(testURLs) == 0 {
		testURLs = defaultSpeedTestURLs
	}
	detected, result, ok := detectWorkingStandardProxyWithDialer(src, testURLs, cfg.Timeout, upstreamDialer)
	if ok {
		return detected, nil
	}
	if result.Error == "" {
		result.Error = "代理协议探测失败"
	}
	return "", fmt.Errorf("%s", result.Error)
}

func detectWorkingStandardProxy(src string, testURLs []string, timeout time.Duration) (string, TestResult, bool) {
	return detectWorkingStandardProxyWithDialer(src, testURLs, timeout, nil)
}

func detectWorkingStandardProxyWithDialer(src string, testURLs []string, timeout time.Duration, upstreamDialer C.Dialer) (string, TestResult, bool) {
	candidates := append([]string{src}, alternateStandardProxyConfigs(src)...)
	type detectionResult struct {
		candidate string
		result    TestResult
	}
	results := make(chan detectionResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			mapping, err := proxyConfigToMapping(candidate)
			if err != nil {
				results <- detectionResult{candidate: candidate, result: TestResult{Error: err.Error()}}
				return
			}
			options := make([]adapter.ProxyOption, 0, 1)
			if upstreamDialer != nil {
				options = append(options, adapter.WithDialerForAPI(upstreamDialer))
			}
			px, err := adapter.ParseProxy(mapping, options...)
			if err != nil {
				results <- detectionResult{candidate: candidate, result: TestResult{Error: err.Error()}}
				return
			}
			result := robustHTTPProxyTest("detect", px, testURLs, timeout)
			results <- detectionResult{candidate: candidate, result: result}
		}()
	}

	lastResult := TestResult{Error: "代理协议探测失败"}
	for range candidates {
		result := <-results
		if result.result.Ok {
			return result.candidate, result.result, true
		}
		if result.result.Error != "" {
			lastResult = result.result
		}
	}
	return "", lastResult, false
}

func alternateStandardProxyConfigs(src string) []string {
	trimmed := strings.TrimSpace(src)
	lower := strings.ToLower(trimmed)
	var current string
	switch {
	case strings.HasPrefix(lower, "http://"):
		current = "http"
	case strings.HasPrefix(lower, "https://"):
		current = "https"
	case strings.HasPrefix(lower, "socks5://"):
		current = "socks5"
	case strings.HasPrefix(lower, "socks://"):
		current = "socks5"
		trimmed = "socks5://" + trimmed[len("socks://"):]
	default:
		return nil
	}

	schemes := []string{"http", "https", "socks5"}
	out := make([]string, 0, len(schemes)-1)
	for _, scheme := range schemes {
		if scheme == current {
			continue
		}
		out = append(out, replaceProxyScheme(trimmed, scheme))
	}
	return out
}

func replaceProxyScheme(src string, scheme string) string {
	idx := strings.Index(src, "://")
	if idx < 0 {
		return src
	}
	return scheme + src[idx:]
}

// ─── TCP Ping 降级 ───

func tcpPingFallback(proxyId, src string, timeout time.Duration, log *logger.Logger) TestResult {
	endpoint, err := proxyEndpoint(src)
	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, Error: fmt.Sprintf("无法解析代理地址: %v", err)}
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", endpoint, timeout)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestResult{ProxyId: proxyId, Ok: false, LatencyMs: latency, Error: fmt.Sprintf("TCP 连接失败: %v", err)}
	}
	conn.Close()
	return TestResult{ProxyId: proxyId, Ok: true, LatencyMs: latency}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
