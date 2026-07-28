package backend

import (
	"archive/zip"
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func extensionZipForTest(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallUnpackedExtensionKeepsOldCopyWhenManifestInvalid(t *testing.T) {
	root := t.TempDir()
	extDir := filepath.Join(root, "extensions", "imported", "example")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "marker.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := installUnpackedExtension(root, "example", extensionZipForTest(t, `{"name":"broken"}`))
	if err == nil {
		t.Fatal("invalid manifest should be rejected")
	}
	data, readErr := os.ReadFile(filepath.Join(extDir, "marker.txt"))
	if readErr != nil || string(data) != "old" {
		t.Fatalf("old extension should be preserved: data=%q err=%v", data, readErr)
	}
}

func TestInstallUnpackedExtensionUpdateKeepsProgramRollbackAndReportsVersions(t *testing.T) {
	root := t.TempDir()
	extDir := filepath.Join(root, "extensions", "imported", "example")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"Wallet","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	installed, previous, current, err := installUnpackedExtension(root, "example", extensionZipForTest(t, `{"name":"Wallet","version":"2.0","manifest_version":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if installed != extDir || previous != "1.0" || current != "2.0" {
		t.Fatalf("unexpected update result: dir=%s previous=%s current=%s", installed, previous, current)
	}
	if got := readManifestVersionFromDir(extDir + ".previous"); got != "1.0" {
		t.Fatalf("previous extension package was not retained: %q", got)
	}
}

func TestRemoveExtensionDirFromLaunchArgs(t *testing.T) {
	target := `Z:\\Boost Browser\\extensions\\imported\\mcohilncbfahbmgdjkbpemcciiolgcge`
	other := `Z:\\Boost Browser\\extensions\\imported\\nkbihfbeogaeaoehlefnkodbefgpgknn`
	args := []string{
		"--disable-sync",
		"--load-extension=" + target + "," + other,
	}
	next, changed := removeExtensionDirFromLaunchArgs(args, target)
	if !changed {
		t.Fatalf("expected target extension to be removed")
	}
	want := []string{
		"--disable-sync",
		"--load-extension=" + other,
	}
	if !reflect.DeepEqual(next, want) {
		t.Fatalf("unexpected args after removal\nwant: %#v\n got: %#v", want, next)
	}
}

func TestGlobalExtensionRegistryUpsertUsesExtensionIDAsIdentity(t *testing.T) {
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	entries := []globalExtensionRegistryEntry{{
		DownloadAddress: "https://old.example/" + extID,
		ExtensionID:     extID,
	}}
	got := upsertGlobalExtensionRegistryEntry(entries, globalExtensionRegistryEntry{
		DownloadAddress: "https://chromewebstore.google.com/detail/metamask/" + extID,
		ExtensionID:     extID,
	})
	if len(got) != 1 {
		t.Fatalf("expected one global policy, got %d", len(got))
	}
	if got[0].DownloadAddress != "https://chromewebstore.google.com/detail/metamask/"+extID {
		t.Fatalf("global policy address was not updated: %#v", got[0])
	}
}

func TestResolveExtensionDownloadURLUsesBundledChromeVersion(t *testing.T) {
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	got := resolveExtensionDownloadURL(extID, extID)
	if !strings.Contains(got, "prodversion="+managedExtensionChromeVersion) {
		t.Fatalf("extension download URL does not match bundled Chrome %s: %s", managedExtensionChromeVersion, got)
	}
}

func TestAppendGlobalExtensionArgsRunsOnlyForNewProfileCreation(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	extDir := app.globalExtensionDir(extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.saveGlobalExtensionRegistry(globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{{
		DownloadAddress: extID,
		ExtensionID:     extID,
	}}}); err != nil {
		t.Fatal(err)
	}

	got := app.appendGlobalExtensionArgsForNewProfile([]string{"--disable-extensions", "--no-first-run"})
	if !hasExtensionDirInLaunchArgs(got, extDir) {
		t.Fatalf("global extension was not injected into fresh launch args: %#v", got)
	}
	got = app.appendGlobalExtensionArgsForNewProfile(got)
	active := activeLoadExtensionDirs(got)
	if len(active) != 1 {
		t.Fatalf("global extension should be de-duplicated, got %#v", got)
	}
	if reflect.DeepEqual(got, []string{"--disable-extensions", "--no-first-run"}) {
		t.Fatalf("extension blocker should be removed during profile creation: %#v", got)
	}
}

func TestAddExtensionDirRemovesAllExtensionBlockingArgs(t *testing.T) {
	extDir := filepath.Join(t.TempDir(), "extensions", "wallet")
	got := addExtensionDirToLaunchArgs([]string{
		"--disable-extensions",
		"--disable-extensions=except-component-extensions-with-background-pages",
		"--disable-extensions-except=/old/extension",
		"--no-first-run",
	}, extDir)
	if !hasExtensionDirInLaunchArgs(got, extDir) {
		t.Fatalf("assigned extension missing from launch args: %#v", got)
	}
	for _, arg := range got {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(arg)), "--disable-extensions") {
			t.Fatalf("extension blocking argument survived explicit assignment: %q", arg)
		}
	}
}

func TestPreserveAssignedExtensionArgsDuringProfileEdit(t *testing.T) {
	root := t.TempDir()
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	extDir := filepath.Join(root, extID)
	got := preserveAssignedExtensionArgs(
		[]string{"--load-extension=" + extDir},
		[]string{"--disable-extensions", "--no-first-run"},
	)
	if !hasExtensionDirInLaunchArgs(got, extDir) {
		t.Fatalf("manual extension assignment was not preserved: %#v", got)
	}
	for _, arg := range got {
		if strings.EqualFold(arg, "--disable-extensions") {
			t.Fatalf("extension-blocking flag was not removed: %#v", got)
		}
	}
}

func TestProfileExtensionRegistryMergesAndRemovesAssignments(t *testing.T) {
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	entries := upsertProfileExtensionAssignments(nil, profileExtensionRegistryEntry{
		DownloadAddress: extID,
		ExtensionID:     extID,
		ProfileIDs:      []string{"profile-1", "profile-2"},
	})
	entries = upsertProfileExtensionAssignments(entries, profileExtensionRegistryEntry{
		DownloadAddress: "https://chromewebstore.google.com/detail/metamask/" + extID,
		ExtensionID:     extID,
		ProfileIDs:      []string{"profile-2", "profile-3"},
	})
	if len(entries) != 1 || !reflect.DeepEqual(entries[0].ProfileIDs, []string{"profile-1", "profile-2", "profile-3"}) {
		t.Fatalf("unexpected merged assignments: %#v", entries)
	}
	entries, changed := removeProfileExtensionAssignments(entries, extID, []string{"profile-2"})
	if !changed || len(entries) != 1 || !reflect.DeepEqual(entries[0].ProfileIDs, []string{"profile-1", "profile-3"}) {
		t.Fatalf("unexpected assignments after removal: %#v changed=%v", entries, changed)
	}
}

func TestProfileHasEquivalentExtensionByIDOrName(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "Default")
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"manifest": map[string]any{"name": "MetaMask"},
				},
			},
		},
	}
	data, _ := json.Marshal(prefs)
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "Preferences"), data, 0644); err != nil {
		t.Fatal(err)
	}

	if !profileHasEquivalentExtension(root, extID, "") {
		t.Fatal("existing extension ID should be detected during explicit distribution")
	}
	if !profileHasEquivalentExtension(root, "differentid", "MetaMask") {
		t.Fatal("existing extension name should be detected during explicit distribution")
	}
	if profileHasEquivalentExtension(root, "differentid", "Rabby Wallet") {
		t.Fatal("different extension should not be treated as equivalent")
	}
}

func TestEnableExtensionDeveloperModePreservesExistingSettings(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "Default")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{"keep": map[string]any{"state": float64(1)}},
		},
	}
	data, _ := json.Marshal(prefs)
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "Preferences"), data, 0644); err != nil {
		t.Fatal(err)
	}

	enableExtensionDeveloperMode(root)

	outData, err := os.ReadFile(filepath.Join(profileDir, "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	extensions := out["extensions"].(map[string]any)
	ui := extensions["ui"].(map[string]any)
	if ui["developer_mode"] != true {
		t.Fatalf("developer mode was not enabled: %#v", ui)
	}
	settings := extensions["settings"].(map[string]any)
	if _, ok := settings["keep"]; !ok {
		t.Fatal("existing extension settings were overwritten")
	}
}

func TestBrowserProfileCreateInheritsGlobalExtensionAtCreationOnly(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	extDir := app.globalExtensionDir(extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.saveGlobalExtensionRegistry(globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{{
		DownloadAddress: extID,
		ExtensionID:     extID,
	}}}); err != nil {
		t.Fatal(err)
	}

	profile, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: "实例-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasExtensionDirInLaunchArgs(profile.LaunchArgs, extDir) {
		t.Fatalf("new profile did not inherit the explicit global choice: %#v", profile.LaunchArgs)
	}
	prefs, err := os.ReadFile(filepath.Join(app.browserMgr.ResolveUserDataDir(profile), "Default", "Preferences"))
	if err != nil || !strings.Contains(string(prefs), `"developer_mode": true`) {
		t.Fatalf("developer mode was not enabled at profile creation: %s err=%v", prefs, err)
	}
}

func TestBrowserProfileUpdateDoesNotEraseAssignedExtension(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	extDir := filepath.Join(root, "extensions", "imported", "wallet")
	created, err := app.browserMgr.Create(BrowserProfileInput{
		ProfileName: "实例-1",
		LaunchArgs:  []string{"--load-extension=" + extDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := app.BrowserProfileUpdate(created.ProfileId, BrowserProfileInput{
		ProfileName: "实例-1",
		LaunchArgs:  []string{"--no-first-run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasExtensionDirInLaunchArgs(updated.LaunchArgs, extDir) {
		t.Fatalf("profile edit erased assigned extension: %#v", updated.LaunchArgs)
	}
}

func TestDownloadAndInstallExtensionReusesExistingPackageWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	extDir := app.globalExtensionDir(extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(extDir, "user-data.marker")
	if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	gotID, gotDir, previous, current, err := app.downloadAndInstallExtension(extID)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != extID || gotDir != extDir || previous != "1.0" || current != "1.0" {
		t.Fatalf("unexpected reused package result: %s %s %s %s", gotID, gotDir, previous, current)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("existing extension package was overwritten: data=%q err=%v", data, err)
	}
}
