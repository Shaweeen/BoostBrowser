package backend

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBrowserDebugAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	livePort := 0
	if _, err := fmt.Sscanf(u.Port(), "%d", &livePort); err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	if !browserDebugAlive(livePort) {
		t.Fatalf("browserDebugAlive(%d) = false, want true (live CDP endpoint)", livePort)
	}

	// 取一个已释放的端口模拟“无浏览器进程”的场景。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve dead port: %v", err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if browserDebugAlive(deadPort) {
		t.Fatalf("browserDebugAlive(%d) = true, want false (no listener)", deadPort)
	}
	if browserDebugAlive(0) {
		t.Fatalf("browserDebugAlive(0) = true, want false (invalid port)")
	}
}
