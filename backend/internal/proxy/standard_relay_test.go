package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// TestStandardRelaySharedTransportReusesUpstream 验证 relay 的共享 upstream
// transport 会在多个 HTTP 请求间复用同一条上游代理连接（每请求新建 transport
// 会为每个 http:// 资源重新 TCP 握手 + 上游代理握手，导致加载慢且易在个别
// 连接上失败——X “Something went wrong”、Discord 卡加载的直接诱因）。
func TestStandardRelaySharedTransportReusesUpstream(t *testing.T) {
	var connects atomic.Int32

	// 目标服务器：接收绝对形式请求（relay 的上游隧道里透传的是 absolute-form）。
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok:"+r.RequestURI)
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}

	// 模拟上游代理：只接受 CONNECT 并转发到目标，统计建连次数。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		dst := r.Host
		if !strings.Contains(dst, ":") {
			dst += ":80"
		}
		up, err := net.Dial("tcp", dst)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
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
	defer upstream.Close()

	relay, err := startStandardRelay(upstream.URL, "")
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	defer relay.Close()

	proxyURL, err := url.Parse(relay.localURL)
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}

	for i := 0; i < 2; i++ {
		resp, err := client.Get(targetURL.String() + "/p" + fmt.Sprint(i))
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok:/p") {
			t.Fatalf("request %d bad response: %d %q", i, resp.StatusCode, body)
		}
	}

	if got := connects.Load(); got != 1 {
		t.Fatalf("upstream CONNECT count = %d, want 1 (upstream connection reuse broken)", got)
	}
}
