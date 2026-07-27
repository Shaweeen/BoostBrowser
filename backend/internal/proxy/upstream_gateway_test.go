package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNormalizeProxyNetworkMode(t *testing.T) {
	tests := map[string]string{
		"":              ProxyNetworkModeAuto,
		"AUTO":          ProxyNetworkModeAuto,
		"direct":        ProxyNetworkModeDirect,
		"local_gateway": ProxyNetworkModeLocalGateway,
		"tun":           ProxyNetworkModeTUN,
		"unknown":       ProxyNetworkModeAuto,
	}
	for input, want := range tests {
		if got := NormalizeProxyNetworkMode(input); got != want {
			t.Fatalf("NormalizeProxyNetworkMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeLocalGatewayURLRejectsRemoteHost(t *testing.T) {
	if _, err := NormalizeLocalGatewayURL("http://203.0.113.10:7890"); err == nil {
		t.Fatal("remote host must not be accepted as a local VPN gateway")
	}
	if _, err := NormalizeLocalGatewayURL("https://127.0.0.1:7890"); err == nil {
		t.Fatal("TLS-wrapped HTTP is not a supported local VPN gateway")
	}
	got, err := NormalizeLocalGatewayURL("127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected normalized gateway: %q", got)
	}
}

func TestLocalGatewayCandidatesPreferExplicitAndDeduplicate(t *testing.T) {
	t.Setenv("ALL_PROXY", "http://127.0.0.1:7890")
	t.Setenv("HTTPS_PROXY", "http://203.0.113.10:7890")
	t.Setenv("HTTP_PROXY", "")
	candidates := LocalGatewayCandidates("127.0.0.1:7890")
	if len(candidates) == 0 || candidates[0] != "http://127.0.0.1:7890" {
		t.Fatalf("explicit gateway was not preferred: %#v", candidates)
	}
	count := 0
	for _, candidate := range candidates {
		if candidate == "http://127.0.0.1:7890" {
			count++
		}
		if strings.Contains(candidate, "203.0.113.10") {
			t.Fatalf("remote environment proxy leaked into local candidates: %q", candidate)
		}
	}
	if count != 1 {
		t.Fatalf("gateway was not deduplicated: %#v", candidates)
	}
}

func TestHTTPUpstreamGatewayDialerUsesConnectAndAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverError := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		defer conn.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(conn))
		if readErr != nil {
			serverError <- readErr
			return
		}
		if request.Method != http.MethodConnect || request.Host != "proxy-provider.example:443" {
			serverError <- fmt.Errorf("unexpected CONNECT request: method=%s host=%s", request.Method, request.Host)
			return
		}
		if request.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			serverError <- fmt.Errorf("missing proxy authorization")
			return
		}
		_, writeErr := fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		serverError <- writeErr
	}()

	dialer, err := newUpstreamGatewayDialer("http://user:pass@" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "proxy-provider.example:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func TestStandardRelayChainsThroughLocalVPNGateway(t *testing.T) {
	finalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Proxy-Authorization") != "Basic cHJvdmlkZXI6c2VjcmV0" {
			http.Error(w, "missing provider authentication", http.StatusProxyAuthRequired)
			return
		}
		if request.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unavailable", http.StatusInternalServerError)
			return
		}
		conn, rw, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		if _, readErr := http.ReadRequest(rw.Reader); readErr != nil {
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 204 No Content\r\nX-Chain-Verified: true\r\nContent-Length: 0\r\n\r\n")
		_ = rw.Flush()
	}))
	defer finalProxy.Close()

	gatewayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayListener.Close()
	gatewayDone := make(chan error, 1)
	go func() {
		client, acceptErr := gatewayListener.Accept()
		if acceptErr != nil {
			gatewayDone <- acceptErr
			return
		}
		defer client.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(client))
		if readErr != nil {
			gatewayDone <- readErr
			return
		}
		upstream, dialErr := net.DialTimeout("tcp", request.Host, 2*time.Second)
		if dialErr != nil {
			gatewayDone <- dialErr
			return
		}
		defer upstream.Close()
		if _, writeErr := fmt.Fprint(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); writeErr != nil {
			gatewayDone <- writeErr
			return
		}
		copyDone := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(upstream, client); copyDone <- struct{}{} }()
		go func() { _, _ = io.Copy(client, upstream); copyDone <- struct{}{} }()
		<-copyDone
		gatewayDone <- nil
	}()

	finalURL, err := url.Parse(finalProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := startStandardRelay(
		"http://provider:secret@"+finalURL.Host,
		"http://"+gatewayListener.Addr().String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	localRelayURL, err := url.Parse(relay.localURL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(localRelayURL)},
		Timeout:   3 * time.Second,
	}
	response, err := client.Get("http://chain-test.invalid/ping")
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("X-Chain-Verified") != "true" {
		t.Fatalf("unexpected chained response: status=%d headers=%v body=%s", response.StatusCode, response.Header, responseBody)
	}
	_ = relay.Close()
	select {
	case err := <-gatewayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway tunnel did not close")
	}
}
