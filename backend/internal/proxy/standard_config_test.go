package proxy

import "testing"

func TestNormalizeStandardProxyConfigLooseFormats(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		defaultScheme string
		want          string
	}{
		{"space separated", "98.105.119.245 5494 eanhhzwz nzl9ev3e0qvw", "http", "http://eanhhzwz:nzl9ev3e0qvw@98.105.119.245:5494"},
		{"tab separated socks", "31.59.20.33\t6611\tbuyer\tsecret", "socks5", "socks5://buyer:secret@31.59.20.33:6611"},
		{"pipe separated", "proxy.example.com|8080|user|p@ss:word", "http", "http://user:p%40ss%3Aword@proxy.example.com:8080"},
		{"colon separated", "127.0.0.1:443:username:pass:word", "http", "http://username:pass%3Aword@127.0.0.1:443"},
		{"colon password with at", "127.0.0.1:443:username:p@ssword", "http", "http://username:p%40ssword@127.0.0.1:443"},
		{"standard auth first", "username:password@127.0.0.1:1080", "socks5", "socks5://username:password@127.0.0.1:1080"},
		{"unescaped url password", "http://username:p@ss#word/1@127.0.0.1:8080", "http", "http://username:p%40ss%23word%2F1@127.0.0.1:8080"},
		{"scheme token", "socks5 127.0.0.1 1080 user pass", "http", "socks5://user:pass@127.0.0.1:1080"},
		{"scheme alias", "socket://127.0.0.1:1080", "http", "socks5://127.0.0.1:1080"},
		{"ipv6", "http://user:pass@[2001:db8::10]:8080", "http", "http://user:pass@[2001:db8::10]:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeStandardProxyConfig(tt.input, tt.defaultScheme)
			if err != nil {
				t.Fatalf("NormalizeStandardProxyConfig() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeStandardProxyConfig() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeStandardProxyConfigRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{
		"",
		"127.0.0.1",
		"127.0.0.1:70000",
		"127.0.0.1 invalid user secret",
		"ftp://127.0.0.1:21",
	} {
		if got, err := NormalizeStandardProxyConfig(input, "http"); err == nil {
			t.Fatalf("NormalizeStandardProxyConfig(%q) unexpectedly returned %q", input, got)
		}
	}
}

func TestStandardProxyMappingDecodesCredentials(t *testing.T) {
	mapping, err := proxyConfigToMapping("http://user:p%40ss%3Aword@127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if mapping["username"] != "user" || mapping["password"] != "p@ss:word" {
		t.Fatalf("credentials were not decoded: %#v", mapping)
	}
}

func TestProxyEndpointExcludesCredentials(t *testing.T) {
	got, err := proxyEndpoint("socks5://user:secret@[2001:db8::10]:1080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "[2001:db8::10]:1080" {
		t.Fatalf("proxyEndpoint() = %q", got)
	}
}

func TestAlternateStandardProxyConfigsPreserveEndpointAndCredentials(t *testing.T) {
	source := "socks5://buyer:p%40ss%3Aword@198.105.119.245:5494"
	got := alternateStandardProxyConfigs(source)
	want := []string{
		"http://buyer:p%40ss%3Aword@198.105.119.245:5494",
		"https://buyer:p%40ss%3Aword@198.105.119.245:5494",
	}
	if len(got) != len(want) {
		t.Fatalf("alternate count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("alternate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAcquireExistingRelayDoesNotDoubleCountSameProfile(t *testing.T) {
	manager := NewStandardRelayManager()
	relay := &standardRelay{localURL: "http://127.0.0.1:12345", refCount: 1}
	manager.relays["upstream"] = relay
	manager.refs["profile-1"] = "upstream"

	if _, ok := manager.acquireExistingLocked("profile-1", "upstream"); !ok {
		t.Fatal("existing relay not found")
	}
	if relay.refCount != 1 {
		t.Fatalf("same profile inflated relay refCount to %d", relay.refCount)
	}
}
