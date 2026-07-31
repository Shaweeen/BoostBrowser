package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAssignmentFingerprintStableAndOrderIndependent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := filepath.Join(dir, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(a, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0755); err != nil {
		t.Fatal(err)
	}
	fp1, ids1 := assignmentFingerprintFromLaunchArgs([]string{
		"--load-extension=" + b + "," + a,
	})
	fp2, ids2 := assignmentFingerprintFromLaunchArgs([]string{
		"--load-extension=" + a,
		"--other=1",
		"--load-extension=" + b,
	})
	if fp1 == "" || fp1 != fp2 {
		t.Fatalf("fingerprint mismatch: %q vs %q", fp1, fp2)
	}
	if len(ids1) != 2 || len(ids2) != 2 {
		t.Fatalf("ids=%v %v", ids1, ids2)
	}
}

func TestExtensionLaunchPrepReadyRequiresMatchingMarker(t *testing.T) {
	userData := t.TempDir()
	fp, ids := assignmentFingerprintFromLaunchArgs([]string{
		"--load-extension=" + filepath.Join(userData, "cccccccccccccccccccccccccccccccc"),
	})
	if isExtensionLaunchPrepReady(userData, fp) {
		t.Fatal("missing marker must not be ready")
	}
	if err := writeExtensionLaunchReadyMarker(userData, "p1", fp, ids); err != nil {
		t.Fatal(err)
	}
	if !isExtensionLaunchPrepReady(userData, fp) {
		t.Fatal("matching marker must be ready")
	}
	if isExtensionLaunchPrepReady(userData, fp+"x") {
		t.Fatal("fingerprint change must invalidate readiness")
	}
	clearExtensionLaunchReadyMarker(userData)
	if isExtensionLaunchPrepReady(userData, fp) {
		t.Fatal("cleared marker must not be ready")
	}
}

func TestVerifyAssignedExtensionsAgainstProfileData(t *testing.T) {
	root := t.TempDir()
	extID := "dddddddddddddddddddddddddddddddd"
	extDir := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"t","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	args := []string{"--load-extension=" + extDir}
	if verifyAssignedExtensionsAgainstProfileData(userData, args) {
		t.Fatal("missing profile data must fail verification")
	}
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0755); err != nil {
		t.Fatal(err)
	}
	// Empty scaffold must not pass — only real Chrome-written state.
	if verifyAssignedExtensionsAgainstProfileData(userData, args) {
		t.Fatal("empty LES scaffold must not satisfy verification")
	}
	if err := os.WriteFile(filepath.Join(les, "000003.log"), []byte("leveldb"), 0600); err != nil {
		t.Fatal(err)
	}
	if !verifyAssignedExtensionsAgainstProfileData(userData, args) {
		t.Fatal("Local Extension Settings with real files should satisfy verification")
	}
	// Without vault data, package with stable key is still acceptably "ready"
	// so first-open can mark and subsequent starts skip heavy prep.

	userData2 := filepath.Join(root, "user2")
	prefDir := filepath.Join(userData2, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{"state": 1},
			},
		},
	}
	raw, _ := json.Marshal(prefs)
	if err := os.WriteFile(filepath.Join(prefDir, "Preferences"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if !verifyAssignedExtensionsAgainstProfileData(userData2, args) {
		t.Fatal("Preferences extensions.settings should satisfy verification")
	}
}

func TestEmptyAssignmentUsesStartPrepMarker(t *testing.T) {
	dir := t.TempDir()
	if isExtensionLaunchPrepReady(dir, "") {
		t.Fatal("without start-prep marker, empty assignment is not ready")
	}
	markStartPrepDone(dir)
	if !isExtensionLaunchPrepReady(dir, "") {
		t.Fatal("with start-prep marker, empty assignment should skip heavy prep")
	}
}

func TestStripLoadExtensionArgs(t *testing.T) {
	got := stripLoadExtensionArgs([]string{
		"--user-data-dir=/tmp/x",
		"--load-extension=/a,/b",
		"--no-first-run",
		"--load-extension=/c",
	})
	if len(got) != 2 || got[0] != "--user-data-dir=/tmp/x" || got[1] != "--no-first-run" {
		t.Fatalf("strip failed: %#v", got)
	}
}

func TestShouldSkipLoadExtensionInjectionRequiresAdaptedProfileData(t *testing.T) {
	root := t.TempDir()
	extID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	extDir := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"t","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	if err := os.MkdirAll(userData, 0755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--load-extension=" + extDir}
	fp, ids := assignmentFingerprintFromLaunchArgs(args)
	if shouldSkipLoadExtensionInjection(userData, fp, args) {
		t.Fatal("without marker must not skip inject")
	}
	if err := writeExtensionLaunchReadyMarker(userData, "p1", fp, ids); err != nil {
		t.Fatal(err)
	}
	// Marker + package alone must not skip inject (no Preferences/LES).
	if shouldSkipLoadExtensionInjection(userData, fp, args) {
		t.Fatal("marker without adapted profile data must not skip inject")
	}
	prefDir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{"state": 1, "path": extDir},
			},
		},
	}
	raw, _ := json.Marshal(prefs)
	if err := os.WriteFile(filepath.Join(prefDir, "Preferences"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if !shouldSkipLoadExtensionInjection(userData, fp, args) {
		t.Fatal("marker + Preferences extension entry must skip inject")
	}
}

func TestCompleteAssignedDoesNotRewritePreferences(t *testing.T) {
	root := t.TempDir()
	extID := "hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh"
	extDir := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"t","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	prefDir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"extensions":{"settings":{"` + extID + `":{"account":"keep-me"}}}}`)
	prefPath := filepath.Join(prefDir, "Preferences")
	if err := os.WriteFile(prefPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	var app *App
	app.completeAssignedExtensionProfileData(userData, []string{"--load-extension=" + extDir})
	got, err := os.ReadFile(prefPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("Preferences must not be rewritten during completeAssigned: got %s", got)
	}
}

func TestEnsureEmptyExtensionSettingsScaffoldNeverTouchesExisting(t *testing.T) {
	userData := t.TempDir()
	extID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	target := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(target, "wallet-state")
	if err := os.WriteFile(vault, []byte("encrypted-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if ensureEmptyExtensionSettingsScaffold(userData, extID) {
		t.Fatal("must not recreate existing LES directory")
	}
	data, err := os.ReadFile(vault)
	if err != nil || string(data) != "encrypted-secret" {
		t.Fatalf("existing wallet state was modified: %v %q", err, data)
	}
}

func TestMergeExtensionSettingsNeverOverwritesExistingEntries(t *testing.T) {
	userData := t.TempDir()
	prefDir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	extID := "ffffffffffffffffffffffffffffffff"
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"state":   float64(1),
					"path":    "/keep/me",
					"account": "social-session",
				},
			},
			"ui": map[string]any{},
		},
	}
	raw, _ := json.Marshal(prefs)
	prefPath := filepath.Join(prefDir, "Preferences")
	if err := os.WriteFile(prefPath, raw, 0644); err != nil {
		t.Fatal(err)
	}
	_ = mergeExtensionSettingsNeverOverwrite(userData, []string{extID})
	out, err := os.ReadFile(prefPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(out, &got) != nil {
		t.Fatal("prefs parse failed")
	}
	settings := got["extensions"].(map[string]any)["settings"].(map[string]any)
	entry := settings[extID].(map[string]any)
	if entry["path"] != "/keep/me" || entry["account"] != "social-session" {
		t.Fatalf("existing extension settings were overwritten: %#v", entry)
	}
	ui := got["extensions"].(map[string]any)["ui"].(map[string]any)
	if ui["developer_mode"] != true {
		t.Fatalf("developer_mode should be enabled without touching settings: %#v", ui)
	}
}

func TestCompleteAssignedExtensionProfileDataCreatesScaffoldOnlyWhenMissing(t *testing.T) {
	root := t.TempDir()
	extID := "gggggggggggggggggggggggggggggggg"
	extDir := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Minimal manifest without key — completion must not delete package.
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"t","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	args := []string{"--load-extension=" + extDir}
	var app *App
	_, scaffolds := app.completeAssignedExtensionProfileData(userData, args)
	if scaffolds != 1 {
		t.Fatalf("expected one empty LES scaffold, got %d", scaffolds)
	}
	// Second call must not report new scaffolds or wipe anything.
	_, scaffolds2 := app.completeAssignedExtensionProfileData(userData, args)
	if scaffolds2 != 0 {
		t.Fatalf("second complete must not recreate scaffolds: %d", scaffolds2)
	}
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	entries, err := os.ReadDir(les)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("scaffold must stay empty (no fake vault writes): %d entries", len(entries))
	}
}
