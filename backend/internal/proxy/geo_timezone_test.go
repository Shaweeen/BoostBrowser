package proxy

import "testing"

func TestTimezoneHintFromCountry(t *testing.T) {
	if got := TimezoneHintFromCountry("US"); got != "America/New_York" {
		t.Fatalf("US: %q", got)
	}
	if got := TimezoneHintFromCountry("日本"); got != "Asia/Tokyo" {
		t.Fatalf("日本: %q", got)
	}
	if got := TimezoneHintFromCountry("Germany"); got != "Europe/Berlin" {
		t.Fatalf("Germany: %q", got)
	}
	if got := TimezoneHintFromCountry(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}
