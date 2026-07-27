package proxy

import (
	"net/http"
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
