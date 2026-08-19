package proxy

import "strings"

// ShouldSkipChromeProxyOnLaunch is true only when TUN is the intended
// system-wide exit and this environment has no assigned remote node.
// A bound residential HTTP/SOCKS URL must still reach that node (via the
// loopback gateway relay) so "one environment = one pool IP" holds under Clash TUN.
func ShouldSkipChromeProxyOnLaunch(mode, resolvedConfig string) bool {
	if NormalizeProxyNetworkMode(mode) != ProxyNetworkModeTUN {
		return false
	}
	src := strings.TrimSpace(resolvedConfig)
	if src == "" || strings.EqualFold(src, "direct://") {
		return true
	}
	return isLocalProxyURL(src)
}

// LaunchRelayMode is the relay/probe mode used when Chrome still gets a
// --proxy-server. TUN + a remote assigned node uses local_gateway so the
// first hop is Clash mixed (loopback, not TUN-captured).
func LaunchRelayMode(mode string) string {
	normalized := NormalizeProxyNetworkMode(mode)
	if normalized == ProxyNetworkModeTUN {
		return ProxyNetworkModeLocalGateway
	}
	return normalized
}
