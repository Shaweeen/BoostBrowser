package backend

import (
	"archive/zip"
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type failingExtensionProfileDAO struct{}

func (failingExtensionProfileDAO) List() ([]*browser.Profile, error) {
	return nil, nil
}

func (failingExtensionProfileDAO) GetById(string) (*browser.Profile, error) {
	return nil, errors.New("not implemented")
}

func (failingExtensionProfileDAO) Upsert(*browser.Profile) error {
	return errors.New("forced persistence failure")
}

func (failingExtensionProfileDAO) UpsertMany([]*browser.Profile) error {
	return errors.New("forced persistence failure")
}

func (failingExtensionProfileDAO) Delete(string) error {
	return errors.New("not implemented")
}

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
	_, _, _, err := installUnpackedExtension(root, "example", extensionZipForTest(t, `{"name":"broken"}`), nil)
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
	if err := os.MkdirAll(extDir+".previous", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir+".previous", "manifest.json"), []byte(`{"name":"Wallet","version":"0.9","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	installed, previous, current, err := installUnpackedExtension(root, "example", extensionZipForTest(t, `{"name":"Wallet","version":"2.0","manifest_version":3}`), nil)
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

func TestNormalizeLoadExtensionArgsKeepsLegacyMixedCaseSwitch(t *testing.T) {
	args := normalizeLoadExtensionArgs([]string{
		"--LOAD-EXTENSION=C:\\extensions\\wallet",
		"--load-extension=C:\\extensions\\wallet,C:\\extensions\\tool",
		"--disable-sync",
	})
	joined := strings.Join(args, " ")
	if strings.Count(strings.ToLower(joined), "--load-extension=") != 1 {
		t.Fatalf("mixed-case legacy load switches must collapse into one: %v", args)
	}
	if !strings.Contains(joined, `C:\extensions\wallet`) || !strings.Contains(joined, `C:\extensions\tool`) {
		t.Fatalf("legacy extension paths were lost: %v", args)
	}
}

func TestGlobalExtensionRegistryUpsertUsesExtensionIDAsIdentity(t *testing.T) {
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	entries := []globalExtensionRegistryEntry{{
		DownloadAddress: "https://old.example/" + extID,
		ExtensionID:     extID,
		ProfileIDs:      []string{"profile-old"},
	}}
	got := upsertGlobalExtensionRegistryEntry(entries, globalExtensionRegistryEntry{
		DownloadAddress: "https://chromewebstore.google.com/detail/metamask/" + extID,
		ExtensionID:     extID,
		ProfileIDs:      []string{"profile-1", "profile-2", "profile-2"},
	})
	if len(got) != 1 {
		t.Fatalf("expected one global policy, got %d", len(got))
	}
	if got[0].DownloadAddress != "https://chromewebstore.google.com/detail/metamask/"+extID {
		t.Fatalf("global policy address was not updated: %#v", got[0])
	}
	if !reflect.DeepEqual(got[0].ProfileIDs, []string{"profile-1", "profile-2"}) {
		t.Fatalf("global completion set was not replaced and normalized: %#v", got[0])
	}
	completed := globalExtensionCompletedProfiles(got, extID, "")
	if !completed["profile-1"] || !completed["profile-2"] || completed["profile-old"] {
		t.Fatalf("unexpected completed profile set: %#v", completed)
	}
}

func TestResolveExtensionDownloadURLUsesBundledChromeVersion(t *testing.T) {
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	got := resolveExtensionDownloadURL(extID, extID)
	if !strings.Contains(got, "prodversion="+managedExtensionChromeVersion) {
		t.Fatalf("extension download URL does not match bundled Chrome %s: %s", managedExtensionChromeVersion, got)
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

	// Without a valid package path, residue prefs must NOT block re-import/heal.
	if profileHasLoadableEquivalentExtension(root, extID, "", "") {
		t.Fatal("prefs without loadable path must not count as loadable equivalent")
	}
	// Add loadable path → then detect.
	pkg := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"MetaMask","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(pkg)
	prefs2 := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"state":    float64(1),
					"path":     abs,
					"manifest": map[string]any{"name": "MetaMask"},
				},
			},
		},
	}
	data2, _ := json.Marshal(prefs2)
	if err := os.WriteFile(filepath.Join(profileDir, "Preferences"), data2, 0644); err != nil {
		t.Fatal(err)
	}
	if !profileHasLoadableEquivalentExtension(root, extID, "", pkg) {
		t.Fatal("loadable prefs path must be detected")
	}
	if !profileHasLoadableEquivalentExtension(root, "differentid", "MetaMask", "") {
		t.Fatal("loadable same-name extension must be detected")
	}
	if profileHasLoadableEquivalentExtension(root, "differentid", "Rabby Wallet", "") {
		t.Fatal("different extension should not be treated as equivalent")
	}
}

func TestBrowserProfileCreateDoesNotApplyGlobalExtensionWithoutDistribution(t *testing.T) {
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
	if hasExtensionDirInLaunchArgs(profile.LaunchArgs, extDir) {
		t.Fatalf("new profile must wait for an explicit distribution action: %#v", profile.LaunchArgs)
	}
	prefsPath := filepath.Join(app.browserMgr.ResolveUserDataDir(profile), "Default", "Preferences")
	if prefs, err := os.ReadFile(prefsPath); err == nil && strings.Contains(string(prefs), `"developer_mode": true`) {
		t.Fatalf("profile creation unexpectedly changed extension preferences: %s", prefs)
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

func TestBindExtensionRestoresInMemoryProfileWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.ProfileDAO = failingExtensionProfileDAO{}
	app.browserMgr.Profiles["profile-1"] = &browser.Profile{
		ProfileId:   "profile-1",
		ProfileName: "one",
		LaunchArgs:  []string{"--no-first-run"},
		UpdatedAt:   "before",
	}

	if _, err := app.bindExtensionDirToProfiles([]string{"profile-1"}, filepath.Join(root, "extension")); err == nil {
		t.Fatal("persistence failure was expected")
	}
	got := app.browserMgr.Profiles["profile-1"]
	if !reflect.DeepEqual(got.LaunchArgs, []string{"--no-first-run"}) || got.UpdatedAt != "before" {
		t.Fatalf("failed write leaked into in-memory profile: %+v", got)
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

func testRSAPublicKeyAndID(t *testing.T) ([]byte, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pubDER, extensionIDFromPublicKey(pubDER)
}

func buildCRX2ForTest(t *testing.T, publicKey, zipPayload []byte) []byte {
	t.Helper()
	// CRX2 layout: magic, version, pubLen, sigLen, publicKey, signature, zip
	signature := make([]byte, 128)
	out := make([]byte, 0, 16+len(publicKey)+len(signature)+len(zipPayload))
	out = append(out, 'C', 'r', '2', '4')
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(publicKey)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(signature)))
	out = append(out, publicKey...)
	out = append(out, signature...)
	out = append(out, zipPayload...)
	return out
}

func buildCRX3ForTest(t *testing.T, publicKey, zipPayload []byte) []byte {
	t.Helper()
	// Minimal CrxFileHeader with one sha256_with_rsa proof (field 2) containing
	// public_key (field 1). Signature bytes are ignored by the importer.
	proof := appendProtobufBytesField(nil, 1, publicKey)
	proof = appendProtobufBytesField(proof, 2, bytes.Repeat([]byte{0x11}, 32))
	header := appendProtobufBytesField(nil, 2, proof)

	out := make([]byte, 0, 12+len(header)+len(zipPayload))
	out = append(out, 'C', 'r', '2', '4')
	out = binary.LittleEndian.AppendUint32(out, 3)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(header)))
	out = append(out, header...)
	out = append(out, zipPayload...)
	return out
}

func appendProtobufBytesField(buf []byte, fieldNumber int, value []byte) []byte {
	tag := uint64(fieldNumber<<3 | 2)
	buf = appendProtobufVarint(buf, tag)
	buf = appendProtobufVarint(buf, uint64(len(value)))
	return append(buf, value...)
}

func appendProtobufVarint(buf []byte, value uint64) []byte {
	for value >= 0x80 {
		buf = append(buf, byte(value)|0x80)
		value >>= 7
	}
	return append(buf, byte(value))
}

func TestExtensionIDFromPublicKeyIsStableChromeMapping(t *testing.T) {
	// SHA-256 first 16 bytes of empty input, remapped 0-f -> a-p.
	emptySum := sha256.Sum256(nil)
	want := make([]byte, 0, 32)
	const alphabet = "abcdefghijklmnop"
	for _, b := range emptySum[:16] {
		want = append(want, alphabet[b>>4], alphabet[b&0x0f])
	}
	if got := extensionIDFromPublicKey(nil); got != "" {
		t.Fatalf("empty key should yield empty id, got %q", got)
	}
	if got := extensionIDFromPublicKey([]byte{}); got != "" {
		t.Fatalf("empty key should yield empty id, got %q", got)
	}
	// Any non-empty key must be 32 a-p chars.
	pub, id := testRSAPublicKeyAndID(t)
	if len(id) != 32 || !isWebStoreExtensionID(id) {
		t.Fatalf("derived id invalid: %q from %d-byte key", id, len(pub))
	}
	if extensionIDFromPublicKey(pub) != id {
		t.Fatal("derived extension id is not stable")
	}
	_ = want
}

func TestExtractZipAndPublicKeyFromCRX2AndCRX3(t *testing.T) {
	pub, wantID := testRSAPublicKeyAndID(t)
	zipPayload := extensionZipForTest(t, `{"name":"Wallet","version":"1.0","manifest_version":3}`)

	for _, tc := range []struct {
		name string
		crx  []byte
	}{
		{name: "crx2", crx: buildCRX2ForTest(t, pub, zipPayload)},
		{name: "crx3", crx: buildCRX3ForTest(t, pub, zipPayload)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotZip, gotPub, err := extractZipAndPublicKeyFromCRX(tc.crx)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotZip, zipPayload) {
				t.Fatalf("zip payload mismatch")
			}
			if !bytes.Equal(gotPub, pub) {
				t.Fatalf("public key mismatch")
			}
			if extensionIDFromPublicKey(gotPub) != wantID {
				t.Fatalf("id mismatch: got %s want %s", extensionIDFromPublicKey(gotPub), wantID)
			}
		})
	}

	// Pure ZIP has no public key.
	zipOnly, pubOnly, err := extractZipAndPublicKeyFromCRX(zipPayload)
	if err != nil || !bytes.Equal(zipOnly, zipPayload) || len(pubOnly) != 0 {
		t.Fatalf("pure zip handling failed: zipEqual=%v pub=%d err=%v", bytes.Equal(zipOnly, zipPayload), len(pubOnly), err)
	}
}

func TestInstallUnpackedExtensionInjectsManifestKeyForStableWalletID(t *testing.T) {
	root := t.TempDir()
	pub, wantID := testRSAPublicKeyAndID(t)
	zipPayload := extensionZipForTest(t, `{"name":"Wallet","version":"3.0","manifest_version":3,"description":"test"}`)

	installed, previous, current, err := installUnpackedExtension(root, wantID, zipPayload, pub)
	if err != nil {
		t.Fatal(err)
	}
	if previous != "" || current != "3.0" {
		t.Fatalf("unexpected versions previous=%q current=%q", previous, current)
	}
	if !extensionManifestHasStableKey(installed, wantID) {
		t.Fatal("installed package must carry a manifest key that yields the official extension id")
	}
	// Existing description must survive key injection.
	data, err := os.ReadFile(filepath.Join(installed, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["description"] != "test" {
		t.Fatalf("manifest fields were not preserved: %#v", manifest)
	}
	if got, _ := manifest["key"].(string); got != base64.StdEncoding.EncodeToString(pub) {
		t.Fatalf("manifest key mismatch")
	}
}

func TestEnsureManifestPublicKeyIsIdempotentAndRepairsMissingKey(t *testing.T) {
	dir := t.TempDir()
	pub, wantID := testRSAPublicKeyAndID(t)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"Wallet","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if extensionManifestHasStableKey(dir, wantID) {
		t.Fatal("missing key should be detected")
	}
	if err := ensureManifestPublicKey(dir, pub); err != nil {
		t.Fatal(err)
	}
	if !extensionManifestHasStableKey(dir, wantID) {
		t.Fatal("key injection failed")
	}
	// Second write with the same key must be a no-op for content.
	before, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err := ensureManifestPublicKey(dir, pub); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("identical key rewrite should be skipped")
	}
}

func TestRepairLoadExtensionStableIDsSkipsPackagesWithKey(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	pub, wantID := testRSAPublicKeyAndID(t)
	extDir := filepath.Join(root, "extensions", "imported", wantID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":             "Wallet",
		"version":          "1.0",
		"manifest_version": 3,
		"key":              base64.StdEncoding.EncodeToString(pub),
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	// No network needed: already stable.
	app.repairLoadExtensionStableIDs([]string{"--load-extension=" + extDir})
	if !extensionManifestHasStableKey(extDir, wantID) {
		t.Fatal("stable package was damaged by repair pass")
	}
}

func TestGlobalExtensionDistributionChecksOnlyNewProfilesOnExplicitAction(t *testing.T) {
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

	existing, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	absPkg, err := filepath.Abs(extDir)
	if err != nil {
		t.Fatal(err)
	}
	// Loadable registration (ENABLED + valid package path): distribution must skip.
	existingPrefs := filepath.Join(app.browserMgr.ResolveUserDataDir(existing), "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(existingPrefs), 0755); err != nil {
		t.Fatal(err)
	}
	loadablePrefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"state":    float64(1),
					"path":     absPkg,
					"manifest": map[string]any{"name": "MetaMask"},
				},
			},
		},
	}
	raw, _ := json.Marshal(loadablePrefs)
	if err := os.WriteFile(existingPrefs, raw, 0644); err != nil {
		t.Fatal(err)
	}
	beforePrefs, _ := os.ReadFile(existingPrefs)

	first, err := app.BrowserGlobalExtensionImport(extID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.UpdatedProfiles, []string{missing.ProfileId}) {
		t.Fatalf("explicit distribution should bind only the missing profile: %#v", first.UpdatedProfiles)
	}
	afterPrefs, err := os.ReadFile(existingPrefs)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforePrefs) != string(afterPrefs) {
		t.Fatalf("loadable existing profile Preferences must not be rewritten: before=%s after=%s", beforePrefs, afterPrefs)
	}

	second, err := app.BrowserGlobalExtensionImport(extID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.UpdatedProfiles) != 0 {
		t.Fatalf("completed profiles were checked or rebound again: %#v", second.UpdatedProfiles)
	}

	createdLater, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: "created-later"})
	if err != nil {
		t.Fatal(err)
	}
	if hasExtensionDirInLaunchArgs(createdLater.LaunchArgs, extDir) {
		t.Fatalf("new profile inherited a global extension before distribution: %#v", createdLater.LaunchArgs)
	}
	third, err := app.BrowserGlobalExtensionImport(extID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(third.UpdatedProfiles, []string{createdLater.ProfileId}) {
		t.Fatalf("next explicit distribution should bind only the new profile: %#v", third.UpdatedProfiles)
	}
}

func TestAssignRecordsStoppedProfileWithoutRewritingChromePreferences(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	extDir := app.globalExtensionDir(extID)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"11.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	profile, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.BrowserProfileImportExtension([]string{profile.ProfileId}, extID)
	if err != nil {
		t.Fatal(err)
	}
	if result.PrefsInstalledCount != 0 {
		t.Fatalf("assignment must not write Chromium Preferences, got prefs=%d msg=%q", result.PrefsInstalledCount, result.Message)
	}
	if result.SkippedCount != 0 {
		t.Fatalf("fresh profile should not skip: %#v", result)
	}
	updated, ok := app.browserMgr.Profiles[profile.ProfileId]
	if !ok || updated == nil || !hasExtensionDirInLaunchArgs(updated.LaunchArgs, extDir) {
		t.Fatalf("assignment must persist --load-extension: %#v", updated)
	}

	// Second assign: same extension → skip, without touching profile data.
	second, err := app.BrowserProfileImportExtension([]string{profile.ProfileId}, extID)
	if err != nil {
		t.Fatal(err)
	}
	if second.SkippedCount != 1 || len(second.UpdatedProfiles) != 0 {
		t.Fatalf("same extension must skip: %#v", second)
	}
	userData := app.browserMgr.ResolveUserDataDir(profile)
	if _, err := os.Stat(filepath.Join(userData, "Default", "Preferences")); !os.IsNotExist(err) {
		t.Fatalf("assignment must not create or rewrite Chromium Preferences, err=%v", err)
	}
	if !strings.Contains(second.Message, "跳过") && !strings.Contains(second.Message, "已存在") {
		t.Fatalf("skip message should be clear: %q", second.Message)
	}
}

func TestGlobalExtensionDistributionHealsUnusableRegistration(t *testing.T) {
	// Preferences residue must not block an explicit user action, but it belongs
	// to Chromium and must never be healed by rewriting the profile file.
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
	existing, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "residue"})
	if err != nil {
		t.Fatal(err)
	}
	prefsPath := filepath.Join(app.browserMgr.ResolveUserDataDir(existing), "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(prefsPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefsPath, []byte(`{"extensions":{"settings":{"nkbihfbeogaeaoehlefnkodbefgpgknn":{"manifest":{"name":"MetaMask"}}}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	beforePrefs, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.BrowserGlobalExtensionImport(extID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.UpdatedProfiles) != 1 || result.UpdatedProfiles[0] != existing.ProfileId {
		t.Fatalf("residue without loadable path must re-bind for heal: %#v", result.UpdatedProfiles)
	}
	updated := app.browserMgr.Profiles[existing.ProfileId]
	if updated == nil || !hasExtensionDirInLaunchArgs(updated.LaunchArgs, extDir) {
		t.Fatalf("after re-bind, launch assignment must be present: %#v", updated)
	}
	afterPrefs, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforePrefs) != string(afterPrefs) {
		t.Fatalf("assignment must preserve Chromium Preferences exactly: before=%s after=%s", beforePrefs, afterPrefs)
	}
}

func TestSyncKnownExtensionsSkipsExistingLoadableExtension(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	extID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	globalDir := app.globalExtensionDir(extID)
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.saveGlobalExtensionRegistry(globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{{
		DownloadAddress: extID,
		ExtensionID:     extID,
	}}}); err != nil {
		t.Fatal(err)
	}

	existing, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "已有扩展"})
	if err != nil {
		t.Fatal(err)
	}
	manualDir := filepath.Join(root, "manual", extID)
	if err := os.MkdirAll(manualDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manualDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"0.9","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	existingData := app.browserMgr.ResolveUserDataDir(existing)
	if err := installUnpackedExtensionIntoProfile(existingData, manualDir); err != nil {
		t.Fatal(err)
	}
	prefsPath := filepath.Join(existingData, "Default", "Preferences")
	before, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}

	missing, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "待同步"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.BrowserExtensionSyncKnownToProfiles([]string{existing.ProfileId, missing.ProfileId})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.UpdatedProfiles) != 1 || result.UpdatedProfiles[0] != missing.ProfileId || result.SkippedCount != 1 {
		t.Fatalf("sync result must bind only missing profile: %#v", result)
	}
	after, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("known-extension sync must not rewrite an existing loadable extension registration")
	}
	if hasExtensionDirInLaunchArgs(app.browserMgr.Profiles[existing.ProfileId].LaunchArgs, globalDir) {
		t.Fatalf("existing extension must not gain duplicate managed launch arg: %#v", app.browserMgr.Profiles[existing.ProfileId].LaunchArgs)
	}
	if !hasExtensionDirInLaunchArgs(app.browserMgr.Profiles[missing.ProfileId].LaunchArgs, globalDir) {
		t.Fatalf("missing profile must receive managed launch arg: %#v", app.browserMgr.Profiles[missing.ProfileId].LaunchArgs)
	}
}

func TestProfileDeletionArchivesOwnedDataRetainsSnapshotsAndRemovesExtensionReferences(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	profile, err := app.browserMgr.Create(BrowserProfileInput{ProfileName: "delete-me"})
	if err != nil {
		t.Fatal(err)
	}
	userDataDir := app.browserMgr.ResolveUserDataDir(profile)
	if err := os.MkdirAll(filepath.Join(userDataDir, "Default", "Extensions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDataDir, "Default", "Cookies"), []byte("session"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshotDir := filepath.Join(root, "data", "snapshots", profile.ProfileId)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := app.saveProfileExtensionRegistry(profileExtensionRegistry{Extensions: []profileExtensionRegistryEntry{{
		DownloadAddress: "manual",
		ExtensionID:     "manual-extension",
		ProfileIDs:      []string{profile.ProfileId},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := app.saveGlobalExtensionRegistry(globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{{
		DownloadAddress: "global",
		ExtensionID:     "global-extension",
		ProfileIDs:      []string{profile.ProfileId},
	}}}); err != nil {
		t.Fatal(err)
	}

	if err := app.BrowserProfileDeleteWithCache(profile.ProfileId, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userDataDir); !os.IsNotExist(err) {
		t.Fatalf("active user-data path survived archival: %v", err)
	}
	archives, err := browser.ListProfileDataArchives(app.browserMgr.ProfileRecoveryArchiveRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 1 || !archives[0].DataAvailable {
		t.Fatalf("environment recovery archive missing: %+v", archives)
	}
	if _, err := os.Stat(snapshotDir); err != nil {
		t.Fatalf("profile snapshots must remain recoverable: %v", err)
	}
	assignments, err := app.loadProfileExtensionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments.Extensions) != 0 {
		t.Fatalf("manual extension references survived profile deletion: %#v", assignments.Extensions)
	}
	global, err := app.loadGlobalExtensionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(global.Extensions) != 1 || len(global.Extensions[0].ProfileIDs) != 0 {
		t.Fatalf("global completion reference survived profile deletion: %#v", global.Extensions)
	}
}
