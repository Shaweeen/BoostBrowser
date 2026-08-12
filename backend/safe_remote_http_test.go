package backend

import (
	"net/http"
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

func TestPublicRemoteHTTPClientOptionalLocalGateway(t *testing.T) {
	// Prefer fixed optional proxy (app LocalVPNProxy path). Do not rely on
	// http.ProxyFromEnvironment — it caches env at first process use and is
	// flaky under suite order / user-level HTTPS_PROXY on developer machines.
	client := newPublicRemoteHTTPClientWithProxy(5*time.Second, true, "http://127.0.0.1:17890")
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("expected http.Transport with Proxy func")
	}
	req, err := http.NewRequest(http.MethodGet, "https://clients2.google.com/service/update2/crx", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.Host != "127.0.0.1:17890" {
		t.Fatalf("expected fixed local gateway 127.0.0.1:17890, got %#v", proxyURL)
	}
	localReq, err := http.NewRequest(http.MethodGet, "https://127.0.0.1/secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Proxy(localReq); err == nil {
		t.Fatal("proxy selection must reject loopback destinations")
	}
}

func TestEnvHTTPProxyConfigured(t *testing.T) {
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
