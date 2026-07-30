//go:build windows

package backend

import (
	"runtime"
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

// defaultEnvironmentMainWindowWidth/Height are applied only to the environment
// main Chrome frame once per user-initiated start. Popups, extension windows
// and sync-arranged tiles are never forced to this size.
const (
	defaultEnvironmentMainWindowWidth  = 1400
	defaultEnvironmentMainWindowHeight = 600
)

// enforceBrowserWindowBounds applies the startup main-window template once:
//   - only the environment's primary browser frame (not wallet/extension popups);
//   - only when the user starts an environment (caller is the start path);
//   - skipped while an input-sync session is active so tile/stack/horizontal
//     layouts fully follow the user-selected arrangement and confinement rules.
// There is no continuous worker: one SetWindowPos, then hand off.
func enforceBrowserWindowBounds(pid, width, height int) {
	if pid <= 0 || width <= 0 || height <= 0 {
		return
	}
	// Sync panel owns geometry while input sync is running.
	if inputSyncSessionActive() {
		return
	}
	hwnd := findMainEnvironmentBrowserWindow(pid)
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(uintptr(hwnd), swRestore)
	// Default on-screen placement for a free (non-sync) start only.
	procSetWindowPos.Call(uintptr(hwnd), 0, 80, 80, uintptr(width), uintptr(height), SWP_NOZORDER|SWP_SHOWWINDOW)
}

// enforceMainEnvironmentWindowOnStart is the only entry the start path should
// call. It never sizes popups or extension surfaces.
func enforceMainEnvironmentWindowOnStart(pid int) {
	enforceBrowserWindowBounds(pid, defaultEnvironmentMainWindowWidth, defaultEnvironmentMainWindowHeight)
}

func inputSyncSessionActive() bool {
	syncState.mu.Lock()
	active := syncState.active
	syncState.mu.Unlock()
	return active
}

// findMainEnvironmentBrowserWindow returns the primary Chromium frame for an
// environment process tree, excluding extension/wallet popups and DevTools.
func findMainEnvironmentBrowserWindow(rootPID int) windows.HWND {
	if rootPID <= 0 {
		return 0
	}
	// Prefer the root process, then children (Cloak may host the frame in a child).
	if hwnd := bestMainEnvironmentWindowForPID(rootPID); hwnd != 0 {
		return hwnd
	}
	children := snapshotProcessChildren()
	if children == nil {
		return 0
	}
	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]bool{rootPID: true}
	var best processWindowCandidate
	found := false
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if candidate, ok := bestMainEnvironmentWindowCandidateForPID(pid); ok {
			if !found || candidate.score > best.score {
				best = candidate
				found = true
			}
		}
		queue = append(queue, children[pid]...)
	}
	if !found {
		return 0
	}
	return best.hwnd
}

func bestMainEnvironmentWindowForPID(pid int) windows.HWND {
	candidate, ok := bestMainEnvironmentWindowCandidateForPID(pid)
	if !ok {
		return 0
	}
	return candidate.hwnd
}

func bestMainEnvironmentWindowCandidateForPID(pid int) (processWindowCandidate, bool) {
	search := &processWindowSearch{pid: pid}
	procEnumWindows.Call(processWindowEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)
	var best processWindowCandidate
	found := false
	for _, c := range search.candidates {
		title := getWindowTitle(c.hwnd)
		if !isMainEnvironmentBrowserFrame(c.hwnd, title) {
			continue
		}
		// Re-score: large main frames beat small popup-class surfaces that
		// still passed the generic Chrome window filter.
		score := c.score
		if looksLikeMainBrowserWindowTitle(title) {
			score += 50000
		}
		if w, h, ok := getClientSize(c.hwnd); ok {
			score += w * h / 100
		}
		c.score = score
		if !found || c.score > best.score {
			best = c
			found = true
		}
	}
	return best, found
}

func isMainEnvironmentBrowserFrame(hwnd windows.HWND, title string) bool {
	if hwnd == 0 {
		return false
	}
	// Never apply the startup template to extension/wallet popups or DevTools.
	if isCompactExtensionPopupTitle(title) || isDefinitiveExtensionPopupTitle(title) {
		return false
	}
	if looksLikeServiceWorkerDevToolsTitle(title) {
		return false
	}
	if isAuxiliaryIMEWindowTitleOrClass(title, getWindowClassName(hwnd)) {
		return false
	}
	w, h, ok := getClientSize(hwnd)
	if !ok {
		return false
	}
	// Typical extension popups are compact; the environment main page is a full
	// browser frame. Allow short stacked heights after multi-open, but reject
	// narrow popup-like surfaces unless the title clearly is a browser chrome frame.
	if w < 480 && !looksLikeMainBrowserWindowTitle(title) {
		return false
	}
	if h < 200 && w < 700 && !looksLikeMainBrowserWindowTitle(title) {
		return false
	}
	return true
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
