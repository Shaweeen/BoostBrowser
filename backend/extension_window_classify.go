package backend

import "strings"

// This file holds the platform-neutral window classification helpers. They are
// kept free of Win32 dependencies so the popup-vs-main-frame policy can be unit
// tested on any host and shared by the environment popup confiner, the tile
// path and the input-sync window resolution.

// environmentFrameLooksLikeMain classifies a Chrome top-level by client size +
// title. Size wins over title: a full browser frame whose tab title is
// "MetaMask" / a wallet brand is still the environment main window. Only
// tall portrait compact surfaces with popup titles are treated as extension hosts.
//
// Regression (1.7.72): title-first rejection made tile/sync return zero HWNDs
// whenever users had a wallet page open in the main tab ("没有可用的运行实例窗口").
func environmentFrameLooksLikeMain(w, h int, title string) bool {
	if w < 80 || h < 8 {
		return false
	}
	titlePopup := isCompactExtensionPopupTitle(title) || isDefinitiveExtensionPopupTitle(title)
	// Wallet Notification / confirm: tall portrait (height clearly exceeds width).
	// Landscape tile cells (e.g. 360×300 under 10-open) must NOT match this.
	if titlePopup && h > w && h >= 400 && w <= 560 {
		return false
	}
	// Tiny extension menus / attach chips.
	if titlePopup && w < 280 && h < 280 {
		return false
	}
	// Popup surfaces with STRONG signals (exact wallet popup titles, OAuth
	// authorization / request flows such as X 邮箱授权, extension prompts) in the
	// compact band must be genuinely large to be the environment main frame.
	// Real main frames carry the browser suffix (" - Google Chrome") and are
	// exempt, so a 320×260 MetaMask confirm or a 400×300 authorization popup is
	// never mistaken for the environment window that tile/sync should move.
	//
	// Deliberately NOT applied to broad keyword hits (looksLikeWalletExtensionPopup):
	// dense multi-open tile cells can be narrower than 480px while their active
	// tab is a wallet web page — rejecting those re-opens the 1.7.72 regression
	// where tile/sync returned "没有可用的运行实例窗口". The start-time registry
	// (P3-1) is the authoritative main-frame pointer for that path.
	if isDefinitiveExtensionPopupTitle(title) && !looksLikeMainBrowserWindowTitle(title) && (w < 480 || h < 320) {
		return false
	}
	// Multi-open tile cells on 1080p are ~340–480×280+ — always main frames,
	// including when the active tab title is a wallet product name.
	if w >= 260 && h >= 120 {
		return true
	}
	if looksLikeMainBrowserWindowTitle(title) && w >= 120 && h >= 80 {
		return true
	}
	if titlePopup {
		return false
	}
	return w >= 200 && h >= 100
}

// isAutoOpenedWalletHomepageTitle matches MV3 wallet Notification hosts.
func isAutoOpenedWalletHomepageTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "wallet") && strings.Contains(lower, "notification")
}

func isStrongExtensionPopupTitle(title string) bool {
	if title == "" {
		return false
	}

	// Wallet web pages often include the wallet brand in their tab title, e.g.
	// "Web3 入口，一个就够 - OKX Wallet" or
	// "The crypto wallet for DeFi... | MetaMask". Treat a brand-only title as a
	// popup, or require explicit popup cue words. Do not clamp every title that
	// merely contains a wallet brand.
	if isExactKnownWalletPopupTitle(title) {
		return true
	}
	return hasExtensionPopupCue(title)
}

func isExactKnownWalletPopupTitle(title string) bool {
	t := strings.TrimSpace(strings.ToLower(title))
	knownTitles := []string{
		"okx wallet",
		"metamask",
		"rabby",
		"rabby wallet",
		"phantom",
		"phantom wallet",
		"bitget wallet",
		"keplr",
		"keplr wallet",
		"petra",
		"petra wallet",
	}
	for _, known := range knownTitles {
		if t == known {
			return true
		}
	}
	return false
}

func hasExtensionPopupCue(title string) bool {
	t := strings.TrimSpace(strings.ToLower(title))
	cuePhrases := []string{
		"notification",
		"prompt",
		"login request",
		"sign in request",
		"connect request",
		"signature request",
		"登录请求",
		"登录授权",
		"sign request",
		"confirm transaction",
		"transaction request",
		"approve",
		"查看权限",
		"连接请求",
		"签名请求",
		"确认交易",
		"授权",
		"request",
		"permission",
		"allow",
	}
	for _, cue := range cuePhrases {
		if strings.Contains(t, cue) {
			return true
		}
	}
	return false
}

func looksLikeMainBrowserWindowTitle(title string) bool {
	t := strings.TrimSpace(strings.ToLower(title))
	if t == "" {
		return false
	}
	return strings.Contains(t, " - browserstudio") ||
		strings.HasSuffix(t, "browserstudio") ||
		strings.Contains(t, " - boost browser") ||
		strings.HasSuffix(t, "boost browser") ||
		strings.Contains(t, " - chromium") ||
		strings.HasSuffix(t, "chromium") ||
		strings.Contains(t, " - google chrome") ||
		strings.HasSuffix(t, "google chrome") ||
		strings.Contains(t, " - chrome") ||
		strings.HasSuffix(t, "chrome")
}

func isKnownWalletPopupProductTitle(title string) bool {
	t := strings.TrimSpace(strings.ToLower(title))
	for _, suffix := range []string{" - browserstudio", " - boost browser"} {
		if !strings.HasSuffix(t, suffix) {
			continue
		}
		product := strings.TrimSpace(strings.TrimSuffix(t, suffix))
		product = strings.TrimSpace(strings.TrimSuffix(product, " notification"))
		return isExactKnownWalletPopupTitle(product)
	}
	return false
}

func isCompactExtensionPopupTitle(title string) bool {
	return looksLikeWalletExtensionPopup(title) ||
		isStrongExtensionPopupTitle(title) ||
		isKnownWalletPopupProductTitle(title)
}

func isDefinitiveExtensionPopupTitle(title string) bool {
	return isStrongExtensionPopupTitle(title) || isKnownWalletPopupProductTitle(title)
}

func looksLikeServiceWorkerDevToolsTitle(title string) bool {
	if title == "" {
		return false
	}
	return strings.Contains(title, "service worker") ||
		strings.Contains(title, "devtools") ||
		strings.Contains(title, "developer tools") ||
		strings.Contains(title, "开发者工具")
}
