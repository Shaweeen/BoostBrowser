package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"boost-browser/backend/internal/config"
)

// startConnectProxy 启动一个极简 HTTP CONNECT 代理（本地 Clash 网关 / 池里的
// http 节点都长这样），统计经过它的 CONNECT 连接数。
func startConnectProxy(t *testing.T) (string, *int32) {
	t.Helper()
	return startConnectProxyWithRewrite(t, nil)
}

// startConnectProxyWithRewrite 同 startConnectProxy，但支持对 CONNECT 目标
// 做重写（模拟“直连被 TUN 截断、但经网关可达真实节点”的场景）。
// rewrite 返回空串表示不改写；返回非空串则替换 CONNECT 目标地址。
func startConnectProxyWithRewrite(t *testing.T, rewrite func(hostport string) string) (string, *int32) {
	t.Helper()
	var conns int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			// Go 的 http.Transport 对 http 目标会发绝对 URI 的 GET（经代理）；
			// 直接回 204 视为节点可用。
			w.WriteHeader(http.StatusNoContent)
			return
		}
		atomic.AddInt32(&conns, 1)
		dst := r.Host
		if !strings.Contains(dst, ":") {
			dst += ":443"
		}
		// 网关探测目标（www.gstatic.com:80）：不劫持，Go 服务器自动回 200，
		// 探测即可离线通过，不依赖外网。
		if strings.HasPrefix(dst, "www.gstatic.com:") {
			return
		}
		if rewrite != nil {
			if rewritten := rewrite(dst); rewritten != "" {
				dst = rewritten
			}
		}
		up, err := net.Dial("tcp", dst)
		if err != nil {
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		client, bufrw, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		_, _ = bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = bufrw.Flush()
		go io.Copy(up, client)
		go io.Copy(client, up)
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL, &conns
}

// startTestGateway 启动本地“Clash 网关”并返回其 URL 与连接计数。
func startTestGateway(t *testing.T) (string, *int32) {
	t.Helper()
	return startConnectProxy(t)
}

// TestSpeedTestLocalGatewayRoutesThroughGateway 验证 local_gateway 模式下，
// 代理池测速必须经本地 VPN 网关拨号（与环境的 relay 一致），而不是直连。
// 修复前池页永远直连：本地 Clash 开 TUN 时直连被截获 → 池页全部“超时/不可用”。
func TestSpeedTestLocalGatewayRoutesThroughGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	gatewayURL, gatewayConns := startTestGateway(t)

	// 节点 = 本地 http 节点（CONNECT 代理，模拟代理池里的 http 节点）。
	nodeURL, _ := startConnectProxy(t)

	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: nodeURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeLocalGateway,
		LocalGatewayURL: gatewayURL,
	})
	if !result.Ok {
		t.Fatalf("local_gateway mode should succeed via gateway, got: %s", result.Error)
	}
	if atomic.LoadInt32(gatewayConns) == 0 {
		t.Fatal("local_gateway mode must route the probe through the local VPN gateway")
	}
}

// TestSpeedTestLocalGatewayNoGatewayFails 验证 local_gateway 模式在网关不可用时
// 明确报错，而不是静默直连（否则与环境的 relay 路径不一致，误导用户）。
func TestSpeedTestLocalGatewayNoGatewayFails(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	nodeURL, _ := startConnectProxy(t)

	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: nodeURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeLocalGateway,
		LocalGatewayURL: "http://127.0.0.1:1", // 没有服务监听
	})
	if result.Ok {
		t.Fatal("local_gateway mode without a working gateway must fail")
	}
	if !strings.Contains(result.Error, "本地 VPN 网关") {
		t.Fatalf("error should mention the local gateway, got: %s", result.Error)
	}
}

// TestSpeedTestAutoDirectWithoutGateway 验证 auto 模式默认直连即可工作
// （无网关依赖，保持老行为）。
func TestSpeedTestAutoDirectWithoutGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	nodeURL, _ := startConnectProxy(t)

	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: nodeURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode: ProxyNetworkModeAuto,
	})
	if !result.Ok {
		t.Fatalf("auto mode direct probe should succeed, got: %s", result.Error)
	}
}

// handleSocks5Conn 处理一个 SOCKS5 CONNECT 会话（无认证）。
func handleSocks5Conn(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 260)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	// 读取全部 method 字节（greeting = version + nmethods + methods），
	// 只读 2 字节会让 method 残留到请求头解析，导致握手错位后 RST。
	nmethods := int(buf[1])
	if nmethods > 0 {
		if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
			return
		}
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 {
		return
	}
	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain
		var nlen [1]byte
		if _, err := io.ReadFull(conn, nlen[:]); err != nil {
			return
		}
		name := make([]byte, nlen[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return
		}
		host = string(name)
	case 0x04: // IPv6
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		return
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	up, err := net.Dial("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		up.Close()
		return
	}
	// 隧道由 relay goroutine 持有连接；不能在函数返回时 defer Close，
	// 否则握手刚完成隧道就被关闭，HTTP 交换立刻中断。
	relaySocks := func() {
		defer up.Close()
		defer conn.Close()
		_, _ = io.Copy(up, conn)
	}
	relayTarget := func() {
		defer up.Close()
		defer conn.Close()
		_, _ = io.Copy(conn, up)
	}
	go relaySocks()
	go relayTarget()
}

func startSocks5Node(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks5 listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSocks5Conn(conn)
		}
	}()
	return "socks5://" + ln.Addr().String()
}

// TestSpeedTestSocks5NodeViaGateway 验证 SOCKS5 节点在 local_gateway 模式下
// 经本地网关拨号可正常测速（用户代理池里最常见的节点类型）。
func TestSpeedTestSocks5NodeViaGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	gatewayURL, gatewayConns := startTestGateway(t)

	nodeURL := startSocks5Node(t)
	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: nodeURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeLocalGateway,
		LocalGatewayURL: gatewayURL,
	})
	if !result.Ok {
		t.Fatalf("socks5 node via gateway should be usable, got: %s", result.Error)
	}
	if atomic.LoadInt32(gatewayConns) == 0 {
		t.Fatal("socks5 probe must route through the local VPN gateway")
	}
}

// TestSpeedTestTUNModeFallsBackToGateway 验证 tun 模式下“直连不可达、经网关可
// 达”时测速成功 —— 模拟本地 Clash TUN 截获直连（节点 IP 从代理出口连不上）而
// 网关（回环地址不被 TUN 截获）可达。v1.7.129 只给 auto 加了网关回退，tun 模式
// 仍只直连 → 用户开 TUN 时池页全部“超时/不可用”的回归。
func TestSpeedTestTUNModeFallsBackToGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	// 节点 = 本地 http CONNECT 节点（模拟代理池里的 http 节点）。
	nodeURL, nodeConns := startConnectProxy(t)
	nodeHost := strings.TrimPrefix(nodeURL, "http://")

	// 本地“Clash 网关”：CONNECT 到 node-unreachable.test 时重写到真实节点地址，
	// 模拟“直连被 TUN 截断，但经网关可达真实节点”。
	gatewayURL, gatewayConns := startConnectProxyWithRewrite(t, func(hostport string) string {
		if strings.HasPrefix(hostport, "node-unreachable.test:") {
			return nodeHost
		}
		return ""
	})

	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: "http://node-unreachable.test:39876"}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeTUN,
		LocalGatewayURL: gatewayURL,
	})
	if !result.Ok {
		t.Fatalf("tun mode with dead direct should succeed via gateway, got: %s", result.Error)
	}
	if atomic.LoadInt32(gatewayConns) == 0 {
		t.Fatal("tun mode must route the probe through the local VPN gateway when direct is blocked")
	}
	if atomic.LoadInt32(nodeConns) == 0 {
		t.Fatal("probe should have reached the real node through the gateway tunnel")
	}
}

// TestSpeedTestTUNModeFallsBackToDirectWithoutGateway 验证 tun 模式在本地网关
// 不可用（Clash 未运行等）时回退直连，节点仍然可用。
func TestSpeedTestTUNModeFallsBackToDirectWithoutGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	nodeURL, _ := startConnectProxy(t)
	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: nodeURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeTUN,
		LocalGatewayURL: "http://127.0.0.1:1", // 没有服务监听
	})
	if !result.Ok {
		t.Fatalf("tun mode without a gateway should fall back to direct, got: %s", result.Error)
	}
}

// TestSpeedTestDirectModeIgnoresGateway 验证 direct 模式永远直连，即使本地有
// 可用网关也不经网关拨号（用户明确选择直连，不做任何回退）。
func TestSpeedTestDirectModeIgnoresGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	gatewayURL, gatewayConns := startTestGateway(t)
	nodeURL, _ := startConnectProxy(t)
	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: nodeURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeDirect,
		LocalGatewayURL: gatewayURL,
	})
	if !result.Ok {
		t.Fatalf("direct mode should succeed by dialing the node directly, got: %s", result.Error)
	}
	if atomic.LoadInt32(gatewayConns) != 0 {
		t.Fatal("direct mode must never contact the local VPN gateway")
	}
}

// TestIPHealthTUNModeViaGateway 验证 IP 健康检测在 tun 模式下先经本地网关获取
// 节点真实出口 IP（直连在 TUN 下会被截获，出口会变成 Clash 节点而非代理池节点）。
func TestIPHealthTUNModeViaGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"198.51.100.7","country":"US"}`))
	}))
	defer target.Close()

	oldURL := defaultIPPureInfoURL
	defaultIPPureInfoURL = target.URL
	defer func() { defaultIPPureInfoURL = oldURL }()

	// IP 健康检测用 socks5 节点：Go 的 http.Transport 对 http 节点 + http 目标
	// 发绝对 URI GET（代理直接回 204 不转发），socks5 节点才会经网关隧道真正
	// CONNECT 到目标并返回 JSON。
	nodeURL := startSocks5Node(t)
	nodeHost := strings.TrimPrefix(nodeURL, "socks5://")
	gatewayURL, gatewayConns := startConnectProxyWithRewrite(t, func(hostport string) string {
		if strings.HasPrefix(hostport, "node-unreachable.test:") {
			return nodeHost
		}
		return ""
	})

	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: "socks5://node-unreachable.test:39876"}}
	data, err := FetchIPPureInfo("p1", proxies, nil, nil, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeTUN,
		LocalGatewayURL: gatewayURL,
	})
	if err != nil {
		t.Fatalf("tun mode IP health should succeed via gateway, got: %v", err)
	}
	if atomic.LoadInt32(gatewayConns) == 0 {
		t.Fatal("tun mode IP health must route through the local VPN gateway")
	}
	// data["ip"] 只能来自经网关隧道 → 真实节点 → 目标服务器的返回，
	// 证明探测确实穿透了整条链路。
	if data["ip"] != "198.51.100.7" {
		t.Fatalf("unexpected exit ip data: %v", data["ip"])
	}
}

// TestIsLocalGatewaySource 验证“节点就是本地网关本身”的判定：仅回环地址且
// host:port 与检测到的网关完全一致时才命中；其它回环节点与非回环节点不命中。
func TestIsLocalGatewaySource(t *testing.T) {
	gatewayURL, _ := startTestGateway(t)
	gatewayHostPort := strings.TrimPrefix(gatewayURL, "http://")

	// 节点 == 网关（scheme 不同也算，比较的是 host:port）
	if !isLocalGatewaySource("socks5://"+gatewayHostPort, gatewayURL) {
		t.Fatal("same host:port as the gateway must be detected as the gateway itself")
	}

	// 其它回环节点（不同端口）：不是网关，不命中
	other, _ := startConnectProxy(t)
	if isLocalGatewaySource(other, gatewayURL) {
		t.Fatal("a different loopback port must not be treated as the gateway")
	}

	// 非回环节点：不命中（即使配置了网关）
	if isLocalGatewaySource("http://198.51.100.7:6396", gatewayURL) {
		t.Fatal("a non-loopback node must not be treated as the gateway")
	}

	// 网关不可用时不命中
	if isLocalGatewaySource("http://"+gatewayHostPort, "http://127.0.0.1:1") {
		t.Fatal("unavailable gateway must not match")
	}
}

// TestSpeedTestNodeEqualsGatewayTestable 验证节点恰好就是本地网关本身时
// （内置「本地代理」行即指向本地 Clash 网关），local_gateway / tun 模式仍能
// 测通——直接直连本机服务，避免经网关测自己（自环/双跳）。
func TestSpeedTestNodeEqualsGatewayTestable(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	gatewayURL, _ := startTestGateway(t)

	// 节点 == 网关本身
	proxies := []config.BrowserProxy{{ProxyId: "p1", ProxyName: "p1", ProxyConfig: gatewayURL}}
	cfg := &SpeedTestConfig{Timeout: 5 * time.Second, TCPTimeout: 3 * time.Second, URLs: []string{target.URL}}
	for _, mode := range []string{ProxyNetworkModeLocalGateway, ProxyNetworkModeTUN, ProxyNetworkModeAuto} {
		result := SpeedTest("p1", proxies, nil, nil, cfg, SpeedTestRouteOptions{
			Mode:            mode,
			LocalGatewayURL: gatewayURL,
		})
		if !result.Ok {
			t.Fatalf("%s mode: node == gateway should be testable, got: %s", mode, result.Error)
		}
	}
}

// TestIPHealthClientViaGateway 验证 IP 健康检测在 local_gateway 模式下同样
// 经本地网关拨号（修复前它与测速一样永远直连，TUN 场景下同样误报超时）。
func TestIPHealthClientViaGateway(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	gatewayURL, gatewayConns := startTestGateway(t)

	nodeURL, _ := startConnectProxy(t)

	client, err := buildProxyHTTPClient(nodeURL, "p1", nil, nil, nil, 5*time.Second, SpeedTestRouteOptions{
		Mode:            ProxyNetworkModeLocalGateway,
		LocalGatewayURL: gatewayURL,
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("request via gateway should succeed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if atomic.LoadInt32(gatewayConns) == 0 {
		t.Fatal("IP health client must route through the local VPN gateway")
	}
}
