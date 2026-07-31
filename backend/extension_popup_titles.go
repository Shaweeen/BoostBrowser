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
	// Keywords aligned with galaylm/Chrome-Manager-Clean is_likely_wallet_popup
	// plus common multi-env wallets used with BrowserStudio.
	keywords := []string{
		"okx wallet",
		"okx",
		"metamask",
		"rabby",
		"phantom",
		"bitget wallet",
		"keplr",
		"petra",
		"unisat",
		"wallet notification",
		"wallet - prompt",
		" - prompt",
		"钱包",
		"token",
		"connect",
		"signature",
		"transaction",
		"sign",
		"confirm",
		"web3",
		"permission",
		"notification",
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
