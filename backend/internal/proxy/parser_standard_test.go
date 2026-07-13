package proxy

import "testing"

func TestParseClashStandardProxyFormatsIPv6Host(t *testing.T) {
	standard, outbound, err := ParseProxyNode(`
name: ipv6-socks
type: socks5
server: 2001:db8::10
port: 1080
username: buyer
password: secret
`)
	if err != nil {
		t.Fatal(err)
	}
	if outbound != nil {
		t.Fatalf("standard proxy should not create bridge outbound: %#v", outbound)
	}
	if standard != "socks5://buyer:secret@[2001:db8::10]:1080" {
		t.Fatalf("unexpected IPv6 proxy URL: %q", standard)
	}
}
