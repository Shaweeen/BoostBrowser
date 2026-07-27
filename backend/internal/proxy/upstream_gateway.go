package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	C "github.com/metacubex/mihomo/constant"
	xproxy "golang.org/x/net/proxy"
)

const (
	ProxyNetworkModeAuto         = "auto"
	ProxyNetworkModeDirect       = "direct"
	ProxyNetworkModeLocalGateway = "local_gateway"
	ProxyNetworkModeTUN          = "tun"
)

type StandardProxyRouteOptions struct {
	Mode            string
	LocalGatewayURL string
}

func NormalizeProxyNetworkMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ProxyNetworkModeDirect:
		return ProxyNetworkModeDirect
	case ProxyNetworkModeLocalGateway:
		return ProxyNetworkModeLocalGateway
	case ProxyNetworkModeTUN:
		return ProxyNetworkModeTUN
	default:
		return ProxyNetworkModeAuto
	}
}

func NormalizeLocalGatewayURL(raw string) (string, error) {
	normalized, err := NormalizeStandardProxyConfig(raw, "http")
	if err != nil || !isLocalProxyURL(normalized) {
		return "", fmt.Errorf("本地 VPN 网关必须是本机 HTTP/SOCKS5 监听地址")
	}
	u, err := url.Parse(normalized)
	if err != nil || (u.Scheme != "http" && u.Scheme != "socks5") {
		return "", fmt.Errorf("本地 VPN 网关仅支持 HTTP 或 SOCKS5 监听地址")
	}
	return normalized, nil
}

// LocalGatewayCandidates returns explicit/environment/common local proxy
// endpoints without changing the system proxy or TUN configuration.
func LocalGatewayCandidates(explicit string) []string {
	raw := []string{explicit, os.Getenv("ALL_PROXY"), os.Getenv("HTTPS_PROXY"), os.Getenv("HTTP_PROXY")}
	raw = append(raw,
		"http://127.0.0.1:7890", // Clash mixed/http
		"http://127.0.0.1:7897", // Clash Verge Rev mixed
		"socks5://127.0.0.1:7891",
		"socks5://127.0.0.1:7897",
		"http://127.0.0.1:10809", // v2rayN
		"socks5://127.0.0.1:10808",
	)
	seen := map[string]struct{}{}
	result := make([]string, 0, len(raw))
	for _, candidate := range raw {
		normalized, err := NormalizeLocalGatewayURL(candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func DiscoverLocalGateway(explicit string, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = 1800 * time.Millisecond
	}
	if strings.TrimSpace(explicit) != "" {
		normalized, err := NormalizeLocalGatewayURL(explicit)
		if err != nil || !probeLocalGateway(normalized, timeout) {
			return ""
		}
		return normalized
	}
	candidates := LocalGatewayCandidates("")
	if len(candidates) == 0 {
		return ""
	}
	type result struct {
		url     string
		latency time.Duration
	}
	results := make(chan result, len(candidates))
	for _, candidate := range candidates {
		go func(candidate string) {
			started := time.Now()
			if probeLocalGateway(candidate, timeout) {
				results <- result{url: candidate, latency: time.Since(started)}
			} else {
				results <- result{}
			}
		}(candidate)
	}
	best := result{}
	for range candidates {
		item := <-results
		if item.url != "" && (best.url == "" || item.latency < best.latency) {
			best = item
		}
	}
	return best.url
}

func probeLocalGateway(gatewayURL string, timeout time.Duration) bool {
	dialer, err := newUpstreamGatewayDialer(gatewayURL)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "www.gstatic.com:80")
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprint(conn, "GET /generate_204 HTTP/1.1\r\nHost: www.gstatic.com\r\nConnection: close\r\n\r\n"); err != nil {
		return false
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode >= 100 && response.StatusCode < 500
}

type upstreamGatewayDialer struct {
	proxyURL   *url.URL
	socksDial  xproxy.ContextDialer
	netDialer  net.Dialer
	proxyBasic string
}

func newUpstreamGatewayDialer(rawURL string) (C.Dialer, error) {
	normalized, err := NormalizeLocalGatewayURL(rawURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("本地 VPN 网关地址无效")
	}
	dialer := &upstreamGatewayDialer{proxyURL: u, netDialer: net.Dialer{Timeout: 5 * time.Second}}
	if u.User != nil {
		password, _ := u.User.Password()
		dialer.proxyBasic = base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + password))
	}
	if strings.EqualFold(u.Scheme, "socks5") {
		var auth *xproxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: password}
		}
		created, err := xproxy.SOCKS5("tcp", u.Host, auth, &dialer.netDialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := created.(xproxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("本地 SOCKS5 网关不支持上下文拨号")
		}
		dialer.socksDial = contextDialer
	}
	return dialer, nil
}

func (d *upstreamGatewayDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.socksDial != nil {
		return d.socksDial.DialContext(ctx, network, address)
	}
	conn, err := d.netDialer.DialContext(ctx, "tcp", d.proxyURL.Host)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{},
		Host:   address,
		Header: make(http.Header),
	}
	if d.proxyBasic != "" {
		request.Header.Set("Proxy-Authorization", "Basic "+d.proxyBasic)
	}
	if err := request.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		conn.Close()
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("本地 VPN 网关 CONNECT 返回 HTTP %d", response.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *upstreamGatewayDialer) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	return nil, fmt.Errorf("本地 HTTP/SOCKS 第一跳不支持 UDP；请对 UDP 节点使用 TUN 模式")
}
