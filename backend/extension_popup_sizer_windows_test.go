//go:build windows

package backend

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestIsMainEnvironmentBrowserFrameRejectsPopupsAndZero(t *testing.T) {
	if isMainEnvironmentBrowserFrame(0, "anything") {
		t.Fatal("zero hwnd must not be a main frame")
	}
	// Title-only rejection paths (hwnd non-zero is required for size checks;
	// popup titles are rejected before size when title matches).
	if !isCompactExtensionPopupTitle("MetaMask") {
		t.Fatal("precondition: MetaMask is a popup title")
	}
}

func TestEnvironmentFrameLooksLikeMainAcceptsWalletTitledBrowser(t *testing.T) {
	// Full env window with MetaMask open as the active tab must still tile/sync.
	if !environmentFrameLooksLikeMain(900, 700, "MetaMask") {
		t.Fatal("large wallet-titled browser must count as main frame")
	}
	if !environmentFrameLooksLikeMain(360, 300, "MetaMask") {
		t.Fatal("tiled multi-open cell with wallet tab title must count as main")
	}
	// Classic tall notification host stays a popup.
	if environmentFrameLooksLikeMain(360, 600, "MetaMask") {
		t.Fatal("portrait wallet notification must not count as main frame")
	}
	if environmentFrameLooksLikeMain(390, 620, "Rabby Wallet Notification") {
		t.Fatal("notification host must not count as main frame")
	}
	// Empty-title wide tile cell (Cloak / custom core).
	if !environmentFrameLooksLikeMain(480, 340, "") {
		t.Fatal("empty-title tile cell must count as main frame")
	}
	if environmentFrameLooksLikeMain(100, 80, "MetaMask") {
		t.Fatal("tiny popup must not count as main")
	}
}

func TestClampRectToBoundsLiftsPopupAboveTaskbar(t *testing.T) {
	// Work area ends at y=1000 (taskbar below). Popup 400x600 placed too low.
	_, y, w, h := clampRectToBounds(100, 700, 400, 600, 0, 0, 1920, 1000, false)
	if y+h > 1000 {
		t.Fatalf("popup bottom must stay above taskbar: y=%d h=%d", y, h)
	}
	if y != 400 { // 1000-600
		t.Fatalf("expect lift to y=400, got %d", y)
	}
	if w != 400 || h != 600 {
		t.Fatalf("position-only must keep size: %dx%d", w, h)
	}
	// With resize, taller-than-work popup shrinks.
	_, y2, _, h2 := clampRectToBounds(0, 0, 300, 2000, 0, 0, 1920, 1000, true)
	if h2 > 1000 || y2 != 0 {
		t.Fatalf("resized into work area: y=%d h=%d", y2, h2)
	}
}

func TestWalletNotificationNeverForceFitsSize(t *testing.T) {
	// Rabby/MetaMask Notification hosts blank permanently if resized mid-paint.
	if !isWalletNotificationHostTitle("Rabby Wallet Notification") {
		t.Fatal("Rabby Wallet Notification must be classified as notification host")
	}
	if !isWalletNotificationHostTitle("MetaMask Notification") {
		t.Fatal("MetaMask Notification must be classified as notification host")
	}
	if isWalletNotificationHostTitle("MetaMask") {
		t.Fatal("plain wallet product title is not a notification host")
	}
	// Position-only: natural size kept even when larger than cell.
	x, y, width, height, changed := constrainSyncPopupRectOptions(
		winRect{Left: -40, Top: 20, Right: 400, Bottom: 700}, // 440x680
		winRect{Left: 0, Top: 0, Right: 420, Bottom: 560},    // available 416x556
		2,
		false,
	)
	if width != 440 || height != 680 {
		t.Fatalf("notification must keep natural size: %dx%d", width, height)
	}
	if x != 2 || y != 2 {
		t.Fatalf("notification origin should pin into cell: %d,%d changed=%v", x, y, changed)
	}
	if !changed {
		t.Fatal("spilled notification origin must still move into cell")
	}
}

func TestPopupForceFitGraceBlocksImmediateResize(t *testing.T) {
	// Fresh hwnd → grace → no force-fit (popupAllowForceFitResize false until 2s).
	// Use a fake hwnd value that is not a real window; notePopupFirstSeen still records it.
	hwnd := windows.HWND(0xBEEF)
	popupFirstSeenMu.Lock()
	delete(popupFirstSeen, hwnd)
	popupFirstSeenMu.Unlock()
	if popupAllowForceFitResize(hwnd, "MetaMask") {
		t.Fatal("new popup must not force-fit during open grace")
	}
	if popupAllowForceFitResize(hwnd, "Rabby Wallet Notification") {
		t.Fatal("notification host must never allow force-fit")
	}
	// After grace: product title may force-fit; notification still blocked.
	popupFirstSeenMu.Lock()
	popupFirstSeen[hwnd] = time.Now().Add(-popupForceFitGrace - time.Second)
	popupFirstSeenMu.Unlock()
	if !popupAllowForceFitResize(hwnd, "MetaMask") {
		t.Fatal("after grace, non-notification popup may force-fit")
	}
	if popupAllowForceFitResize(hwnd, "Rabby Wallet Notification") {
		t.Fatal("notification host stays position-only after grace")
	}
	popupFirstSeenMu.Lock()
	delete(popupFirstSeen, hwnd)
	popupFirstSeenMu.Unlock()
}

func TestSyncPopupUsesCurrentArrangedOwnerBounds(t *testing.T) {
	// Oversized wallet inside cell → force-fit to available (816×556 after inset).
	x, y, width, height, changed := constrainSyncPopupRect(
		winRect{Left: 20, Top: 20, Right: 1460, Bottom: 920},
		winRect{Left: 0, Top: 0, Right: 820, Bottom: 560},
		2,
	)
	if !changed || width != 816 || height != 556 {
		t.Fatalf("oversized wallet must force-fit cell: x=%d y=%d %dx%d changed=%v", x, y, width, height, changed)
	}
	if x != 2 || y != 2 {
		// After shrink, origin must sit inside inset cell.
		t.Fatalf("origin after force-fit: x=%d y=%d", x, y)
	}

	// Origin outside + oversized → pull into inset and shrink to cell.
	x, y, width, height, changed = constrainSyncPopupRect(
		winRect{Left: -200, Top: -80, Right: 1240, Bottom: 820},
		winRect{Left: 0, Top: 0, Right: 820, Bottom: 560},
		2,
	)
	if !changed || x != 2 || y != 2 || width != 816 || height != 556 {
		t.Fatalf("outside oversized must fit cell: x=%d y=%d %dx%d changed=%v", x, y, width, height, changed)
	}
}

func TestSyncPopupShrinksTallWalletIntoCell(t *testing.T) {
	// Tall wallet exceeds cell height → force scale height to available.
	x, y, width, height, changed := constrainSyncPopupRect(
		winRect{Left: 50, Top: 50, Right: 410, Bottom: 710}, // 360x660
		winRect{Left: 0, Top: 0, Right: 500, Bottom: 400},   // available 496x396
		2,
	)
	if width != 360 || height != 396 {
		t.Fatalf("tall wallet must shrink height into cell: %dx%d", width, height)
	}
	if x != 50 || y != 2 {
		// Y pulled so height fits; X stays if still inside.
		t.Fatalf("origin after tall shrink: %d,%d changed=%v", x, y, changed)
	}
}

func TestSyncPopupPreservesExtensionNaturalSizeWhenItFits(t *testing.T) {
	x, y, width, height, changed := constrainSyncPopupRect(
		winRect{Left: 120, Top: 80, Right: 560, Bottom: 700},
		winRect{Left: 0, Top: 0, Right: 1600, Bottom: 900},
		2,
	)
	if changed {
		t.Fatalf("an in-bounds popup must keep the extension's natural geometry: %d,%d %dx%d", x, y, width, height)
	}
	if x != 120 || y != 80 || width != 440 || height != 620 {
		t.Fatalf("unexpected natural popup geometry: %d,%d %dx%d", x, y, width, height)
	}
}

func TestPausedInputKeepsPopupConfinement(t *testing.T) {
	// Multi-open environments own confinement now. "active" means environments
	// are running; pause must not release geometry ownership. Layout hold still
	// yields while tiles are moving.
	if !syncPopupConfinementEnabled(true, true, 0) {
		t.Fatal("pausing input must not disable popup containment")
	}
	if syncPopupConfinementEnabled(false, false, 0) {
		t.Fatal("no running environment must release popup containment")
	}
	if syncPopupConfinementEnabled(true, false, 1) {
		t.Fatal("popup containment must yield while an explicit layout is updating")
	}
}

func TestAnyPopupCanResolveOwnerThroughChromeProcessTree(t *testing.T) {
	owner := syncPopupOwnerWindow{hwnd: 10, pid: 100, rect: winRect{Left: 0, Top: 0, Right: 800, Bottom: 600}}
	search := &syncPopupBoundsSearch{
		owners:              []syncPopupOwnerWindow{owner},
		processOwners:       map[int]int{777: 100},
		processOwnersLoaded: true,
	}
	resolved, ok := search.findProcessTreeOwner(777)
	if !ok || resolved.hwnd != owner.hwnd {
		t.Fatalf("Chrome child-process popup did not resolve to its browser owner: %+v", resolved)
	}
}

func TestSyncPopupPlacementNeverPromotesDesktopTopmost(t *testing.T) {
	// Position-only move keeps NOSIZE.
	geometryOnly := syncPopupPlacementFlags(true, false, false)
	if geometryOnly&SWP_NOZORDER == 0 {
		t.Fatal("geometry-only update must preserve an already correct owner-relative Z-order")
	}
	if geometryOnly&SWP_NOACTIVATE == 0 {
		t.Fatal("popup placement must not steal focus")
	}
	if geometryOnly&SWP_NOSIZE == 0 {
		t.Fatal("position-only move must keep SWP_NOSIZE")
	}
	// Force-fit resize: sizeChanged clears NOSIZE.
	withSize := syncPopupPlacementFlags(true, false, true)
	if withSize&SWP_NOSIZE != 0 {
		t.Fatal("force-fit resize must allow size change (no SWP_NOSIZE)")
	}

	zOrderOnly := syncPopupPlacementFlags(false, true, false)
	if zOrderOnly&SWP_NOZORDER != 0 {
		t.Fatal("owner-relative placement must be allowed to repair Z-order")
	}
	if zOrderOnly&SWP_NOMOVE == 0 || zOrderOnly&SWP_NOSIZE == 0 {
		t.Fatal("Z-order-only repair must preserve the extension's current geometry")
	}
	if HWND_NOTOPMOST == HWND_TOP {
		t.Fatal("topmost demotion must not use the normal HWND_TOP insertion handle")
	}
}

func TestOtherExtensionPromptCandidateUsesOwnerBounds(t *testing.T) {
	owner := winRect{Left: 0, Top: 0, Right: 600, Bottom: 500}
	popup := winRect{Left: 520, Top: 50, Right: 920, Bottom: 450}
	if !isSyncPopupSurfaceCandidate("Permission request", popup, owner, false, true) {
		t.Fatal("non-wallet extension prompt must be contained after process-tree ownership is resolved")
	}
}

func TestProcessLinkedWalletNotificationIsContained(t *testing.T) {
	owner := winRect{Left: 100, Top: 100, Right: 900, Bottom: 700}
	// Empty-title Aura shell sized like a MetaMask notification host.
	popup := winRect{Left: 400, Top: 200, Right: 760, Bottom: 800}
	if !isSyncPopupSurfaceCandidate("", popup, owner, false, true) {
		t.Fatal("process-linked empty-title wallet shell must be constrained to its environment")
	}
	// Full-size frame with a browser title must never be treated as a popup.
	full := winRect{Left: 100, Top: 100, Right: 900, Bottom: 700}
	if isSyncPopupSurfaceCandidate("example.com - Google Chrome", full, owner, false, true) {
		t.Fatal("main browser frames must not be adopted as wallet popups")
	}
}

func TestOwnerLinkedSurfaceAlwaysContained(t *testing.T) {
	owner := winRect{Left: 0, Top: 0, Right: 800, Bottom: 600}
	popup := winRect{Left: 10, Top: 10, Right: 790, Bottom: 590}
	if !isSyncPopupSurfaceCandidate("", popup, owner, true, false) {
		t.Fatal("Win32-owned Chrome surfaces must stay confined to the owner cell")
	}
}

func TestConstrainSyncPopupKeepsZOrderAboveOwnerCell(t *testing.T) {
	// Geometry: notification origin spilled left of a tiled cell; taller than cell.
	x, y, w, h, changed := constrainSyncPopupRect(
		winRect{Left: -40, Top: 20, Right: 400, Bottom: 700}, // 440x680
		winRect{Left: 0, Top: 0, Right: 420, Bottom: 560},    // available 416x556
		2,
	)
	if !changed {
		t.Fatal("spilled wallet popup must be moved back into the environment cell")
	}
	// Force-fit: width may keep 416 if 440>416; height 556.
	if w != 416 || h != 556 {
		t.Fatalf("force-fit size: %dx%d", w, h)
	}
	if x != 2 || y != 2 {
		t.Fatalf("origin clamp after fit: %d,%d", x, y)
	}
}

func TestMainClientWindowTitleExcludesSyncAssistant(t *testing.T) {
	for _, title := range []string{"BrowserStudio", "BrowserStudio Manager"} {
		if !isMainClientWindowTitle(title) {
			t.Fatalf("manager title should be recognized: %q", title)
		}
	}
	for _, title := range []string{"BrowserStudio · 同步工具", "MetaMask - BrowserStudio", "BrowserStudio Window Sync"} {
		if isMainClientWindowTitle(title) {
			t.Fatalf("non-manager title must not be minimized: %q", title)
		}
	}
}

func TestWalletPopupTitleClassification(t *testing.T) {
	for _, title := range []string{"petra - prompt", "Rabby Wallet Notification", "metamask"} {
		if !looksLikeWalletExtensionPopup(title) {
			t.Fatalf("%q should be recognized as a wallet extension popup", title)
		}
	}
	if !isStrongExtensionPopupTitle("登录请求") {
		t.Fatal("login request should be recognized as a strong popup title")
	}
	if isStrongExtensionPopupTitle("The crypto wallet for DeFi | MetaMask") {
		t.Fatal("wallet website title must not be classified as a strong popup")
	}
	for _, title := range []string{
		"MetaMask Notification - BrowserStudio",
		"Rabby Wallet - Boost Browser",
		"Petra - BrowserStudio",
	} {
		if !isKnownWalletPopupProductTitle(title) {
			t.Fatalf("%q should be recognized as a wallet product popup", title)
		}
	}
	if isKnownWalletPopupProductTitle("Wallet news - BrowserStudio") {
		t.Fatal("unknown browser title must not be classified as a wallet product popup")
	}
}

func TestBrowserTopLevelClientSizeAllowsArrangedThinWindows(t *testing.T) {
	for _, size := range [][2]int{{1920, 36}, {480, 120}, {320, 24}, {80, 8}} {
		if !browserTopLevelClientSizeAllowed(size[0], size[1]) {
			t.Fatalf("arranged browser window %dx%d must remain discoverable", size[0], size[1])
		}
	}
	for _, size := range [][2]int{{0, 600}, {600, 0}, {79, 600}, {600, 7}, {10001, 600}, {600, 10001}} {
		if browserTopLevelClientSizeAllowed(size[0], size[1]) {
			t.Fatalf("invalid browser surface %dx%d was accepted", size[0], size[1])
		}
	}
}

func TestMainBrowserWindowTitleClassification(t *testing.T) {
	for _, title := range []string{"Moss - Boost Browser", "新标签页 - Chromium", "Phantom - Google Chrome"} {
		if !looksLikeMainBrowserWindowTitle(title) {
			t.Fatalf("%q should be recognized as a main browser title", title)
		}
	}
	if looksLikeMainBrowserWindowTitle("Rabby Wallet Notification") {
		t.Fatal("wallet notification must not be classified as a main browser window")
	}
}

func TestLooksLikeServiceWorkerDevToolsTitleIncludesGenericDevTools(t *testing.T) {
	for _, title := range []string{"devtools", "service worker", "developer tools", "开发者工具"} {
		if !looksLikeServiceWorkerDevToolsTitle(title) {
			t.Fatalf("%q should be recognized as service worker/devtools window", title)
		}
	}
}

func TestAuxiliaryIMEWindowTitleOrClassIsExcluded(t *testing.T) {
	cases := []struct {
		title string
		class string
	}{
		{title: "Default IME"},
		{class: "IME"},
		{title: "Microsoft IME", class: "Chrome_WidgetWin_0"},
	}
	for _, tc := range cases {
		if !isAuxiliaryIMEWindowTitleOrClass(tc.title, tc.class) {
			t.Fatalf("title=%q class=%q should be treated as auxiliary IME window", tc.title, tc.class)
		}
	}
	if isAuxiliaryIMEWindowTitleOrClass("Moss - Boost Browser", "Chrome_WidgetWin_1") {
		t.Fatal("normal browser window should not be treated as auxiliary IME window")
	}
}
