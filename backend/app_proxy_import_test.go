package backend

import (
	"testing"
	"time"
)

func TestParseSubscriptionUserInfo(t *testing.T) {
	expire := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	raw := "upload=100; download=250; total=1000; expire=" + formatInt64(expire.Unix())
	info := parseSubscriptionUserInfo(raw)
	if info["usedBytes"] != int64(350) || info["remainingBytes"] != int64(650) {
		t.Fatalf("unexpected traffic info: %#v", info)
	}
	if info["expireAt"] != expire.Format(time.RFC3339) {
		t.Fatalf("unexpected expiry: %#v", info)
	}
}

func TestParseSubscriptionUserInfoIgnoresInvalidValues(t *testing.T) {
	info := parseSubscriptionUserInfo("upload=-1; download=bad; total=500")
	if info["usedBytes"] != int64(0) || info["remainingBytes"] != int64(500) || info["expireAt"] != "" {
		t.Fatalf("invalid header was not handled safely: %#v", info)
	}
}

func formatInt64(value int64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for value > 0 {
		buf = append(buf, digits[value%10])
		value /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
