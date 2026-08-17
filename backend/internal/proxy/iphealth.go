package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"boost-browser/backend/internal/config"
)

const defaultIPPureInfoURL = "https://my.ippure.com/v1/info"

// FetchIPPureInfo 通过指定代理链路查询 IPPure 的出口 IP 健康信息。
// 返回值为第三方接口原始 JSON（map 形式），不做本地评分计算。
// route 可选：与代理池测速/环境 relay 一致，local_gateway 经本地 VPN
// 网关拨号，auto 直连失败回退网关——否则用户开 TUN 时直连会被截获，
// 健康检测和池页测速一样误报“超时/不可用”。
func FetchIPPureInfo(
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	route ...SpeedTestRouteOptions,
) (map[string]interface{}, error) {
	routeOpts := SpeedTestRouteOptions{}
	if len(route) > 0 {
		routeOpts = route[0]
	}
	routeOpts.Mode = NormalizeProxyNetworkMode(routeOpts.Mode)
	routeOpts.LocalGatewayURL = strings.TrimSpace(routeOpts.LocalGatewayURL)

	src := ""
	for _, item := range proxies {
		if strings.EqualFold(item.ProxyId, proxyId) {
			src = strings.TrimSpace(item.ProxyConfig)
			break
		}
	}
	if src == "" {
		return nil, fmt.Errorf("未找到代理配置")
	}

	data, err := fetchIPPureInfoWithSource(src, proxyId, proxies, xrayMgr, singboxMgr, 20*time.Second, routeOpts)
	if err == nil {
		return data, nil
	}
	lastErr := err

	// auto 模式：直连失败后经本地 VPN 网关重试（与池页测速一致，覆盖 TUN 接管场景）
	if routeOpts.Mode == ProxyNetworkModeAuto {
		gwOpts := routeOpts
		gwOpts.Mode = ProxyNetworkModeLocalGateway
		if data, gwErr := fetchIPPureInfoWithSource(src, proxyId, proxies, xrayMgr, singboxMgr, 20*time.Second, gwOpts); gwErr == nil {
			return data, nil
		} else {
			lastErr = gwErr
		}
	}

	// 常见导入问题：HTTP 代理被写成 socks5://，SOCKS 握手会报 unexpected protocol version 72(H)。
	// IP健康检测也跟普通测速一样自动尝试同一 host:port 的其它标准协议。
	for _, altSrc := range alternateStandardProxyConfigs(src) {
		data, altErr := fetchIPPureInfoWithSource(altSrc, proxyId, proxies, xrayMgr, singboxMgr, 20*time.Second, routeOpts)
		if altErr == nil {
			return data, nil
		}
		lastErr = altErr
	}
	return nil, lastErr
}

func fetchIPPureInfoWithSource(
	src string,
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	timeout time.Duration,
	routeOpts SpeedTestRouteOptions,
) (map[string]interface{}, error) {
	client, err := buildIPPureHTTPClient(src, proxyId, proxies, xrayMgr, singboxMgr, timeout, routeOpts)
	if err != nil {
		return nil, err
	}

	req, _ := http.NewRequest(http.MethodGet, defaultIPPureInfoURL, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/144 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 IPPure 接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 IPPure 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("IPPure HTTP %d: %s", resp.StatusCode, bodySnippet(body, 180))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("IPPure JSON 解析失败: %w", err)
	}
	return result, nil
}

func buildIPPureHTTPClient(
	src string,
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	timeout time.Duration,
	routeOpts SpeedTestRouteOptions,
) (*http.Client, error) {
	return buildProxyHTTPClient(src, proxyId, proxies, xrayMgr, singboxMgr, timeout, routeOpts)
}

func bodySnippet(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
