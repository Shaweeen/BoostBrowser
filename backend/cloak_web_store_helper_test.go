package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boost-browser/backend/internal/browser"
)

func TestWebStoreHelperUsesPerProfileDataPath(t *testing.T) {
	userDataA := filepath.Join(t.TempDir(), "profile-a")
	userDataB := filepath.Join(t.TempDir(), "profile-b")
	gotA, err := ensureEmbeddedWebStoreHelper(userDataA)
	if err != nil {
		t.Fatalf("materialize helper A: %v", err)
	}
	gotB, err := ensureEmbeddedWebStoreHelper(userDataB)
	if err != nil {
		t.Fatalf("materialize helper B: %v", err)
	}
	wantA := filepath.Join(userDataA, ".browserstudio", "extensions", cloakWebStoreHelperDirName)
	if normalizeExtensionPath(gotA) != normalizeExtensionPath(wantA) {
		t.Fatalf("helper path=%q want=%q", gotA, wantA)
	}
	if normalizeExtensionPath(gotA) == normalizeExtensionPath(gotB) {
		t.Fatalf("parallel profiles must not share mutable helper endpoint: A=%q B=%q", gotA, gotB)
	}
	if err := validateUnpackedExtensionManifest(gotA); err != nil {
		t.Fatalf("embedded helper was not materialized: %v", err)
	}
	if again, err := ensureEmbeddedWebStoreHelper(userDataA); err != nil || normalizeExtensionPath(again) != normalizeExtensionPath(gotA) {
		t.Fatalf("helper must be reused for the same profile: first=%q second=%q err=%v", gotA, again, err)
	}
}

func TestWebStoreHelperEndpointPinsExactProfile(t *testing.T) {
	helperDir, err := ensureEmbeddedWebStoreHelper(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHelperBoostEndpoint(helperDir, 19876, "X-Test-Key", "secret", "profile-a"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(helperDir, "boost_endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var endpoint struct {
		Port      int    `json:"port"`
		APIHeader string `json:"apiHeader"`
		APIKey    string `json:"apiKey"`
		ProfileID string `json:"profileId"`
	}
	if err := json.Unmarshal(data, &endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.Port != 19876 || endpoint.APIHeader != "X-Test-Key" || endpoint.APIKey != "secret" || endpoint.ProfileID != "profile-a" {
		t.Fatalf("endpoint mismatch: %+v", endpoint)
	}
}

func TestBrowserCoreNeedsWebStoreHelperOnlyForCompatibleChromium(t *testing.T) {
	chromeTestingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(chromeTestingDir, "chrome-for-testing.marker"), []byte("148"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !browserCoreNeedsWebStoreHelper(browser.Core{}, filepath.Join(chromeTestingDir, "chrome.exe")) {
		t.Fatal("verified Chrome for Testing must receive Web Store compatibility")
	}
	if !browserCoreNeedsWebStoreHelper(browser.Core{CoreName: "CloakBrowser"}, filepath.Join(t.TempDir(), "chrome.exe")) {
		t.Fatal("Cloak Chromium must receive Web Store compatibility")
	}
	if browserCoreNeedsWebStoreHelper(browser.Core{CoreName: "Google Chrome"}, filepath.Join(t.TempDir(), "chrome.exe")) {
		t.Fatal("official branded Chrome must keep its native Web Store installer")
	}
}

func TestAppendWebStoreHelperLaunchArgsKeepsUserExtensionsAndEnablesCLI(t *testing.T) {
	userExtension := filepath.Join(t.TempDir(), "metamask")
	helperExtension := filepath.Join(t.TempDir(), "web-store-helper")
	got := appendWebStoreHelperLaunchArgs([]string{
		"--disable-extensions",
		"--load-extension=" + userExtension,
	}, helperExtension)
	if !hasExtensionDirInLaunchArgs(got, userExtension) || !hasExtensionDirInLaunchArgs(got, helperExtension) {
		t.Fatalf("helper injection must preserve the user's extension: %#v", got)
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "--disable-extensions ") || strings.HasSuffix(joined, "--disable-extensions") {
		t.Fatalf("extension blocker remained: %#v", got)
	}
	if !strings.Contains(joined, "DisableLoadExtensionCommandLineSwitch") {
		t.Fatalf("Chrome 137+ compatibility feature missing: %#v", got)
	}
}
