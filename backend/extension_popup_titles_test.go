package backend

import "testing"

func TestWalletPopupTitleClassificationIsCaseInsensitive(t *testing.T) {
	for _, title := range []string{
		"Rabby Wallet Notification",
		"MetaMask Notification",
		"PETRA - PROMPT",
		"  Phantom Wallet  ",
	} {
		if !looksLikeWalletExtensionPopup(title) {
			t.Fatalf("%q should be recognized as a wallet extension popup", title)
		}
	}
	if looksLikeWalletExtensionPopup("BrowserStudio Manager") {
		t.Fatal("manager title must not be classified as a wallet extension popup")
	}
}
