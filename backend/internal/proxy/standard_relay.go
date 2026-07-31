package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

type StandardRelayManager struct {
	mu                sync.Mutex
	gatewayDiscoverMu sync.Mutex
	relays            map[string]*standardRelay
	refs              map[string]string
	detected          map[string]detectedStandardProxy
	detectedGateways  map[string]detectedLocalGateway
}

type detectedStandardProxy struct {
	working   string
	gateway   string
	expiresAt time.Time
}

type detectedLocalGateway struct {
	url       string
	expiresAt time.Time
}

type standardRelay struct {
	key      string
	proxyURL string
	listen   net.Listener
	server   *http.Server
	localURL string
	refCount int
}

func NewStandardRelayManager() *StandardRelayManager {
	return &StandardRelayManager{
		relays:           make(map[string]*standardRelay),
		refs:             make(map[string]string),
		detected:         make(map[string]detectedStandardProxy),
		detectedGateways: make(map[string]detectedLocalGateway),
	}
}

// detectedProxyCacheTTL keeps a successful scheme/auth detection sticky across
// multi-environment launches. Re-probing every start wastes time and can flip
// dual-protocol ports between http/socks5. Cache is invalidated on relay start
// failure so dead endpoints re-detect promptly.
const detectedProxyCacheTTL = 30 * time.Minute

func (m *StandardRelayManager) Acquire(profileID, src string, routeOptions ...StandardProxyRouteOptions) (string, string, error) {
	profileID = strings.TrimSpace(profileID)
	src = strings.TrimSpace(src)
	if profileID == "" {
		return "", "", fmt.Errorf("profile id is empty")
	}
	if !IsStandardProxyURL(src) || !standardProxyNeedsRelay(src) {
		return src, "", nil
	}

	options := StandardProxyRouteOptions{Mode: ProxyNetworkModeAuto}
	if len(routeOptions) > 0 {
		options = routeOptions[0]
	}
	options.Mode = NormalizeProxyNetworkMode(options.Mode)
	options.LocalGatewayURL = strings.TrimSpace(options.LocalGatewayURL)
	detectionKey := src + "\x00" + options.Mode + "\x00" + options.LocalGatewayURL

	localURL, working, err := m.acquireOnce(profileID, src, detectionKey, options, false)
	if err == nil {
		return localURL, working, nil
	}
	// One automatic recovery: drop sticky detection and re-probe. Required so a
	// cached dead endpoint does not brick environment starts until TTL expires.
	return m.acquireOnce(profileID, src, detectionKey, options, true)
}

func (m *StandardRelayManager) acquireOnce(
	profileID, src, detectionKey string,
	options StandardProxyRouteOptions,
	forceRedetect bool,
) (string, string, error) {
	working := ""
	gateway := ""
	now := time.Now()
	m.mu.Lock()
	if forceRedetect {
		delete(m.detected, detectionKey)
	}
	if cached, ok := m.detected[detectionKey]; ok {
		if now.Before(cached.expiresAt) {
			working = cached.working
			gateway = cached.gateway
		} else {
			delete(m.detected, detectionKey)
		}
	}
	relayKey := standardRelayKey(working, gateway)
	if working != "" {
		if localURL, ok := m.acquireExistingLocked(profileID, relayKey); ok {
			m.mu.Unlock()
			return localURL, working, nil
		}
	}
	m.mu.Unlock()

	if working == "" {
		// Protocol-labelled provider lists are frequently wrong. Probe declared
		// scheme first, then alternates under a short bounded timeout (kept on
		// every start for connectivity — not a disposable check).
		var upstreamDialer C.Dialer
		if options.Mode == ProxyNetworkModeAuto || options.Mode == ProxyNetworkModeLocalGateway {
			gateway = m.discoverLocalGatewayCached(options.LocalGatewayURL)
			if gateway != "" {
				upstreamDialer, _ = newUpstreamGatewayDialer(gateway)
			}
			if options.Mode == ProxyNetworkModeLocalGateway && upstreamDialer == nil {
				return "", "", fmt.Errorf("未检测到可用的本地 VPN HTTP/SOCKS 网关；请确认非 TUN 模式端口并填写本地 VPN 网关")
			}
		}
		var err error
		working, err = detectWorkingStandardProxyConfigWithDialer(src, &SpeedTestConfig{
			Timeout:    6 * time.Second,
			TCPTimeout: 3 * time.Second,
			URLs:       []string{defaultTestURL},
		}, upstreamDialer)
		if err != nil && options.Mode == ProxyNetworkModeAuto && upstreamDialer != nil {
			// Local gateway may be alive while blocking this particular
			// provider endpoint. Auto mode falls back to direct/TUN routing.
			gateway = ""
			working, err = DetectWorkingStandardProxyConfig(src, &SpeedTestConfig{
				Timeout:    6 * time.Second,
				TCPTimeout: 3 * time.Second,
				URLs:       []string{defaultTestURL},
			})
		}
		if err != nil {
			return "", "", fmt.Errorf("代理协议/认证验证失败；非 TUN 请填写本地 VPN 网关，TUN 请确认代理服务器连接已由 VPN 正常转发且未形成代理回环: %w", err)
		}
		working = strings.TrimSpace(working)
		relayKey = standardRelayKey(working, gateway)
		m.mu.Lock()
		m.detected[detectionKey] = detectedStandardProxy{working: working, gateway: gateway, expiresAt: now.Add(detectedProxyCacheTTL)}
		if localURL, ok := m.acquireExistingLocked(profileID, relayKey); ok {
			m.mu.Unlock()
			return localURL, working, nil
		}
		m.mu.Unlock()
	}

	r, err := startStandardRelay(working, gateway)
	if err != nil {
		m.mu.Lock()
		delete(m.detected, detectionKey)
		m.mu.Unlock()
		return "", "", err
	}

	m.mu.Lock()
	if existing := m.relays[relayKey]; existing != nil {
		m.mu.Unlock()
		_ = r.Close()
		m.mu.Lock()
		if oldKey := m.refs[profileID]; oldKey == relayKey {
			localURL := existing.localURL
			m.mu.Unlock()
			return localURL, working, nil
		} else if oldKey != "" {
			m.releaseLocked(profileID)
		}
		existing.refCount++
		m.refs[profileID] = relayKey
		localURL := existing.localURL
		m.mu.Unlock()
		return localURL, working, nil
	}
	if oldKey := m.refs[profileID]; oldKey != "" && oldKey != relayKey {
		m.releaseLocked(profileID)
	} else if oldKey == relayKey {
		// A stale reference without a live relay must not inflate refCount.
		delete(m.refs, profileID)
	}
	r.refCount = 1
	m.relays[relayKey] = r
	m.refs[profileID] = relayKey
	localURL := r.localURL
	m.mu.Unlock()
	return localURL, working, nil
}

func standardRelayKey(working, gateway string) string {
	if strings.TrimSpace(working) == "" {
		return ""
	}
	return strings.TrimSpace(working) + "\x00" + strings.TrimSpace(gateway)
}

func (m *StandardRelayManager) discoverLocalGatewayCached(explicit string) string {
	cacheKey := strings.TrimSpace(explicit)
	now := time.Now()
	m.mu.Lock()
	if cached, ok := m.detectedGateways[cacheKey]; ok && now.Before(cached.expiresAt) {
		m.mu.Unlock()
		return cached.url
	}
	m.mu.Unlock()

	m.gatewayDiscoverMu.Lock()
	defer m.gatewayDiscoverMu.Unlock()
	now = time.Now()
	m.mu.Lock()
	if cached, ok := m.detectedGateways[cacheKey]; ok && now.Before(cached.expiresAt) {
		m.mu.Unlock()
		return cached.url
	}
	m.mu.Unlock()

	gateway := DiscoverLocalGateway(explicit, 1800*time.Millisecond)
	m.mu.Lock()
	m.detectedGateways[cacheKey] = detectedLocalGateway{url: gateway, expiresAt: now.Add(5 * time.Minute)}
	m.mu.Unlock()
	return gateway
}

func (m *StandardRelayManager) acquireExistingLocked(profileID, key string) (string, bool) {
	r := m.relays[key]
	if r == nil {
		return "", false
	}
	if oldKey := m.refs[profileID]; oldKey == key {
		return r.localURL, true
	} else if oldKey != "" {
		m.releaseLocked(profileID)
	}
	r.refCount++
	m.refs[profileID] = key
	return r.localURL, true
}

func (m *StandardRelayManager) Release(profileID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.releaseLocked(strings.TrimSpace(profileID))
	m.mu.Unlock()
}

func (m *StandardRelayManager) releaseLocked(profileID string) {
	key := m.refs[profileID]
	if key == "" {
		return
	}
	delete(m.refs, profileID)
	r := m.relays[key]
	if r == nil {
		return
	}
	r.refCount--
	if r.refCount > 0 {
		return
	}
	delete(m.relays, key)
	go r.Close()
}

func (m *StandardRelayManager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	relays := make([]*standardRelay, 0, len(m.relays))
	for _, r := range m.relays {
		relays = append(relays, r)
	}
	m.relays = make(map[string]*standardRelay)
	m.refs = make(map[string]string)
	m.detected = make(map[string]detectedStandardProxy)
	m.detectedGateways = make(map[string]detectedLocalGateway)
	m.mu.Unlock()
	for _, r := range relays {
		_ = r.Close()
	}
}

func startStandardRelay(src string, gateway string) (*standardRelay, error) {
	mapping, err := proxyConfigToMapping(src)
	if err != nil {
		return nil, err
	}
	options := make([]adapter.ProxyOption, 0, 1)
	if strings.TrimSpace(gateway) != "" {
		upstreamDialer, err := newUpstreamGatewayDialer(gateway)
		if err != nil {
			return nil, err
		}
		options = append(options, adapter.WithDialerForAPI(upstreamDialer))
	}
	px, err := adapter.ParseProxy(mapping, options...)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &standardRelay{
		key:      standardRelayKey(src, gateway),
		proxyURL: src,
		listen:   ln,
		localURL: "http://" + ln.Addr().String(),
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.handle(px, w, req) }),
		ReadHeaderTimeout: 20 * time.Second,
		// IdleTimeout keeps Chromium long-lived CONNECT tunnels healthy under
		// multi-tab loads without cutting residential sessions early.
		IdleTimeout: 90 * time.Second,
	}
	r.server = server
	go func() { _ = server.Serve(ln) }()
	return r, nil
}

func (r *standardRelay) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if r.server != nil {
		return r.server.Shutdown(ctx)
	}
	if r.listen != nil {
		return r.listen.Close()
	}
	return nil
}

func (r *standardRelay) handle(px C.Proxy, w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		r.handleConnect(px, w, req)
		return
	}
	r.handleHTTP(px, w, req)
}

func (r *standardRelay) handleConnect(px C.Proxy, w http.ResponseWriter, req *http.Request) {
	dst := req.Host
	if !strings.Contains(dst, ":") {
		dst += ":443"
	}
	meta, err := addressToMeta(dst)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Residential/mobile exits are slower to dial; 45s matches common sticky
	// session warm-up without hanging the browser indefinitely.
	ctx, cancel := context.WithTimeout(req.Context(), 45*time.Second)
	defer cancel()
	upstream, err := px.DialContext(ctx, &meta)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufrw, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = bufrw.Flush()
	go relayCopy(upstream, clientConn)
	go relayCopy(clientConn, upstream)
}

func (r *standardRelay) handleHTTP(px C.Proxy, w http.ResponseWriter, req *http.Request) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			meta, err := addressToMeta(address)
			if err != nil {
				return nil, err
			}
			return px.DialContext(ctx, &meta)
		},
		// Keep-alives cut latency for multi-request pages through the same exit.
		DisableKeepAlives:     false,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 45 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
	}
	outReq := req.Clone(req.Context())
	outReq.RequestURI = ""
	outReq.URL.Scheme = req.URL.Scheme
	outReq.URL.Host = req.URL.Host
	if outReq.URL.Scheme == "" || outReq.URL.Host == "" {
		if req.Host == "" {
			http.Error(w, "missing host", http.StatusBadRequest)
			return
		}
		outReq.URL.Scheme = "http"
		outReq.URL.Host = req.Host
	}
	removeHopHeaders(outReq.Header)
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func relayCopy(dst net.Conn, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func removeHopHeaders(h http.Header) {
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(key)
	}
}

func IsStandardProxyURL(src string) bool {
	l := strings.ToLower(strings.TrimSpace(src))
	return strings.HasPrefix(l, "http://") ||
		strings.HasPrefix(l, "https://") ||
		strings.HasPrefix(l, "socks5://") ||
		strings.HasPrefix(l, "socks5h://") ||
		strings.HasPrefix(l, "socks://") ||
		strings.HasPrefix(l, "socket://")
}

func standardProxyNeedsRelay(src string) bool {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return false
	}
	// Chromium ignores credentials embedded in manual proxy settings and does
	// not support SOCKS5 username/password authentication. Route every external
	// standard proxy through the same local HTTP relay so protocol detection,
	// authentication and proxy-side DNS are identical in testing and browsing.
	// Localhost proxies (Clash/VPN clients) stay direct to avoid relay loops.
	return IsStandardProxyURL(trimmed) && !isLocalProxyURL(trimmed)
}

func isLocalProxyURL(src string) bool {
	u, err := parseProxyURLCompat(src)
	if err != nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func parseProxyURLCompat(src string) (*url.URL, error) {
	return url.Parse(strings.TrimSpace(src))
}
