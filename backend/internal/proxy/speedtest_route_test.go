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
