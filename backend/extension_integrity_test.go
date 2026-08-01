package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrityCompleteWhenNoExtensions(t *testing.T) {
	dir := t.TempDir()
	if !isExtensionAssignmentComplete(dir, nil) {
		t.Fatal("empty assignment must be complete")
	}
}

func TestIntegrityMarkerFastPathAndClearOnStaleData(t *testing.T) {
	root := t.TempDir()
	extID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1","manifest_version":3,"permissions":["storage"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	args := []string{"--load-extension=" + pkg}

	// Without durable LES → incomplete.
	if isExtensionAssignmentComplete(userData, args) {
		t.Fatal("prefs-only must not be complete")
	}

	// LES + registration → complete + marker.
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(les, "000003.log"), []byte("vault"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installUnpackedExtensionIntoProfile(userData, pkg); err != nil {
		t.Fatal(err)
	}
	if !isExtensionAssignmentComplete(userData, args) {
		t.Fatal("with LES must be complete")
	}
	if _, ok := readExtensionIntegrityMarker(userData); !ok {
		t.Fatal("marker must be written")
	}
	// Second call uses marker fast path.
	if !isExtensionAssignmentComplete(userData, args) {
		t.Fatal("marker path must stay complete")
	}

	// Wipe LES + disable prefs entry → must be incomplete.
	_ = os.RemoveAll(les)
	clearExtensionIntegrityMarker(userData)
	// Remove Preferences settings so read-only detect has nothing left.
	_ = os.Remove(filepath.Join(userData, "Default", "Preferences"))
	if isExtensionAssignmentComplete(userData, args) {
		t.Fatal("after wiping LES and prefs must be incomplete")
	}
}

func TestMarkExtensionIntegrityIfComplete(t *testing.T) {
	root := t.TempDir()
	extID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	args := []string{"--load-extension=" + pkg}
	if markExtensionIntegrityIfComplete(userData, "p1", args) {
		t.Fatal("must not mark without durable data")
	}
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	_ = os.MkdirAll(les, 0700)
	_ = os.WriteFile(filepath.Join(les, "x"), []byte("1"), 0600)
	_ = installUnpackedExtensionIntoProfile(userData, pkg)
	if !markExtensionIntegrityIfComplete(userData, "p1", args) {
		t.Fatal("must mark when durable data present")
	}
}
