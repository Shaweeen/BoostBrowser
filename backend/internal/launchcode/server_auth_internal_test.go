package launchcode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildHandlerRejectsNonLocalRequestBeforeAPIAuth(t *testing.T) {
	srv := NewLaunchServer(NewLaunchCodeService(NewMemoryLaunchCodeDAO()), nil, nil, 0)
	srv.SetAPIAuthConfig(APIAuthConfig{
		Enabled: true,
		APIKey:  "secret-key",
		Header:  "X-Test-Api-Key",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "10.0.0.8:3456"
	w := httptest.NewRecorder()
	srv.buildHandler(true).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("非 localhost 请求应优先返回 403: got=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "forbidden: only localhost is allowed") {
		t.Fatalf("错误信息不正确: %s", w.Body.String())
	}
}

func TestBuildHandlerRejectsDNSRebindingHost(t *testing.T) {
	srv := NewLaunchServer(NewLaunchCodeService(NewMemoryLaunchCodeDAO()), nil, nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "127.0.0.1:3456"
	req.Host = "attacker.example"
	w := httptest.NewRecorder()
	srv.buildHandler(true).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "invalid localhost host header") {
		t.Fatalf("DNS rebinding Host should be rejected: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBuildHandlerRejectsCrossOriginCDPRequest(t *testing.T) {
	srv := NewLaunchServer(NewLaunchCodeService(NewMemoryLaunchCodeDAO()), nil, nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/json/version", nil)
	req.RemoteAddr = "127.0.0.1:3456"
	req.Host = "127.0.0.1:19876"
	req.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	srv.buildHandler(true).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "cross-origin") {
		t.Fatalf("cross-origin CDP request should be rejected: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBuildHandlerAllowsAuthenticatedChromeExtensionAPIOrigin(t *testing.T) {
	srv := NewLaunchServer(NewLaunchCodeService(NewMemoryLaunchCodeDAO()), nil, nil, 0)
	srv.SetAPIAuthConfig(APIAuthConfig{Enabled: true, APIKey: "secret-key"})
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "127.0.0.1:3456"
	req.Host = "127.0.0.1:19876"
	req.Header.Set("Origin", "chrome-extension://abcdefghijklmnopabcdefghijklmnop")
	req.Header.Set(DefaultAPIKeyHeader, "secret-key")
	w := httptest.NewRecorder()
	srv.buildHandler(true).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated helper extension should be allowed: code=%d body=%s", w.Code, w.Body.String())
	}
}
