package backend

import "testing"

func TestIsChromeWebStoreURL(t *testing.T) {
	store := []string{
		"https://chromewebstore.google.com/",
		"https://chromewebstore.google.com/detail/metamask/nkbihfbeogaeaoehlefnkodbefgpgknn",
		"https://chrome.google.com/webstore/detail/foo",
		"https://microsoftedge.microsoft.com/addons/detail/example",
		"https://addons.opera.com/extensions/details/example/",
	}
	for _, raw := range store {
		if !isChromeWebStoreURL(raw) || !shouldInjectWebStoreCompat(raw) {
			t.Fatalf("store url must inject compat: %s", raw)
		}
		if isAuthSensitiveURL(raw) {
			t.Fatalf("store url is not an auth surface: %s", raw)
		}
	}
	notStore := []string{
		"about:blank",
		"chrome://newtab",
		"https://portal.genlayer.foundation/community/journey",
		"https://x.com/home",
		"https://x.com/i/oauth2/authorize?state=abc",
		"https://google.com/search?q=webstore",
	}
	for _, raw := range notStore {
		if isChromeWebStoreURL(raw) || shouldInjectWebStoreCompat(raw) {
			t.Fatalf("must not inject Web Store compat: %s", raw)
		}
	}
}

func TestIsAuthSensitiveURLCoversOAuthAndLeavesLoginAndNormalPagesAlone(t *testing.T) {
	auth := []string{
		"https://x.com/i/oauth2/authorize?client_id=1&state=once&response_type=code",
		"https://twitter.com/i/oauth2/authorize?state=once",
		"https://twitter.com/oauth/authorize?oauth_token=abc",
		"https://api.twitter.com/oauth/authenticate",
		"https://discord.com/oauth2/authorize?client_id=1&response_type=code",
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=1",
		"https://accounts.google.com/signin/oauth",
		"https://github.com/login/oauth/authorize?client_id=1",
		"https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		"https://login.live.com/oauth20_authorize.srf",
		"https://appleid.apple.com/auth/authorize?client_id=1",
		"https://www.facebook.com/v18.0/dialog/oauth?client_id=1",
		"https://auth.example.com/authorize?client_id=abc&response_type=code&redirect_uri=https://app.example/cb",
	}
	for _, raw := range auth {
		if !isAuthSensitiveURL(raw) {
			t.Fatalf("expected auth-sensitive: %s", raw)
		}
		if shouldAttachPageCDP(raw) {
			t.Fatalf("must not CDP-attach auth surface: %s", raw)
		}
		if !shouldMirrorSyncNavigation(raw) {
			t.Fatalf("OAuth surface must URL-sync from the master: %s", raw)
		}
		if !shouldReplaySyncInput(raw) {
			t.Fatalf("OAuth surface must replay master input: %s", raw)
		}
	}

	// Password / account login must still sync so a farm can log into X/Google.
	login := []string{
		"https://x.com/i/flow/login",
		"https://twitter.com/i/flow/login",
		"https://accounts.google.com/signin/v2/identifier",
		"https://accounts.google.com/ServiceLogin",
		"https://accounts.google.com/accountchooser",
		"https://github.com/login",
		"https://discord.com/login",
	}
	for _, raw := range login {
		if isAuthSensitiveURL(raw) {
			t.Fatalf("password login must not be treated as one-time OAuth: %s", raw)
		}
		if !shouldAttachPageCDP(raw) {
			t.Fatalf("password login may still be probed: %s", raw)
		}
		if !shouldMirrorSyncNavigation(raw) {
			t.Fatalf("password login must still URL-sync: %s", raw)
		}
		if !shouldReplaySyncInput(raw) {
			t.Fatalf("password login must still replay input: %s", raw)
		}
	}

	normal := []string{
		"https://portal.genlayer.foundation/community/journey",
		"https://x.com/home",
		"https://x.com/i/web",
		"https://twitter.com/compose/post",
		"https://discord.com/channels/123/456",
		"https://github.com/Shaweeen/BoostBrowser",
		"https://www.facebook.com/",
		"https://google.com/search?q=oauth",
	}
	for _, raw := range normal {
		if isAuthSensitiveURL(raw) {
			t.Fatalf("ordinary page must not be treated as auth: %s", raw)
		}
		if !shouldMirrorSyncNavigation(raw) {
			t.Fatalf("ordinary page must still URL-sync: %s", raw)
		}
		if !shouldReplaySyncInput(raw) {
			t.Fatalf("ordinary page must still replay input: %s", raw)
		}
	}
}

func TestShouldMirrorSyncNavigationSkipsOnlyInternal(t *testing.T) {
	skip := []string{
		"",
		"about:blank",
		"about:blank#blocked",
		"chrome://newtab/",
		"devtools://devtools/bundled/inspector.html",
	}
	for _, raw := range skip {
		if shouldMirrorSyncNavigation(raw) {
			t.Fatalf("must not mirror: %s", raw)
		}
	}
	if !shouldMirrorSyncNavigation("https://portal.genlayer.foundation/community/journey") {
		t.Fatal("dapp journey page must remain URL-synced")
	}
	if !shouldMirrorSyncNavigation("https://x.com/i/flow/login") {
		t.Fatal("X password login must remain URL-synced")
	}
	if !shouldMirrorSyncNavigation("chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/popup.html") {
		t.Fatal("wallet extension popup must remain URL-synced")
	}
}
