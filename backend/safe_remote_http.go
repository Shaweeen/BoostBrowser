package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// validatePublicRemoteURL rejects URLs that can reach local services. It is
// shared by subscription and extension downloads, both of which accept URLs
// from the desktop UI.
func validatePublicRemoteURL(rawURL string, allowHTTP bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("URL 格式无效")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(allowHTTP && scheme == "http") {
		if allowHTTP {
			return nil, fmt.Errorf("仅支持 http/https URL")
		}
		return nil, fmt.Errorf("下载地址必须使用 HTTPS")
	}
	if parsed.Hostname() == "" || isBlockedRemoteHostname(parsed.Hostname()) {
		return nil, fmt.Errorf("URL 不允许指向本机或内网地址")
	}
	return parsed, nil
}

func isBlockedRemoteHostname(host string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isUnsafeRemoteIP(ip)
	}
	return false
}

func isUnsafeRemoteIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 (carrier-grade NAT) is not covered by net.IP.IsPrivate.
		return v4[0] == 100 && v4[1]&0xc0 == 64
	}
	return false
}

func newPublicRemoteHTTPClient(timeout time.Duration, allowHTTP bool) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A local HTTP_PROXY would bypass destination validation and reopen SSRF.
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("解析远程地址失败: %w", err)
		}
		if isBlockedRemoteHostname(host) {
			return nil, fmt.Errorf("拒绝连接本机或内网地址")
		}
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, candidate := range resolved {
			if isUnsafeRemoteIP(candidate.IP) {
				lastErr = fmt.Errorf("域名解析到本机或内网地址")
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("域名没有可用的公网地址")
		}
		return nil, lastErr
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("重定向次数过多")
			}
			_, err := validatePublicRemoteURL(req.URL.String(), allowHTTP)
			return err
		},
	}
}
