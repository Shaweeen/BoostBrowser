//go:build windows

package backend

import "testing"

func TestConstrainSyncWalletPopupUsesCompactResponsiveBounds(t *testing.T) {
	x, y, width, height, changed := constrainSyncPopupRectForTitle(
		"Rabby Wallet Notification",
		winRect{Left: 20, Top: 20, Right: 1460, Bottom: 920},
		winRect{Left: 0, Top: 0, Right: 820, Bottom: 560},
		2,
	)
	if !changed {
		t.Fatal("oversized wallet popup should be constrained")
	}
	if width != extensionPopupTargetWidth || height != 556 {
		t.Fatalf("unexpected responsive popup size: %dx%d", width, height)
	}
	if x < 2 || y < 2 || x+width > 818 || y+height > 558 {
		t.Fatalf("popup escaped owner bounds: x=%d y=%d width=%d height=%d", x, y, width, height)
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
