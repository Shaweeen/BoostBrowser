package backend

import (
	"boost-browser/backend/internal/config"
	"testing"
)

func TestBrowserStartTimingSettingsUsesDefaultsWhenUnset(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Browser.StartReadyTimeoutMs = 0
	cfg.Browser.StartStableWindowMs = -1

	readyMs := browserStartReadyTimeoutMillis(cfg)
	stableMs := browserStartStableWindowMillis(cfg)

	if readyMs != 3000 {
		t.Fatalf("expected default ready timeout 3000ms, got %d", readyMs)
	}
	if stableMs != 450 {
		t.Fatalf("expected default stable window 450ms, got %d", stableMs)
	}
}

func TestBrowserStartTimingSettingsMigratesLegacyStableWindow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Browser.StartStableWindowMs = 1200
	if stableMs := browserStartStableWindowMillis(cfg); stableMs != 450 {
		t.Fatalf("expected legacy stable window to migrate to 450ms, got %d", stableMs)
	}
}

func TestSaveBrowserSettingsPreservesExistingStartTimingWhenOmitted(t *testing.T) {
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()
	app.config.Browser.StartReadyTimeoutMs = 15000
	app.config.Browser.StartStableWindowMs = 2400

	if err := app.SaveBrowserSettings(BrowserSettings{
		UserDataRoot:           app.config.Browser.UserDataRoot,
		DefaultFingerprintArgs: append([]string{}, app.config.Browser.DefaultFingerprintArgs...),
		DefaultLaunchArgs:      append([]string{}, app.config.Browser.DefaultLaunchArgs...),
		DefaultProxy:           app.config.Browser.DefaultProxy,
	}); err != nil {
		t.Fatalf("SaveBrowserSettings returned error: %v", err)
	}

	if app.config.Browser.StartReadyTimeoutMs != 15000 {
		t.Fatalf("expected ready timeout to be preserved, got %d", app.config.Browser.StartReadyTimeoutMs)
	}
	if app.config.Browser.StartStableWindowMs != 2400 {
		t.Fatalf("expected stable window to be preserved, got %d", app.config.Browser.StartStableWindowMs)
	}
}

func TestSaveBrowserSettingsAppliesExplicitStartTiming(t *testing.T) {
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()

	if err := app.SaveBrowserSettings(BrowserSettings{
		UserDataRoot:           app.config.Browser.UserDataRoot,
		DefaultFingerprintArgs: append([]string{}, app.config.Browser.DefaultFingerprintArgs...),
		DefaultLaunchArgs:      append([]string{}, app.config.Browser.DefaultLaunchArgs...),
		DefaultProxy:           app.config.Browser.DefaultProxy,
		StartReadyTimeoutMs:    18000,
		StartStableWindowMs:    3000,
	}); err != nil {
		t.Fatalf("SaveBrowserSettings returned error: %v", err)
	}

	if app.config.Browser.StartReadyTimeoutMs != 18000 {
		t.Fatalf("expected ready timeout 18000ms, got %d", app.config.Browser.StartReadyTimeoutMs)
	}
	if app.config.Browser.StartStableWindowMs != 3000 {
		t.Fatalf("expected stable window 3000ms, got %d", app.config.Browser.StartStableWindowMs)
	}
}

func TestSaveBrowserSettingsNormalizesLocalVPNGateway(t *testing.T) {
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()

	if err := app.SaveBrowserSettings(BrowserSettings{
		UserDataRoot:     app.config.Browser.UserDataRoot,
		ProxyNetworkMode: "LOCAL_GATEWAY",
		LocalVPNProxy:    "127.0.0.1:7897",
	}); err != nil {
		t.Fatalf("SaveBrowserSettings returned error: %v", err)
	}
	if app.config.Browser.ProxyNetworkMode != "local_gateway" {
		t.Fatalf("unexpected proxy network mode: %q", app.config.Browser.ProxyNetworkMode)
	}
	if app.config.Browser.LocalVPNProxy != "http://127.0.0.1:7897" {
		t.Fatalf("unexpected local VPN gateway: %q", app.config.Browser.LocalVPNProxy)
	}
}

func TestSaveBrowserSettingsRejectsRemoteVPNGateway(t *testing.T) {
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()

	err := app.SaveBrowserSettings(BrowserSettings{
		UserDataRoot:  app.config.Browser.UserDataRoot,
		LocalVPNProxy: "http://203.0.113.10:7897",
	})
	if err == nil {
		t.Fatal("remote endpoint must not be accepted as a local VPN gateway")
	}
}
