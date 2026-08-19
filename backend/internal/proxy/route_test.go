package proxy

import "testing"

func TestShouldSkipChromeProxyOnLaunch(t *testing.T) {
	remote := "http://1.2.3.4:1080"
	local := "http://127.0.0.1:7897"
	cases := []struct {
		mode, src string
		skip      bool
	}{
		{ProxyNetworkModeTUN, "", true},
		{ProxyNetworkModeTUN, "direct://", true},
		{ProxyNetworkModeTUN, local, true},
		{ProxyNetworkModeTUN, remote, false},
		{ProxyNetworkModeTUN, "socks5://user:pass@10.0.0.8:1080", false},
		{ProxyNetworkModeAuto, remote, false},
		{ProxyNetworkModeLocalGateway, remote, false},
		{ProxyNetworkModeDirect, remote, false},
		{ProxyNetworkModeAuto, "", false},
	}
	for _, tc := range cases {
		if got := ShouldSkipChromeProxyOnLaunch(tc.mode, tc.src); got != tc.skip {
			t.Fatalf("ShouldSkipChromeProxyOnLaunch(%q, %q)=%v want %v", tc.mode, tc.src, got, tc.skip)
		}
	}
}

func TestLaunchRelayModePromotesTUNToGateway(t *testing.T) {
	if got := LaunchRelayMode(ProxyNetworkModeTUN); got != ProxyNetworkModeLocalGateway {
		t.Fatalf("TUN launch relay mode=%s", got)
	}
	if got := LaunchRelayMode(ProxyNetworkModeAuto); got != ProxyNetworkModeAuto {
		t.Fatalf("auto launch relay mode=%s", got)
	}
}
