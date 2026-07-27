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
	mu       sync.Mutex
	relays   map[string]*standardRelay
	refs     map[string]string
	detected map[string]detectedStandardProxy
}

type detectedStandardProxy struct {
	working   string
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
		relays:   make(map[string]*standardRelay),
		refs:     make(map[string]string),
		detected: make(map[string]detectedStandardProxy),
	}
}

func (m *StandardRelayManager) Acquire(profileID, src string) (string, string, error) {
	profileID = strings.TrimSpace(profileID)
	src = strings.TrimSpace(src)
	if profileID == "" {
		return "", "", fmt.Errorf("profile id is empty")
	}
	if !IsStandardProxyURL(src) || !standardProxyNeedsRelay(src) {
		return src, "", nil
	}

	key := ""
	now := time.Now()
	m.mu.Lock()
	if cached, ok := m.detected[src]; ok {
		if now.Before(cached.expiresAt) {
			key = cached.working
		} else {
			delete(m.detected, src)
		}
	}
	if key != "" {
		if localURL, ok := m.acquireExistingLocked(profileID, key); ok {
			m.mu.Unlock()
			return localURL, key, nil
		}
	}
	m.mu.Unlock()

	if key == "" {
		// Protocol-labelled provider lists are frequently wrong. Probe the
		// three standard protocols concurrently with a short bounded timeout;
		// serial 30-second probes multiplied startup time across 20 profiles.
		working, err := DetectWorkingStandardProxyConfig(src, &SpeedTestConfig{
			Timeout:    5 * time.Second,
			TCPTimeout: 3 * time.Second,
			URLs:       []string{defaultTestURL},
		})
		if err != nil {
			return "", "", fmt.Errorf("代理协议/认证验证失败；若已开启 VPN TUN，请将代理服务器 IP 加入 TUN 路由排除后重试: %w", err)
		}
		key = strings.TrimSpace(working)
		m.mu.Lock()
		m.detected[src] = detectedStandardProxy{working: key, expiresAt: now.Add(10 * time.Minute)}
		if localURL, ok := m.acquireExistingLocked(profileID, key); ok {
			m.mu.Unlock()
			return localURL, key, nil
		}
		m.mu.Unlock()
	}

	r, err := startStandardRelay(key)
	if err != nil {
		return "", "", err
	}

	m.mu.Lock()
	if existing := m.relays[key]; existing != nil {
		m.mu.Unlock()
		_ = r.Close()
		m.mu.Lock()
		if oldKey := m.refs[profileID]; oldKey == key {
			localURL := existing.localURL
			m.mu.Unlock()
			return localURL, key, nil
		} else if oldKey != "" {
			m.releaseLocked(profileID)
		}
		existing.refCount++
		m.refs[profileID] = key
		localURL := existing.localURL
		m.mu.Unlock()
		return localURL, key, nil
	}
	if oldKey := m.refs[profileID]; oldKey != "" && oldKey != key {
		m.releaseLocked(profileID)
	} else if oldKey == key {
		// A stale reference without a live relay must not inflate refCount.
		delete(m.refs, profileID)
	}
	r.refCount = 1
	m.relays[key] = r
	m.refs[profileID] = key
	localURL := r.localURL
	m.mu.Unlock()
	return localURL, key, nil
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
	m.mu.Unlock()
	for _, r := range relays {
		_ = r.Close()
	}
}

func startStandardRelay(src string) (*standardRelay, error) {
	mapping, err := proxyConfigToMapping(src)
	if err != nil {
		return nil, err
	}
	px, err := adapter.ParseProxy(mapping)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &standardRelay{
		key:      src,
		proxyURL: src,
		listen:   ln,
		localURL: "http://" + ln.Addr().String(),
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.handle(px, w, req) }),
		ReadHeaderTimeout: 15 * time.Second,
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
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
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
		DisableKeepAlives:     true,
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
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "socks5://") || strings.HasPrefix(l, "socks://")
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
