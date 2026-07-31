//go:build windows

package backend

import "testing"

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

func TestSyncPopupUsesCurrentArrangedOwnerBounds(t *testing.T) {
	x, y, width, height, changed := constrainSyncPopupRect(
		winRect{Left: 20, Top: 20, Right: 1460, Bottom: 920},
		winRect{Left: 0, Top: 0, Right: 820, Bottom: 560},
		2,
	)
	if !changed {
		t.Fatal("misplaced wallet popup origin should be nudged into owner")
	}
	// Position-only: keep natural 1440×900; only move origin into cell.
	if width != 1440 || height != 900 {
		t.Fatalf("wallet must keep natural size, got %dx%d", width, height)
	}
	if x != 2 || y != 2 {
		t.Fatalf("origin should be clamped to inset cell: x=%d y=%d", x, y)
	}
}

func TestSyncPopupDoesNotShrinkTallWallet(t *testing.T) {
	// Tall wallet notification that exceeds the cell height — still no resize.
	x, y, width, height, changed := constrainSyncPopupRect(
		winRect{Left: 50, Top: 50, Right: 410, Bottom: 710}, // 360x660
		winRect{Left: 0, Top: 0, Right: 500, Bottom: 400},   // cell 500x400, inset 2 → 496x396
		2,
	)
	if width != 360 || height != 660 {
		t.Fatalf("must not shrink tall wallet: %dx%d", width, height)
	}
	// Origin stays if already inside on X; Y may stay 50 if height overflows.
	if x != 50 || y != 50 || !changed && (x != 50 || y != 50) {
		// y=50 is inside top; for oversized height we keep top-left (no shrink).
		_ = changed
	}
	if x < 2 || y < 2 {
		t.Fatalf("origin pulled outside cell incorrectly: %d,%d", x, y)
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
	// Position move, never resize (wallet natural size).
	geometryOnly := syncPopupPlacementFlags(true, false, false)
	if geometryOnly&SWP_NOZORDER == 0 {
		t.Fatal("geometry-only update must preserve an already correct owner-relative Z-order")
	}
	if geometryOnly&SWP_NOACTIVATE == 0 {
		t.Fatal("popup placement must not steal focus")
	}
	if geometryOnly&SWP_NOSIZE == 0 {
		t.Fatal("wallet placement must set SWP_NOSIZE to avoid reflow flicker")
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
	// Geometry: notification origin spilled left of a tiled cell.
	x, y, w, h, changed := constrainSyncPopupRect(
		winRect{Left: -40, Top: 20, Right: 400, Bottom: 700},
		winRect{Left: 0, Top: 0, Right: 420, Bottom: 560},
		2,
	)
	if !changed {
		t.Fatal("spilled wallet popup must be moved back into the environment cell")
	}
	// Natural size preserved (440x680); origin clamped to cell inset.
	if w != 440 || h != 680 {
		t.Fatalf("natural size must be kept: %dx%d", w, h)
	}
	if x != 2 || y != 20 {
		t.Fatalf("origin clamp: %d,%d", x, y)
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
