package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestObservedProfileExtensionIDsRecordsIdentityWithoutReadingValues(t *testing.T) {
	userData := t.TempDir()
	metaMask := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	rabby := "acmacodkjbdgmoleebolmdjonilkdbch"
	for _, dir := range []string{
		filepath.Join(userData, "Default", "Local Extension Settings", metaMask),
		filepath.Join(userData, "Default", "IndexedDB", "chrome-extension_"+rabby+"_0.indexeddb.leveldb"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userData, "Default", "Local Extension Settings", metaMask, "000003.log"), []byte("encrypted-wallet-state"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := observedProfileExtensionIDs(userData, nil)
	want := []string{rabby, metaMask}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("observed ids = %v, want %v", got, want)
	}
	if orphaned := orphanedProfileExtensionIDs(userData, nil, got); !reflect.DeepEqual(orphaned, want) {
		t.Fatalf("orphaned ids = %v, want %v", orphaned, want)
	}

	prefs := `{"extensions":{"settings":{"` + metaMask + `":{"state":0}}}}`
	if err := os.WriteFile(filepath.Join(userData, "Default", "Preferences"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	if orphaned := orphanedProfileExtensionIDs(userData, nil, got); !reflect.DeepEqual(orphaned, []string{rabby}) {
		t.Fatalf("registered user-disabled extension must not be restored: %v", orphaned)
	}
	securePrefs := `{"extensions":{"settings":{"` + rabby + `":{"state":1}}}}`
	if err := os.WriteFile(filepath.Join(userData, "Default", "Secure Preferences"), []byte(securePrefs), 0o600); err != nil {
		t.Fatal(err)
	}
	if orphaned := orphanedProfileExtensionIDs(userData, nil, got); len(orphaned) != 0 {
		t.Fatalf("Secure Preferences registration must suppress recovery: %v", orphaned)
	}
}

func TestProfileExtensionInventoryPersistsOnlyExtensionIDs(t *testing.T) {
	app := NewApp(t.TempDir())
	inventory := profileExtensionInventory{
		Profiles: map[string][]string{
			"profile-1": {
				"nkbihfbeogaeaoehlefnkodbefgpgknn",
				"INVALID",
				chromiumWebStoreHelperExtensionID,
			},
		},
	}
	if err := app.saveProfileExtensionInventory(inventory); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.loadProfileExtensionInventory()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nkbihfbeogaeaoehlefnkodbefgpgknn"}
	if !reflect.DeepEqual(loaded.Profiles["profile-1"], want) {
		t.Fatalf("inventory ids = %v, want %v", loaded.Profiles["profile-1"], want)
	}
	data, err := os.ReadFile(app.profileExtensionInventoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || filepath.Base(app.profileExtensionInventoryPath()) != profileExtensionInventoryFilename {
		t.Fatal("inventory was not written under the persistent data path")
	}
}

func TestAppendPreparedExtensionRecoveryArgsKeepsExactPackagePaths(t *testing.T) {
	paths := []string{filepath.Join("data", "extensions", "imported", "a"), filepath.Join("data", "extensions", "imported", "b")}
	got := appendPreparedExtensionRecoveryArgs([]string{"--disable-extensions", "--proxy-server=direct://"}, paths)
	joined := ""
	for _, arg := range got {
		joined += arg + "\n"
	}
	if strings.Contains(strings.ToLower(joined), "--disable-extensions") || !strings.Contains(strings.ToLower(joined), strings.ToLower(paths[0])) || !strings.Contains(strings.ToLower(joined), strings.ToLower(paths[1])) {
		t.Fatalf("prepared recovery args = %v", got)
	}
}
