//go:build windows

package backend

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const swRestore = 9

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// enforceBrowserWindowBounds performs one startup-only SW_RESTORE + SetWindowPos
// after extension startup pages have been removed. It gives only the initial
// top-level browser frame the
// 1400x600 startup template without passing a process-wide --window-size flag
// that Chrome could reuse for later extension windows. There is no retry worker,
// timer or window state registry.
func enforceBrowserWindowBounds(pid, width, height int) {
	if pid <= 0 || width <= 0 || height <= 0 {
		return
	}
	hwnd, err := findProcessTreeWindow(pid)
	if err != nil || hwnd == 0 {
		return
	}
	procShowWindow.Call(uintptr(hwnd), swRestore)
	procSetWindowPos.Call(uintptr(hwnd), 0, 80, 80, uintptr(width), uintptr(height), SWP_NOZORDER|SWP_SHOWWINDOW)
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

func getWindowClassName(hwnd windows.HWND) string {
	buf := make([]uint16, 256)
	ret, _, _ := procGetClassNameW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func getWindowTitle(hwnd windows.HWND) string {
	length, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd))
	if length <= 0 {
		return ""
	}
	buf := make([]uint16, int(length)+1)
	procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

func getTopLevelWindowRect(hwnd windows.HWND) (winRect, bool) {
	var rect winRect
	ret, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	return rect, ret != 0
}
