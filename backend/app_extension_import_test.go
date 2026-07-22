package backend

import (
	"archive/zip"
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

func TestAppendGlobalExtensionLaunchArgsSurvivesNewProfiles(t *testing.T) {
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

	got := app.appendGlobalExtensionLaunchArgs([]string{"--no-first-run"})
	if !hasExtensionDirInLaunchArgs(got, extDir) {
		t.Fatalf("global extension was not injected into fresh launch args: %#v", got)
	}
	got = app.appendGlobalExtensionLaunchArgs(got)
	active := activeLoadExtensionDirs(got)
	if len(active) != 1 {
		t.Fatalf("global extension should be de-duplicated, got %#v", got)
	}
}

func TestCleanupStaleManagedUnpackedExtensionsRemovesOldDuplicateByManifestName(t *testing.T) {
	root := t.TempDir()
	userDataDir := filepath.Join(root, "profile")
	profileDir := filepath.Join(userDataDir, "Default")
	activeExt := filepath.Join(root, "extensions", "imported", "nkbihfbeogaeaoehlefnkodbefgpgknn")
	oldExt := filepath.Join(root, "extensions", "imported", "old-metamask")
	if err := os.MkdirAll(activeExt, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeExt, "manifest.json"), []byte(`{"name":"MetaMask","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(profileDir, "Extensions", "oldid"), 0755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				"oldid": map[string]any{
					"path":     oldExt,
					"manifest": map[string]any{"name": "MetaMask"},
				},
				"keepid": map[string]any{
					"path":     activeExt,
					"manifest": map[string]any{"name": "MetaMask"},
				},
				"webstoreid": map[string]any{
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

	cleanupStaleManagedUnpackedExtensions(userDataDir, []string{"--load-extension=" + activeExt}, root)

	outData, err := os.ReadFile(filepath.Join(profileDir, "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	settings := out["extensions"].(map[string]any)["settings"].(map[string]any)
	if _, ok := settings["oldid"]; ok {
		t.Fatalf("old duplicate unpacked extension setting was not removed")
	}
	if _, ok := settings["keepid"]; !ok {
		t.Fatalf("active extension setting should be kept")
	}
	if _, ok := settings["webstoreid"]; !ok {
		t.Fatalf("extension without path should be kept")
	}
	if _, err := os.Stat(filepath.Join(profileDir, "Extensions", "oldid")); !os.IsNotExist(err) {
		t.Fatalf("old extension profile data should be removed")
	}
}

func TestCleanupRemovedManagedExtensionRemovesPinnedAndProfileData(t *testing.T) {
	root := t.TempDir()
	userDataDir := filepath.Join(root, "profile")
	profileDir := filepath.Join(userDataDir, "Default")
	extID := "mcohilncbfahbmgdjkbpemcciiolgcge"
	extDir := filepath.Join(root, "extensions", "imported", extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"OKX Wallet","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(profileDir, "Extensions", extID), 0755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"path":     extDir,
					"manifest": map[string]any{"name": "OKX Wallet"},
				},
			},
			"pinned_extensions": []any{extID, "otherext"},
		},
	}
	data, _ := json.Marshal(prefs)
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "Preferences"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cleanupRemovedManagedExtension(userDataDir, extDir, extID, root)

	outData, err := os.ReadFile(filepath.Join(profileDir, "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	extensions := out["extensions"].(map[string]any)
	settings := extensions["settings"].(map[string]any)
	if _, ok := settings[extID]; ok {
		t.Fatalf("removed extension should be deleted from settings")
	}
	pinned := extensions["pinned_extensions"].([]any)
	if len(pinned) != 1 || pinned[0].(string) != "otherext" {
		t.Fatalf("unexpected pinned_extensions after cleanup: %#v", pinned)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "Extensions", extID)); !os.IsNotExist(err) {
		t.Fatalf("removed extension profile data should be deleted")
	}
}
