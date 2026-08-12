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
	"sort"
	"strings"
	"sync"
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

// Tool-agnostic local listener ports commonly used by desktop proxy/VPN clients.
// This is NOT a product-brand list: we only use it as a scan seed. Final choice
// is always based on live TCP + protocol handshake + end-to-end reachability.
var commonLocalProxyPorts = []int{
	7890, 7891, 7892, 7893, 7897, 7899,
	1080, 1081, 1086, 1087, 10808, 10809,
	2017, 20170, 20171, 20172,
	2080, 3128, 6152, 6153,
	8080, 8118, 8888, 9050, 12334, 12335,
}

type StandardProxyRouteOptions struct {
	Mode            string
	LocalGatewayURL string
}

// LocalNetworkEnvironment is a live snapshot of what the machine currently
// exposes for first-hop forwarding. Brand-agnostic: only ports, schemes, latency.
type LocalNetworkEnvironment struct {
	// ExplicitGateway is the user-configured local hop when it probes healthy.
	ExplicitGateway string `json:"explicitGateway,omitempty"`
	// Gateways are working local HTTP/SOCKS endpoints ranked by quality.
	Gateways []LocalGatewayInfo `json:"gateways"`
	// BestGateway is Gateways[0].URL when any gateway works.
	BestGateway string `json:"bestGateway,omitempty"`
	// HasLocalGateway is true when at least one local hop can reach the internet.
	HasLocalGateway bool `json:"hasLocalGateway"`
	// ProbedAt is when this snapshot was taken.
	ProbedAt time.Time `json:"probedAt"`
}

// LocalGatewayInfo describes one live local first-hop endpoint.
type LocalGatewayInfo struct {
	URL     string        `json:"url"`
	Scheme  string        `json:"scheme"` // http | socks5
	Host    string        `json:"host"`
	Port    string        `json:"port"`
	Latency time.Duration `json:"latencyMs"` // wall time for protocol+reachability probe
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
		return "", fmt.Errorf("本地转发网关必须是本机 HTTP/SOCKS5 监听地址")
	}
	u, err := url.Parse(normalized)
	if err != nil || (u.Scheme != "http" && u.Scheme != "socks5") {
		return "", fmt.Errorf("本地转发网关仅支持 HTTP 或 SOCKS5 监听地址")
	}
	return normalized, nil
}

// LocalGatewayCandidates returns candidate URLs for discovery (explicit, env,
// and common local ports with both HTTP and SOCKS5 schemes). Selection is never
// based on brand preference — only live probes decide.
func LocalGatewayCandidates(explicit string) []string {
	items := buildLocalGatewaySeedURLs(explicit)
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, raw := range items {
		normalized, err := NormalizeLocalGatewayURL(raw)
		if err != nil {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func buildLocalGatewaySeedURLs(explicit string) []string {
	raw := make([]string, 0, 64)
	add := func(values ...string) {
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v != "" {
				raw = append(raw, v)
			}
		}
	}
	add(explicit)
	add(os.Getenv("ALL_PROXY"), os.Getenv("HTTPS_PROXY"), os.Getenv("HTTP_PROXY"), os.Getenv("SOCKS_PROXY"), os.Getenv("socks_proxy"))

	// Open local TCP listeners first — only open ports get scheme probes.
	openPorts := scanOpenLocalProxyPorts(120 * time.Millisecond)
	if len(openPorts) == 0 {
		// Fallback seed when the quick scan misses (slow bind / IPv6-only etc.).
		openPorts = append([]int{}, commonLocalProxyPorts...)
	}
	for _, port := range openPorts {
		add(
			fmt.Sprintf("http://127.0.0.1:%d", port),
			fmt.Sprintf("socks5://127.0.0.1:%d", port),
		)
	}
	return raw
}

// scanOpenLocalProxyPorts returns localhost ports that accept a TCP connection.
// Cheap filter so protocol probes are not wasted on closed ports.
func scanOpenLocalProxyPorts(perPortTimeout time.Duration) []int {
	if perPortTimeout <= 0 {
		perPortTimeout = 120 * time.Millisecond
	}
	type hit struct {
		port int
		ok   bool
	}
	ch := make(chan hit, len(commonLocalProxyPorts))
	var wg sync.WaitGroup
	for _, port := range commonLocalProxyPorts {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)), perPortTimeout)
			if err != nil {
				ch <- hit{port: port}
				return
			}
			_ = conn.Close()
			ch <- hit{port: port, ok: true}
		}(port)
	}
	wg.Wait()
	close(ch)
	out := make([]int, 0, 8)
	for item := range ch {
		if item.ok {
			out = append(out, item.port)
		}
	}
	sort.Ints(out)
	return out
}

// ProbeLocalNetworkEnvironment inspects the machine's current first-hop options.
// Explicit config is preferred when healthy; otherwise all working local
// HTTP/SOCKS endpoints are ranked by probe latency (tool-agnostic).
func ProbeLocalNetworkEnvironment(explicit string, timeout time.Duration) LocalNetworkEnvironment {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	env := LocalNetworkEnvironment{ProbedAt: time.Now()}
	explicit = strings.TrimSpace(explicit)

	if explicit != "" {
		if normalized, err := NormalizeLocalGatewayURL(explicit); err == nil {
			started := time.Now()
			if probeLocalGateway(normalized, timeout) {
				info := gatewayInfoFromURL(normalized, time.Since(started))
				env.ExplicitGateway = normalized
				env.Gateways = append(env.Gateways, info)
				env.BestGateway = normalized
				env.HasLocalGateway = true
				// Still collect others so Auto can compare paths, but explicit
				// stays first when it works.
			}
		}
	}

	candidates := LocalGatewayCandidates("")
	type result struct {
		info LocalGatewayInfo
		ok   bool
	}
	results := make(chan result, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		if explicit != "" && strings.EqualFold(candidate, env.ExplicitGateway) {
			// Already recorded.
			results <- result{}
			continue
		}
		go func() {
			started := time.Now()
			if probeLocalGateway(candidate, timeout) {
				results <- result{info: gatewayInfoFromURL(candidate, time.Since(started)), ok: true}
				return
			}
			results <- result{}
		}()
	}
	extras := make([]LocalGatewayInfo, 0, len(candidates))
	for range candidates {
		item := <-results
		if item.ok {
			extras = append(extras, item.info)
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].Latency < extras[j].Latency
	})
	env.Gateways = append(env.Gateways, extras...)
	if !env.HasLocalGateway && len(env.Gateways) > 0 {
		env.BestGateway = env.Gateways[0].URL
		env.HasLocalGateway = true
	} else if env.HasLocalGateway && env.BestGateway == "" && len(env.Gateways) > 0 {
		env.BestGateway = env.Gateways[0].URL
	}
	return env
}

func gatewayInfoFromURL(raw string, latency time.Duration) LocalGatewayInfo {
	u, _ := url.Parse(raw)
	info := LocalGatewayInfo{URL: raw, Latency: latency}
	if u != nil {
		info.Scheme = strings.ToLower(u.Scheme)
		info.Host = u.Hostname()
		info.Port = u.Port()
	}
	return info
}

// DiscoverLocalGateway finds one healthy local first hop.
// Prefer explicit when it works; otherwise the lowest-latency live endpoint.
func DiscoverLocalGateway(explicit string, timeout time.Duration) string {
	env := ProbeLocalNetworkEnvironment(explicit, timeout)
	if strings.TrimSpace(explicit) != "" {
		// Hard requirement when user pinned a gateway: only that URL, or nothing.
		if env.ExplicitGateway != "" {
			return env.ExplicitGateway
		}
		return ""
	}
	return env.BestGateway
}

// preferLocalGateway ranks two working gateways by latency only (tool-agnostic).
func preferLocalGateway(_ int, latencyA time.Duration, _ int, latencyB time.Duration) bool {
	return latencyA < latencyB
}

// GatewayFamilyName keeps a coarse diagnostic label by port range only.
// Not used for routing priority.
func GatewayFamilyName(gatewayURL string) string {
	u, err := url.Parse(strings.TrimSpace(gatewayURL))
	if err != nil || u == nil {
		return "unknown"
	}
	return "local:" + u.Scheme + ":" + u.Port()
}

func IsClashVergeLocalPort(raw string) bool {
	// Kept for older tests/callers; routing no longer special-cases any brand.
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	switch u.Port() {
	case "7890", "7891", "7892", "7893", "7897":
		return true
	default:
		return false
	}
}

func probeLocalGateway(gatewayURL string, timeout time.Duration) bool {
	dialer, err := newUpstreamGatewayDialer(gatewayURL)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Protocol must match the listener: HTTP CONNECT vs SOCKS5 handshake.
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
		return nil, fmt.Errorf("本地转发网关地址无效")
	}
	dialer := &upstreamGatewayDialer{proxyURL: u, netDialer: net.Dialer{Timeout: 8 * time.Second}}
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
		return nil, fmt.Errorf("本地转发网关 CONNECT 返回 HTTP %d", response.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *upstreamGatewayDialer) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	return nil, fmt.Errorf("本地 HTTP/SOCKS 第一跳不支持 UDP；请使用系统隧道模式承载 UDP")
}
