package backend

import (
	"net"
	"testing"
)

func TestValidatePublicRemoteURLBlocksLocalTargets(t *testing.T) {
	tests := []string{
		"http://127.0.0.1:8080/sub",
		"https://localhost/file.crx",
		"https://10.0.0.8/file.crx",
		"https://[::1]/file.crx",
		"https://metadata.internal/latest",
	}
	for _, rawURL := range tests {
		if _, err := validatePublicRemoteURL(rawURL, true); err == nil {
			t.Fatalf("expected local target to be blocked: %s", rawURL)
		}
	}
}

func TestValidatePublicRemoteURLSchemePolicy(t *testing.T) {
	if _, err := validatePublicRemoteURL("https://example.com/file.crx", false); err != nil {
		t.Fatalf("public HTTPS URL should pass validation: %v", err)
	}
	if _, err := validatePublicRemoteURL("http://example.com/file.crx", false); err == nil {
		t.Fatal("extension HTTP URL should be rejected")
	}
	if _, err := validatePublicRemoteURL("http://example.com/sub.yaml", true); err != nil {
		t.Fatalf("subscription HTTP URL should remain supported: %v", err)
	}
}

func TestUnsafeRemoteIPIncludesCarrierGradeNAT(t *testing.T) {
	if !isUnsafeRemoteIP(net.ParseIP("100.64.1.2")) {
		t.Fatal("carrier-grade NAT address should be blocked")
	}
	if isUnsafeRemoteIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address should not be blocked")
	}
}
