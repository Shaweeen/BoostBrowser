package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testStoreExtID   = "nkbihfbeogaeaoehlefnkodbefgpgknn" // MetaMask webstore id (32 chars)
	testManagedExtID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// writeWebStorePrefs writes a Preferences file with a Chrome Web Store install
// for testStoreExtID (location=INTERNAL=1, from_webstore, NO path field — the
// shape Chrome produces for store installs).
func writeWebStorePrefs(t *testing.T, userData string) {
	t.Helper()
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				testStoreExtID: map[string]any{
					"state":         float64(1),
					"location":      float64(chromeExtLocationInternal),
					"from_webstore": true,
					"install_time":  "13300000000000000",
					"manifest": map[string]any{
						"name":             "MetaMask",
						"version":          "12.0.0",
						"manifest_version": float64(3),
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Preferences"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeTestPackage(t *testing.T, pkgDir string, name, version string) {
	t.Helper()
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"` + version + `","manifest_version":3}`
	if err := os.WriteFile(filepath.Join(pkgDir, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionSettingIsLoadableAcceptsWebStoreInstall(t *testing.T) {
	storeEntry := map[string]any{
		"state":         float64(1),
		"location":      float64(chromeExtLocationInternal),
		"from_webstore": true,
	}
	if !extensionSettingIsLoadable(storeEntry) {
		t.Fatal("enabled webstore install must count as loadable (no path needed)")
	}
	if !isChromiumManagedExtensionEntry(storeEntry) {
		t.Fatal("webstore entry must be recognized as Chrome-managed")
	}
	disabled := map[string]any{"state": float64(0), "location": float64(chromeExtLocationInternal)}
	if extensionSettingIsLoadable(disabled) {
		t.Fatal("disabled webstore install must NOT count as loadable")
	}
}

func TestIsExtensionInstalledInProfileRecognizesWebStoreInstall(t *testing.T) {
	root := t.TempDir()
	userData := filepath.Join(root, "user")
	writeWebStorePrefs(t, userData)
	// Managed package lives under <root>/ext/<id>; folder name = webstore id so
	// resolveExtensionPackageID falls back to it.
	pkg := filepath.Join(root, "ext", testStoreExtID)
	writeTestPackage(t, pkg, "MetaMask", "12.0.0")

	if !isExtensionInstalledInProfile(userData, pkg) {
		t.Fatal("webstore-installed extension must be treated as installed in profile")
	}
}

func TestEnsurePreferencesUnpackedExtensionDoesNotOverwriteWebStoreEntry(t *testing.T) {
	root := t.TempDir()
	userData := filepath.Join(root, "user")
	writeWebStorePrefs(t, userData)
	pkg := filepath.Join(root, "ext", testStoreExtID)
	writeTestPackage(t, pkg, "MetaMask", "12.0.0")

	if err := ensurePreferencesUnpackedExtension(userData, testStoreExtID, pkg, "12.0.0"); err != nil {
		t.Fatalf("ensurePreferencesUnpackedExtension: %v", err)
	}
	entry, ok := readPreferencesExtensionEntry(userData, testStoreExtID)
	if !ok {
		t.Fatal("entry must still exist")
	}
	location, _ := entry["location"].(float64)
	if location != chromeExtLocationInternal {
		t.Fatalf("webstore entry must stay INTERNAL(1), got %v (must not be converted to unpacked)", location)
	}
	if p, _ := entry["path"].(string); p != "" {
		t.Fatalf("webstore entry must not gain a managed path, got %q", p)
	}
}

// writeChromeStorePackage places a valid Chrome-managed package under
// Default/Extensions/<id>/<version>/ so dropStoreInstalledLoadExtensionArgs
// can verify the store registration is genuinely loadable.
func writeChromeStorePackage(t *testing.T, userData, extID, version string) {
	t.Helper()
	pkg := filepath.Join(userData, "Default", "Extensions", extID, version)
	writeTestPackage(t, pkg, "MetaMask", version)
}

func TestDropStoreInstalledLoadExtensionArgs(t *testing.T) {
	root := t.TempDir()
	userData := filepath.Join(root, "user")
	writeWebStorePrefs(t, userData)
	// Chrome 自己在 Default/Extensions/<id>/ 下持有可加载的商城包 → 允许剔除 CLI。
	writeChromeStorePackage(t, userData, testStoreExtID, "12.0.0")
	storePkg := filepath.Join(root, "ext", testStoreExtID)
	writeTestPackage(t, storePkg, "MetaMask", "12.0.0")
	managedPkg := filepath.Join(root, "ext", testManagedExtID)
	writeTestPackage(t, managedPkg, "Rabby", "1.0.0")

	args := []string{
		"--no-first-run",
		"--load-extension=" + storePkg + "," + managedPkg,
	}
	got := dropStoreInstalledLoadExtensionArgs(args, userData)
	joined := strings.Join(got, " ")
	if strings.Contains(joined, testStoreExtID) {
		t.Fatalf("store-installed extension must be dropped from --load-extension: %s", joined)
	}
	if !strings.Contains(joined, testManagedExtID) {
		t.Fatalf("managed unpacked extension must stay in --load-extension: %s", joined)
	}
	if !strings.Contains(joined, "--no-first-run") {
		t.Fatalf("non-extension args must be preserved: %s", joined)
	}
}

// TestDropStoreInstalledLoadExtensionArgsKeepsCLIWhenStorePackageMissing 复现
// “数据能识别但加载不成功”：Preferences 有商城条目（location=INTERNAL）但
// Chrome 商城包缺失/未完成安装。修复前会无条件剔除 CLI，扩展从此没有任何
// 加载路径；修复后保留 CLI，让 BrowserStudio 管理包继续加载。
func TestDropStoreInstalledLoadExtensionArgsKeepsCLIWhenStorePackageMissing(t *testing.T) {
	root := t.TempDir()
	userData := filepath.Join(root, "user")
	writeWebStorePrefs(t, userData) // 商城行存在，但 Default/Extensions/<id>/ 无包
	storePkg := filepath.Join(root, "ext", testStoreExtID)
	writeTestPackage(t, storePkg, "MetaMask", "12.0.0")

	args := []string{"--load-extension=" + storePkg}
	got := dropStoreInstalledLoadExtensionArgs(args, userData)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, testStoreExtID) {
		t.Fatalf("CLI entry must be KEPT when Chrome store package is missing: %s", joined)
	}
}

// TestDropStoreInstalledLoadExtensionArgsKeepsCLIWhenStoreEntryDisabled 同样
// 防止误剔除：商城条目存在但被禁用（state=0）时 Chrome 不会原生加载它，
// CLI 必须保留。
func TestDropStoreInstalledLoadExtensionArgsKeepsCLIWhenStoreEntryDisabled(t *testing.T) {
	root := t.TempDir()
	userData := filepath.Join(root, "user")
	// 商城行：state=0（禁用）+ location=INTERNAL
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				testStoreExtID: map[string]any{
					"state":         float64(0),
					"location":      float64(chromeExtLocationInternal),
					"from_webstore": true,
					"install_time":  "13300000000000000",
				},
			},
		},
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Preferences"), data, 0644); err != nil {
		t.Fatal(err)
	}
	// 即使商城包存在，禁用状态也必须保留 CLI。
	writeChromeStorePackage(t, userData, testStoreExtID, "12.0.0")
	storePkg := filepath.Join(root, "ext", testStoreExtID)
	writeTestPackage(t, storePkg, "MetaMask", "12.0.0")

	args := []string{"--load-extension=" + storePkg}
	got := dropStoreInstalledLoadExtensionArgs(args, userData)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, testStoreExtID) {
		t.Fatalf("CLI entry must be KEPT when store entry is disabled: %s", joined)
	}
}
