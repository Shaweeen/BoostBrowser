package backend

import "testing"

func TestSanitizeNetscapeCookieFieldPreventsRowInjection(t *testing.T) {
	got := sanitizeNetscapeCookieField("value\twith\r\nnew-row")
	if got != "value with  new-row" {
		t.Fatalf("unexpected sanitized field: %q", got)
	}
}
