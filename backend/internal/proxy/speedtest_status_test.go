package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestUsableProxyResponseStatusRejectsProxyAuthentication(t *testing.T) {
	if isUsableProxyResponseStatus(http.StatusProxyAuthRequired) {
		t.Fatal("HTTP 407 must not be treated as working internet access")
	}
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusFound, http.StatusForbidden} {
		if !isUsableProxyResponseStatus(status) {
			t.Fatalf("expected status %d to prove proxy connectivity", status)
		}
	}
}

func TestPreferStandardProxyCandidateFavorsSocks5NearTie(t *testing.T) {
	socks := "socks5://user:pass@1.2.3.4:1080"
	httpURL := "http://user:pass@1.2.3.4:1080"
	// socks slightly slower but within 80ms → still prefer socks5
	if !preferStandardProxyCandidate(socks, TestResult{Ok: true, LatencyMs: 150}, httpURL, TestResult{Ok: true, LatencyMs: 120}) {
		t.Fatal("near-tie should prefer socks5 over http")
	}
	// large gap: faster http wins
	if preferStandardProxyCandidate(socks, TestResult{Ok: true, LatencyMs: 500}, httpURL, TestResult{Ok: true, LatencyMs: 100}) {
		t.Fatal("much faster http should win over slow socks5")
	}
}

func TestAlternateStandardProxyConfigsPreferSocksFallbackOrder(t *testing.T) {
	alts := alternateStandardProxyConfigs("socks5://1.2.3.4:1080")
	if len(alts) < 1 || !strings.HasPrefix(alts[0], "http://") {
		t.Fatalf("socks5 alternate should try http first, got %#v", alts)
	}
	// Bare line: socks5 before http
	bare := alternateStandardProxyConfigs("1.2.3.4:1080:user:pass")
	if len(bare) < 2 {
		t.Fatalf("bare line should produce socks5+http candidates: %#v", bare)
	}
	if !strings.HasPrefix(bare[0], "socks5://") {
		t.Fatalf("bare residential line should prefer socks5 first: %#v", bare)
	}
}

func TestSchemeOfProxyURL(t *testing.T) {
	if schemeOfProxyURL("SOCKS5://x:1") != "socks5" {
		t.Fatal("socks5 scheme parse")
	}
	if schemeOfProxyURL("http://x:1") != "http" {
		t.Fatal("http scheme parse")
	}
}
