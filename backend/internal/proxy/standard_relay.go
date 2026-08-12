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
	// Full local network snapshots (ports/protocols) for adaptive path selection.
	localEnvs map[string]cachedLocalNetworkEnv
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

type cachedLocalNetworkEnv struct {
	env       LocalNetworkEnvironment
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
		localEnvs:        make(map[string]cachedLocalNetworkEnv),
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
	// One automatic recovery: drop sticky detection + local-env cache and re-probe
	// when the machine's network path changed (local port up/down, system tunnel).
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
		// Invalidate local network snapshot so the next start re-scans ports /
		// protocols instead of sticking to a dead first hop.
		delete(m.detectedGateways, options.LocalGatewayURL)
		delete(m.detectedGateways, "")
		delete(m.detectedGateways, "__env__"+options.LocalGatewayURL)
		delete(m.localEnvs, strings.TrimSpace(options.LocalGatewayURL))
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
		// Tool-agnostic path selection:
		//   1) inspect local network (open ports + HTTP/SOCKS handshake)
		//   2) try candidate first hops that fit the mode
		//   3) keep the path where the environment's IP proxy actually works
		//
		// Modes:
		//   direct / tun → system route only (no local port chaining)
		//   local_gateway → require a healthy local HTTP/SOCKS first hop
		//   auto → compare system route + live local gateways; pick best E2E path
		var err error
		working, gateway, err = m.resolveWorkingNetworkPath(src, options)
		if err != nil {
			return "", "", err
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
	env := m.probeLocalNetworkEnvironmentCached(explicit)
	if strings.TrimSpace(explicit) != "" {
		return env.ExplicitGateway
	}
	return env.BestGateway
}

// probeLocalNetworkEnvironmentCached returns a short-lived snapshot of local
// first-hop options. Shared across multi-open so we do not re-scan ports for
// every environment start within a few minutes.
func (m *StandardRelayManager) probeLocalNetworkEnvironmentCached(explicit string) LocalNetworkEnvironment {
	cacheKey := strings.TrimSpace(explicit)
	now := time.Now()
	m.mu.Lock()
	if cached, ok := m.localEnvs[cacheKey]; ok && now.Before(cached.expiresAt) {
		env := cached.env
		m.mu.Unlock()
		return env
	}
	m.mu.Unlock()

	m.gatewayDiscoverMu.Lock()
	defer m.gatewayDiscoverMu.Unlock()
	now = time.Now()
	m.mu.Lock()
	if cached, ok := m.localEnvs[cacheKey]; ok && now.Before(cached.expiresAt) {
		env := cached.env
		m.mu.Unlock()
		return env
	}
	m.mu.Unlock()

	// Full live scan: open ports → protocol handshake → rank by latency.
	env := ProbeLocalNetworkEnvironment(explicit, 3*time.Second)
	m.mu.Lock()
	m.localEnvs[cacheKey] = cachedLocalNetworkEnv{env: env, expiresAt: now.Add(2 * time.Minute)}
	// Keep best-gateway map in sync for DiscoverLocalGatewayCached callers.
	m.detectedGateways[cacheKey] = detectedLocalGateway{
		url:       env.BestGateway,
		expiresAt: now.Add(2 * time.Minute),
	}
	m.detectedGateways["__env__"+cacheKey] = detectedLocalGateway{
		url:       env.BestGateway,
		expiresAt: now.Add(2 * time.Minute),
	}
	m.mu.Unlock()
	return env
}

// resolveWorkingNetworkPath picks how the environment's IP proxy should leave
// this machine: system route only, or via a live local HTTP/SOCKS first hop.
// Decision is based on end-to-end success against the actual proxy config —
// not on any third-party VPN brand.
func (m *StandardRelayManager) resolveWorkingNetworkPath(src string, options StandardProxyRouteOptions) (working, gateway string, err error) {
	probeCfg := cloneLaunchStandardProxyProbeConfig()
	mode := options.Mode

	// Forced system route: no local port chaining.
	if mode == ProxyNetworkModeDirect || mode == ProxyNetworkModeTUN {
		working, err = DetectWorkingStandardProxyConfig(src, &probeCfg)
		if err != nil {
			return "", "", fmt.Errorf("代理协议/认证验证失败（系统路由）；请确认本机网络/隧道可访问代理服务器且未形成回环: %w", err)
		}
		return working, "", nil
	}

	// Build first-hop candidates from the live local environment.
	env := m.probeLocalNetworkEnvironmentCached(options.LocalGatewayURL)
	type pathCandidate struct {
		gateway string // empty = system route
	}
	candidates := make([]pathCandidate, 0, 8)

	switch mode {
	case ProxyNetworkModeLocalGateway:
		if env.ExplicitGateway != "" {
			candidates = append(candidates, pathCandidate{gateway: env.ExplicitGateway})
		} else if env.BestGateway != "" {
			// No explicit pin: use every healthy local hop, best latency first.
			for _, g := range env.Gateways {
				candidates = append(candidates, pathCandidate{gateway: g.URL})
			}
		}
		if len(candidates) == 0 {
			return "", "", fmt.Errorf("未检测到可用的本机 HTTP/SOCKS 转发端口；请开启本地转发服务，或在设置中填写本机网关地址，或改用「系统隧道/自动」模式")
		}
	default: // auto
		// System route first: covers plain ISP and system TUN without double hop.
		candidates = append(candidates, pathCandidate{gateway: ""})
		// Then every live local hop (already ranked by latency). Cap to avoid
		// probing dozens of ports on pathological machines.
		const maxLocalHops = 4
		for i, g := range env.Gateways {
			if i >= maxLocalHops {
				break
			}
			candidates = append(candidates, pathCandidate{gateway: g.URL})
		}
	}

	type pathResult struct {
		working string
		gateway string
		latency time.Duration
		err     error
	}
	results := make(chan pathResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			started := time.Now()
			var dialer C.Dialer
			if candidate.gateway != "" {
				d, dialErr := newUpstreamGatewayDialer(candidate.gateway)
				if dialErr != nil {
					results <- pathResult{gateway: candidate.gateway, err: dialErr}
					return
				}
				dialer = d
			}
			cfg := cloneLaunchStandardProxyProbeConfig()
			resolved, detectErr := detectWorkingStandardProxyConfigWithDialer(src, &cfg, dialer)
			results <- pathResult{
				working: resolved,
				gateway: candidate.gateway,
				latency: time.Since(started),
				err:     detectErr,
			}
		}()
	}

	var best *pathResult
	var lastErr error
	for range candidates {
		item := <-results
		if item.err != nil {
			lastErr = item.err
			continue
		}
		if best == nil || item.latency < best.latency {
			copyItem := item
			best = &copyItem
		}
	}
	if best == nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("无可用网络路径")
		}
		return "", "", fmt.Errorf("代理协议/认证验证失败；已按本机网络环境尝试系统路由与本地 HTTP/SOCKS 转发，均无法到达该 IP 代理。请检查代理协议/账号、本机转发端口，以及系统隧道是否把代理服务器 IP 再次捕获形成回环: %w", lastErr)
	}
	return best.working, best.gateway, nil
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
	m.localEnvs = make(map[string]cachedLocalNetworkEnv)
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
