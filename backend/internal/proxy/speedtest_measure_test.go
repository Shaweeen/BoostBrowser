package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
)

// TestSingleHTTPProxyTestReportsPureRTT 验证延迟测量不再包含建连耗时：
// 上游代理 CONNECT 拨号故意延迟 300ms，预热请求承担该延迟，第二次请求
// 复用同一连接测出的延迟应显著小于 300ms（纯 HTTP RTT，与 Clash
// unified-delay 一致）。修复前会把完整建连+请求耗时当延迟，慢速住宅 IP
// 在代理池里显示成 1300-2400ms，而真实往返只有 400-800ms。
func TestSingleHTTPProxyTestReportsPureRTT(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	// 模拟拨号慢的 HTTP 代理：CONNECT 前 sleep 300ms，然后隧道到目标。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		time.Sleep(300 * time.Millisecond)
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

	mapping, err := proxyConfigToMapping(upstream.URL)
	if err != nil {
		t.Fatalf("build proxy mapping: %v", err)
	}
	px, err := adapter.ParseProxy(mapping)
	if err != nil {
		t.Fatalf("parse proxy: %v", err)
	}

	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	testURL := u.String() + "/t"

	result := singleHTTPProxyTest("t", px, testURL, http.MethodGet, 5*time.Second)
	if !result.Ok {
		t.Fatalf("proxy should be usable, got error: %s", result.Error)
	}
	if result.LatencyMs >= 250 {
		t.Fatalf("latency %dms should exclude the 300ms dial (pure RTT expected, <250ms)", result.LatencyMs)
	}
}
