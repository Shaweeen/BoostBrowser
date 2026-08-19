# One-shot Windows helper: finish 1.7.132 app_instance.go after part1a/1b.
# Does not touch Cookies, Preferences, LES, wallets, or --load-extension paths.
from pathlib import Path

p = Path("backend/app_instance.go")
text = p.read_text(encoding="utf-8")
start = text.find("func navigateToTargetURLs(")
if start < 0:
    raise SystemExit("navigateToTargetURLs missing")
comment = text.rfind("// navigateToTargetURLs", 0, start)
if comment != -1:
    start = comment
end = text.find("func dropStoreInstalledLoadExtensionArgs", start)
if end < 0:
    raise SystemExit("dropStoreInstalledLoadExtensionArgs missing")

new = """// navigateToTargetURLs opens user-configured start URLs with Target.createTarget.
// Every kernel uses the same owner: no about:blank, no UA override, no stealth
// script. Store compatibility (if needed later) lives in webstore_compat_watch.go.
func navigateToTargetURLs(debugPort int, urls []string, profileId string) {
	log := logger.New("Browser")
	if len(urls) == 0 {
		return
	}

	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		log.Warn("CDP \u5bfc\u822a\uff1a\u83b7\u53d6\u6d4f\u89c8\u5668 WebSocket \u5931\u8d25",
			logger.F("profile_id", profileId),
			logger.F("error", err.Error()),
		)
		return
	}

	browserConn, _, err := websocket.DefaultDialer.Dial(browserWsURL, nil)
	if err != nil {
		log.Warn("CDP \u5bfc\u822a\uff1a\u6d4f\u89c8\u5668 WebSocket \u8fde\u63a5\u5931\u8d25",
			logger.F("profile_id", profileId),
			logger.F("error", err.Error()),
		)
		return
	}
	defer browserConn.Close()
	browserConn.SetReadDeadline(time.Now().Add(15 * time.Second))

	for i, rawURL := range urls {
		createMsg := cdpMessage{
			Id:     i + 200,
			Method: "Target.createTarget",
			Params: map[string]any{"url": rawURL},
		}
		if err := browserConn.WriteJSON(createMsg); err != nil {
			log.Warn("CDP \u5bfc\u822a\uff1aTarget.createTarget \u5199\u5165\u5931\u8d25",
				logger.F("profile_id", profileId),
				logger.F("url", rawURL),
				logger.F("error", err.Error()),
			)
			continue
		}
		browserConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var resp cdpResponse
		_ = browserConn.ReadJSON(&resp)
		log.Info("CDP \u5bfc\u822a\uff1a\u76ee\u6807\u9875\u9762\u5df2\u6253\u5f00\uff08\u65e0 stealth/UA \u6ce8\u5165\uff09",
			logger.F("profile_id", profileId),
			logger.F("url", rawURL),
		)
	}
}

"""
text = text[:start] + new + text[end:]
for marker in ("// webStoreCompatScript", "const webStoreCompatScript"):
    idx = text.find(marker)
    if idx != -1:
        text = text[:idx].rstrip() + "\n"
        break

checks = {
    "func createBlankTab(": False,
    "applyWebStoreCompatibilityToInitialTab": False,
    "cloakOnly bool": False,
    "webStoreHelperForProfileLaunch": False,
    "dropStoreInstalledLoadExtensionArgs": True,
    "func navigateToTargetURLs(debugPort int, urls []string, profileId string)": True,
}
for needle, want in checks.items():
    found = needle in text
    if found != want:
        raise SystemExit("check failed: %s found=%s want=%s" % (needle, found, want))

p.write_text(text, encoding="utf-8")
print("instance splice OK")
