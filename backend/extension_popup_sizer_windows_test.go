//go:build windows

package backend

import "testing"

func TestExplicitLayoutCancelsStartupWindowTemplate(t *testing.T) {
	const pid = 987654
	session := &startupWindowBoundsSession{}
	startupWindowBoundsSessions.Store(pid, session)
	t.Cleanup(func() { startupWindowBoundsSessions.Delete(pid) })

	cancelBrowserWindowBoundsEnforcement(pid)
	if !session.cancelled.Load() {
		t.Fatal("explicit layout must cancel the startup-only window template")
	}
	if _, exists := startupWindowBoundsSessions.Load(pid); exists {
		t.Fatal("cancelled startup bounds session must be released")
	}
}

func TestSyncPopupUsesCurrentArrangedOwnerBounds(t *testing.T) {
	x, y, width, height, changed := constrainSyncPopupRect(
		winRect{Left: 20, Top: 20, Right: 1460, Bottom: 920},
		winRect{Left: 0, Top: 0, Right: 820, Bottom: 560},
		2,
	)
	if !changed {
		t.Fatal("oversized wallet popup should be constrained")
	}
	if width != 816 || height != 556 {
		t.Fatalf("popup must adapt to the current arranged owner: %dx%d", width, height)
	}
	if x < 2 || y < 2 || x+width > 818 || y+height > 558 {
		t.Fatalf("popup escaped owner bounds: x=%d y=%d width=%d height=%d", x, y, width, height)
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
	if !syncPopupConfinementEnabled(true, true, 0) {
		t.Fatal("pausing input must not disable popup containment")
	}
	if syncPopupConfinementEnabled(false, false, 0) {
		t.Fatal("a stopped sync session must release popup containment")
	}
	if syncPopupConfinementEnabled(true, false, 1) {
		t.Fatal("popup containment must yield while an explicit layout is updating")
	}
}

func TestWalletPopupCanResolveOwnerThroughChromeProcessTree(t *testing.T) {
	owner := syncPopupOwnerWindow{hwnd: 10, pid: 100, rect: winRect{Left: 0, Top: 0, Right: 800, Bottom: 600}}
	search := &syncPopupBoundsSearch{
		owners:              []syncPopupOwnerWindow{owner},
		processOwners:       map[int]int{777: 100},
		processOwnersLoaded: true,
	}
	resolved, ok := search.findProcessTreeOwner(777)
	if !ok || resolved.hwnd != owner.hwnd {
		t.Fatalf("wallet notification child process did not resolve to its browser owner: %+v", resolved)
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
		t.Fatal("normal browser window should not be treated as an auxiliary IME window")
	}
}
