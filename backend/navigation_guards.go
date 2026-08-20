package backend

import (
	"net/url"
	"strings"
)

// navigation_guards.go is the single owner for "may this page be CDP-touched
// or URL-mirrored". Web Store compat inject, URL sync, and input replay all
// consult these predicates so a later change cannot reintroduce blanket attach
// or one-time OAuth mirroring.

func parseNavigationHostPath(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", ""
	}
	host = strings.ToLower(parsed.Hostname())
	path = strings.ToLower(parsed.EscapedPath())
	if path == "" {
		path = "/"
	}
	return host, path
}

func isChromeWebStoreURL(raw string) bool {
	host, path := parseNavigationHostPath(raw)
	if host == "" {
		return false
	}
	switch host {
	case "chromewebstore.google.com":
		return true
	case "chrome.google.com":
		return strings.HasPrefix(path, "/webstore")
	case "microsoftedge.microsoft.com":
		return strings.HasPrefix(path, "/addons")
	case "addons.opera.com":
		return true
	default:
		return false
	}
}

func shouldInjectWebStoreCompat(raw string) bool {
	return isChromeWebStoreURL(raw)
}

func isInternalBrowserURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	if u == "" || u == "about:blank" || strings.HasPrefix(u, "about:blank") {
		return true
	}
	return strings.HasPrefix(u, "chrome://") ||
		strings.HasPrefix(u, "devtools://") ||
		strings.HasPrefix(u, "edge://")
}

// isAuthSensitiveURL is true only for one-time OAuth / SSO / consent surfaces.
// Password-login pages (X /i/flow/login, Google /signin, GitHub /login) stay
// ordinary so batch account login can still sync. Mirroring or CDP-attaching
// a consent URL consumes the authorization code/state on every follower and
// is a classic automation signal (X then shows "You weren't able to give access").
func isAuthSensitiveURL(raw string) bool {
	if isChromeWebStoreURL(raw) {
		return false
	}
	host, path := parseNavigationHostPath(raw)
	if host == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(raw))

	authPathTokens := []string{
		"/oauth2/authorize",
		"/oauth/authorize",
		"/oauth/authenticate",
		"/oauth2/auth",
		"/oauth2/v2/auth",
		"/dialog/oauth",
		"/connect/authorize",
		"/as/authorization.oauth2",
		"/oidc/authorize",
		"/application/o/authorize",
		"/signin/oauth",
		"/o/oauth2",
		"/i/oauth2/authorize",
		"/i/oauth/authorize",
		"/auth/authorize",
		"/oauth20_authorize.srf",
	}
	for _, token := range authPathTokens {
		if strings.Contains(path, token) {
			return true
		}
	}

	switch {
	case host == "x.com" || host == "twitter.com" || host == "api.twitter.com" ||
		strings.HasSuffix(host, ".x.com") || strings.HasSuffix(host, ".twitter.com"):
		if strings.Contains(path, "/i/oauth") || strings.Contains(path, "/oauth/") {
			return true
		}
	case host == "discord.com" || host == "discordapp.com" || strings.HasSuffix(host, ".discord.com"):
		if strings.Contains(path, "/oauth2/") {
			return true
		}
	case host == "accounts.google.com":
		if strings.Contains(path, "oauth") || strings.Contains(path, "/o/oauth2") {
			return true
		}
	case host == "login.microsoftonline.com" || host == "login.live.com" || host == "login.windows.net":
		return true
	case host == "github.com" || host == "api.github.com":
		if strings.Contains(path, "/login/oauth") || strings.Contains(path, "/login/device") {
			return true
		}
	case host == "appleid.apple.com":
		if strings.Contains(path, "/auth/") {
			return true
		}
	case strings.Contains(host, "facebook.com") || strings.Contains(host, "fb.com"):
		if strings.Contains(path, "/dialog/oauth") || strings.Contains(path, "/login/device-based") {
			return true
		}
	}

	if strings.Contains(path, "/authorize") || strings.HasSuffix(path, "/auth") {
		if strings.Contains(lower, "client_id=") &&
			(strings.Contains(lower, "response_type=") || strings.Contains(lower, "redirect_uri=")) {
			return true
		}
	}
	return false
}

// shouldAttachPageCDP is the focus-probe owner: Runtime.evaluate / page
// WebSockets must not open on one-time OAuth documents, chrome internals,
// or extension/wallet surfaces. After an extension is installed in the
// environment, connect/sign stays Chrome ↔ extension. URL-only decisions
// from /json are still allowed so Authorize can be gated without attaching.
func shouldAttachPageCDP(raw string) bool {
	if isInternalBrowserURL(raw) {
		return false
	}
	return !isAuthSensitiveURL(raw)
}

// shouldMirrorSyncNavigation is the URL-sync owner: followers may only be
// Page.navigate'd to ordinary browsing URLs. Blank, chrome internals, extension
// documents and OAuth/consent surfaces stay local to the environment that
// opened them. Password-login pages remain mirrored.
func shouldMirrorSyncNavigation(raw string) bool {
	if isInternalBrowserURL(raw) {
		return false
	}
	return true
}

// shouldReplaySyncInput is the input-replay owner for auth and extension
// surfaces. Clicking Authorize, or clicking inside Rabby/MetaMask, must not
// be mirrored or CDP-injected. Dapp pages stay replayable.
func shouldReplaySyncInput(raw string) bool {
	if isInternalBrowserURL(raw) {
		return false
	}
	return true
}
