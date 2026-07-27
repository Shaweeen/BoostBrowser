package backend

import "strings"

// looksLikeWalletExtensionPopup accepts the original Win32 window title.
// Normalize inside the classifier so callers and tests do not have to rely on
// an undocumented lower-case precondition.
func looksLikeWalletExtensionPopup(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return false
	}
	keywords := []string{
		"okx wallet",
		"metamask",
		"rabby",
		"phantom",
		"bitget wallet",
		"keplr",
		"petra",
		"wallet notification",
		"wallet - prompt",
		" - prompt",
		"钱包",
	}
	for _, keyword := range keywords {
		if strings.Contains(title, keyword) {
			return true
		}
	}
	// Keep generic "wallet" as a weak match; it is only used with the original
	// browser PID guard, so normal web pages in other browsers are not affected.
	return strings.Contains(title, "wallet")
}
