package backend

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestValidatePublicRemoteURLRejectsLocal(t *testing.T) {
	if _, err := validatePublicRemoteURL("https://127.0.0.1/x", false); err == nil {
		t.Fatal("loopback must be rejected")
	}
	if _, err := validatePublicRemoteURL("https://example.com/x", false); err != nil {
		t.Fatal(err)
	}
}

func TestPublicRemoteHTTPClientUsesEnvProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PK\x03\x04fake"))
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	client := newPublicRemoteHTTPClient(5*time.Second, true)
	req, err := http.NewRequest(http.MethodGet, "http://example.com/extension.crx", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestEnvHTTPProxyConfigured(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	// Clear may not unset empty - force empty and check HTTPS only set later
	os.Unsetenv("HTTP_PROXY")
	os.Unsetenv("HTTPS_PROXY")
	os.Unsetenv("http_proxy")
	os.Unsetenv("https_proxy")
	os.Unsetenv("ALL_PROXY")
	os.Unsetenv("all_proxy")
	if envHTTPProxyConfigured() {
		t.Fatal("expected false")
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7897")
	if !envHTTPProxyConfigured() {
		t.Fatal("expected true")
	}
}
