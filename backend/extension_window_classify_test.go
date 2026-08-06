package backend

import "testing"

// 平铺/同步不得把二级窗口或钱包/OAuth 弹窗当作环境主窗口。

func TestEnvironmentFrameLooksLikeMainRejectsCompactPopupCuedWindow(t *testing.T) {
	cases := []struct {
		name  string
		w, h  int
		title string
	}{
		{"MetaMask confirm 320x260", 320, 260, "MetaMask"},
		{"Rabby confirm 340x260", 340, 260, "Rabby"},
		{"X OAuth authorization 400x300", 400, 300, "登录授权 - X"},
		{"Wallet signature request 420x300", 420, 300, "Signature Request"},
		{"OKX connect prompt 380x280", 380, 280, "OKX Wallet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if environmentFrameLooksLikeMain(tc.w, tc.h, tc.title) {
				t.Fatalf("compact popup-cued window must NOT be treated as main frame: %dx%d %q", tc.w, tc.h, tc.title)
			}
		})
	}
}

func TestEnvironmentFrameLooksLikeMainAcceptsRealMainFrames(t *testing.T) {
	cases := []struct {
		name  string
		w, h  int
		title string
	}{
		{"full frame", 1280, 800, "Twitter - Google Chrome"},
		{"dense tile cell with browser suffix", 340, 280, "MetaMask - Google Chrome"},
		{"dense tile cell with plain tab title", 340, 280, "portfolio"},
		{"browser-suffixed frame with popup cue title", 480, 320, "Rabby - Google Chrome"},
		{"wide dense tile cell with wallet web app", 560, 360, "MetaMask"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !environmentFrameLooksLikeMain(tc.w, tc.h, tc.title) {
				t.Fatalf("real main frame must be recognized: %dx%d %q", tc.w, tc.h, tc.title)
			}
		})
	}
}

func TestEnvironmentFrameLooksLikeMainRejectsTallNotification(t *testing.T) {
	if environmentFrameLooksLikeMain(400, 600, "MetaMask notification") {
		t.Fatal("tall wallet notification must not be main frame")
	}
}

func TestEnvironmentFrameLooksLikeMainRejectsTinyMenus(t *testing.T) {
	if environmentFrameLooksLikeMain(200, 150, "approve") {
		t.Fatal("tiny extension menu must not be main frame")
	}
}
