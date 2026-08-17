package proxy

import (
	"fmt"
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

// TestSingleHTTPProxyTestSlowDialStaysUsable 复现代理池里慢速住宅 SOCKS/HTTP
// 节点的场景：上游 CONNECT 拨号很慢（接近 3s 请求预算）。预热请求承担拨号
// 耗时，计时请求可能超时——此时必须回退判定可用，绝不能把慢节点误判成
// “超时/不可用”。
func TestSingleHTTPProxyTestSlowDialStaysUsable(t *testing.T) {
	delays := []time.Duration{1200 * time.Millisecond, 1800 * time.Millisecond, 2400 * time.Millisecond}
	for _, delay := range delays {
		t.Run(fmt.Sprintf("dial-%dms", delay.Milliseconds()), func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer target.Close()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect {
					http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
					return
				}
				time.Sleep(delay)
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
				t.Fatalf("mapping: %v", err)
			}
			px, err := adapter.ParseProxy(mapping)
			if err != nil {
				t.Fatalf("parse proxy: %v", err)
			}
			u, err := url.Parse(target.URL)
			if err != nil {
				t.Fatalf("parse target: %v", err)
			}

			// 与 robustHTTPProxyTest 相同的 3s 单请求预算。
			result := singleHTTPProxyTest("t", px, u.String()+"/t", http.MethodGet, 3*time.Second)
			if !result.Ok {
				t.Fatalf("slow dial %s: proxy must stay usable, got error: %s", delay, result.Error)
			}
		})
	}
}
