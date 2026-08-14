package backend

import (
	"path/filepath"
	"testing"
)

func TestCloakWebStoreHelperUsesPersistentDataPath(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	got := app.cloakWebStoreHelperForLaunch()
	want := filepath.Join(root, "data", "extensions", cloakWebStoreHelperDirName)
	if normalizeExtensionPath(got) != normalizeExtensionPath(want) {
		t.Fatalf("helper path=%q want=%q", got, want)
	}
	if err := validateUnpackedExtensionManifest(got); err != nil {
		t.Fatalf("embedded helper was not materialized: %v", err)
	}
	if again := app.cloakWebStoreHelperForLaunch(); normalizeExtensionPath(again) != normalizeExtensionPath(got) {
		t.Fatalf("helper must be reused within one client process: first=%q second=%q", got, again)
	}
}
