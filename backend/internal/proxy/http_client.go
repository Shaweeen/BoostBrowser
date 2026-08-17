package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"boost-browser/backend/internal/config"

	C "github.com/metacubex/mihomo/constant"
	xproxy "golang.org/x/net/proxy"
)

// buildProxyHTTPClient 根据代理配置构建 HTTP 客户端，统一用于测速/健康检测场景。
// route 可选：local_gateway 模式经本地 VPN 网关拨号到标准代理节点，
// 与池页测速、环境 relay 的拨号路径一致（用户开 TUN 时直连会被截获）。
func buildProxyHTTPClient(
	src string,
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	timeout time.Duration,
	route ...SpeedTestRouteOptions,
) (*http.Client, error) {
	routeOpts := SpeedTestRouteOptions{}
	if len(route) > 0 {
		routeOpts = route[0]
	}
	routeOpts.Mode = NormalizeProxyNetworkMode(routeOpts.Mode)
	routeOpts.LocalGatewayURL = strings.TrimSpace(routeOpts.LocalGatewayURL)

	l := strings.ToLower(strings.TrimSpace(src))
	if l == "" || l == "direct://" {
		return &http.Client{Timeout: timeout}, nil
	}
	if LooksLikeStandardProxyConfig(src) && routeOpts.Mode == ProxyNetworkModeLocalGateway {
		return buildStandardProxyClientViaGateway(src, timeout, routeOpts)
	}
	if LooksLikeStandardProxyConfig(src) {
		normalized, err := NormalizeStandardProxyConfig(src, "http")
		if err != nil {
			return nil, fmt.Errorf("代理格式无效: %w", err)
		}
		src = normalized
		l = strings.ToLower(src)
	}

	if IsSingBoxProtocol(src) {
		if singboxMgr == nil {
			return nil, fmt.Errorf("sing-box 管理器未初始化")
		}
		socks5Addr, err := singboxMgr.EnsureBridge(src, proxies, proxyId)
		if err != nil {
			return nil, fmt.Errorf("sing-box 桥接启动失败: %w", err)
		}
		return buildSocks5HTTPClient(strings.TrimPrefix(socks5Addr, "socks5://"), timeout)
	}

	if RequiresBridge(src, proxies, proxyId) {
		if xrayMgr == nil {
			return nil, fmt.Errorf("xray 管理器未初始化")
		}
		socks5Addr, err := xrayMgr.EnsureBridge(src, proxies, proxyId)
		if err != nil {
			return nil, fmt.Errorf("xray 桥接启动失败: %w", err)
		}
		return buildSocks5HTTPClient(strings.TrimPrefix(socks5Addr, "socks5://"), timeout)
	}

	if strings.HasPrefix(l, "socks5://") {
		u, err := url.Parse(src)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 地址解析失败: %w", err)
		}
		var auth *xproxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &xproxy.Auth{
				User:     u.User.Username(),
				Password: pass,
			}
		}
		dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 dialer 创建失败: %w", err)
		}
		contextDialer, ok := dialer.(xproxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 dialer 不支持 ContextDialer")
		}
		transport := &http.Transport{DialContext: contextDialer.DialContext}
		return &http.Client{Transport: transport, Timeout: timeout}, nil
	}

	proxyURL, err := url.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("代理地址解析失败: %w", err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	if strings.EqualFold(proxyURL.Scheme, "https") {
		// 这里的 TLS 是“客户端 -> HTTPS 代理服务器”这一层，不是访问目标站点 my.ippure.com 的 TLS。
		// 很多住宅/机房代理会给 IP:port 返回域名证书或自签证书，证书里没有 IP SAN，
		// Go 默认会报：x509: cannot validate certificate for x.x.x.x because it doesn't contain any IP SANs。
		// 当前项目 Windows Go 版本没有 http.Transport.ProxyTLSClientConfig，所以用 DialTLSContext
		// 只在拨号地址等于代理服务器地址时放宽校验；其它 HTTPS 连接仍走 Go 默认校验。
		transport.DialTLSContext = insecureHTTPSProxyDialTLSContext(proxyURL.Host)
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// buildStandardProxyClientViaGateway 在 local_gateway 模式下构建 HTTP 客户端：
// 先经本地 VPN 网关（Clash 7897 等）连到标准代理节点，再经节点访问目标。
func buildStandardProxyClientViaGateway(src string, timeout time.Duration, routeOpts SpeedTestRouteOptions) (*http.Client, error) {
	gateway := DiscoverLocalGateway(routeOpts.LocalGatewayURL, 1800*time.Millisecond)
	if gateway == "" {
		return nil, fmt.Errorf("未检测到可用的本地 VPN 网关，请在设置中填写本地网关地址（如 127.0.0.1:7897）")
	}
	d, err := newUpstreamGatewayDialer(gateway)
	if err != nil {
		return nil, fmt.Errorf("本地 VPN 网关不可用: %w", err)
	}

	defaultScheme := PreferredSchemeFromProxySource(src)
	if defaultScheme == "" {
		defaultScheme = "http"
	}
	normalized, err := NormalizeStandardProxyConfig(src, defaultScheme)
	if err != nil {
		return nil, fmt.Errorf("代理格式无效: %w", err)
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("代理地址解析失败: %s", src)
	}

	if strings.EqualFold(u.Scheme, "socks5") {
		var auth *xproxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: pass}
		}
		dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, &gatewayForwarder{d: d})
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 dialer 创建失败: %w", err)
		}
		contextDialer, ok := dialer.(xproxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 dialer 不支持 ContextDialer")
		}
		transport := &http.Transport{DialContext: contextDialer.DialContext}
		return &http.Client{Transport: transport, Timeout: timeout}, nil
	}

	// http/https 节点：经网关连到节点后，再经节点 CONNECT 目标。
	transport := &http.Transport{
		Proxy:       http.ProxyURL(u),
		DialContext: d.DialContext,
	}
	if strings.EqualFold(u.Scheme, "https") {
		// HTTPS 代理层也经网关拨号；对代理地址放宽证书校验（IP/自签证书）。
		gwDialer := d
		transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := gwDialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(raw, &tls.Config{
				ServerName:         tlsServerName(addr),
				InsecureSkipVerify: strings.EqualFold(addr, u.Host), // #nosec G402 -- HTTPS proxy layer with IP/self-signed proxy certs.
			})
			if deadline, ok := ctx.Deadline(); ok {
				_ = tlsConn.SetDeadline(deadline)
			}
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			_ = tlsConn.SetDeadline(time.Time{})
			return tlsConn, nil
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// gatewayForwarder 把本地 VPN 网关拨号器包装成 x/net/proxy 需要的 forward
// dialer，让 SOCKS5 客户端先经网关连到代理节点（local_gateway 模式）。
type gatewayForwarder struct {
	d C.Dialer
}

func (f *gatewayForwarder) Dial(network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return f.d.DialContext(ctx, network, address)
}

func buildSocks5HTTPClient(socks5Host string, timeout time.Duration) (*http.Client, error) {
	dialer, err := xproxy.SOCKS5("tcp", socks5Host, nil, xproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 dialer 创建失败: %w", err)
	}
	contextDialer, ok := dialer.(xproxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer 不支持 ContextDialer")
	}
	transport := &http.Transport{DialContext: contextDialer.DialContext}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func insecureHTTPSProxyDialTLSContext(proxyHost string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{}
		if deadline, ok := ctx.Deadline(); ok {
			dialer.Deadline = deadline
		}

		serverName := tlsServerName(addr)
		insecureProxyLayer := strings.EqualFold(addr, proxyHost)
		conn, err := tls.DialWithDialer(dialer, network, addr, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: insecureProxyLayer, // #nosec G402 -- only for HTTPS proxy layer with IP/self-signed proxy certs.
		})
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

func tlsServerName(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return strings.Trim(host, "[]")
}
