package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestWalletImportFileDialogUsesWindowsFallbackAfterWailsFailure(t *testing.T) {
	originalPrimary := showWalletImportWailsDialog
	originalFallback := showWalletImportFallbackDialog
	t.Cleanup(func() {
		showWalletImportWailsDialog = originalPrimary
		showWalletImportFallbackDialog = originalFallback
	})

	showWalletImportWailsDialog = func(context.Context, wailsruntime.OpenDialogOptions) (string, error) {
		return "", errors.New("COM unavailable")
	}
	showWalletImportFallbackDialog = func(title string) (string, error) {
		if !strings.Contains(title, "Rabby") {
			t.Fatalf("fallback title missing wallet name: %q", title)
		}
		return `C:\Users\test\wallets.csv`, nil
	}

	app := NewApp("")
	app.ctx = context.Background()
	path, err := app.selectWalletImportFile(walletImportSpecs["rabby"])
	if err != nil || path != `C:\Users\test\wallets.csv` {
		t.Fatalf("fallback result path=%q err=%v", path, err)
	}
}

func TestWalletImportFileDialogReportsBothFailures(t *testing.T) {
	originalPrimary := showWalletImportWailsDialog
	originalFallback := showWalletImportFallbackDialog
	t.Cleanup(func() {
		showWalletImportWailsDialog = originalPrimary
		showWalletImportFallbackDialog = originalFallback
	})

	showWalletImportWailsDialog = func(context.Context, wailsruntime.OpenDialogOptions) (string, error) {
		return "", errors.New("primary failed")
	}
	showWalletImportFallbackDialog = func(string) (string, error) {
		return "", errors.New("fallback failed")
	}

	app := NewApp("")
	app.ctx = context.Background()
	_, err := app.selectWalletImportFile(walletImportSpecs["rabby"])
	if err == nil || !strings.Contains(err.Error(), "primary failed") || !strings.Contains(err.Error(), "fallback failed") {
		t.Fatalf("expected both dialog failures, got %v", err)
	}
}

func TestWalletImportTemplateDialogUsesWindowsFallbackAfterWailsFailure(t *testing.T) {
	originalPrimary := showWalletTemplateWailsDialog
	originalFallback := showWalletTemplateFallbackDialog
	t.Cleanup(func() {
		showWalletTemplateWailsDialog = originalPrimary
		showWalletTemplateFallbackDialog = originalFallback
	})

	showWalletTemplateWailsDialog = func(context.Context, wailsruntime.SaveDialogOptions) (string, error) {
		return "", errors.New("COM unavailable")
	}
	showWalletTemplateFallbackDialog = func(title, defaultFilename string) (string, error) {
		if !strings.Contains(title, "Rabby") || defaultFilename != "rabby-wallet-import-template.csv" {
			t.Fatalf("unexpected fallback options title=%q filename=%q", title, defaultFilename)
		}
		return `C:\Users\test\rabby-wallet-import-template.csv`, nil
	}

	app := NewApp("")
	app.ctx = context.Background()
	path, err := app.selectWalletImportTemplatePath(walletImportSpecs["rabby"])
	if err != nil || path != `C:\Users\test\rabby-wallet-import-template.csv` {
		t.Fatalf("template fallback result path=%q err=%v", path, err)
	}
}

func rabbyTestMnemonic(prefix string, count int) string {
	words := make([]string, count)
	for i := range words {
		words[i] = prefix + string(rune('a'+i))
	}
	return strings.Join(words, " ")
}

func writeRabbyImportFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func rabbyTestProfiles() map[string]RabbyWalletImportPreviewRow {
	return map[string]RabbyWalletImportPreviewRow{
		"profile-1": {EnvironmentNumber: 1, ProfileID: "profile-1", ProfileName: "环境一"},
		"profile-2": {EnvironmentNumber: 2, ProfileID: "profile-2", ProfileName: "环境二", Running: true},
	}
}

func TestParseRabbyWalletImportCSV(t *testing.T) {
	mnemonic := rabbyTestMnemonic("alpha", 12)
	path := writeRabbyImportFixture(t, "wallets.csv", "\ufeffprofile_id,profile_name,mnemonic\nprofile-1,环境一,\""+mnemonic+"\"\n")

	rows, preview, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(rows) != 1 || rows[0].ProfileID != "profile-1" || rows[0].Mnemonic != mnemonic {
		t.Fatalf("unexpected secret rows: %#v", rows)
	}
	if len(preview) != 1 || preview[0].ProfileName != "环境一" || preview[0].WordCount != 12 || preview[0].Running {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal preview: %v", err)
	}
	if bytes.Contains(encoded, []byte("alphaa")) {
		t.Fatal("frontend preview exposed mnemonic material")
	}
}

func TestParseWalletImportCSVByEnvironmentNumber(t *testing.T) {
	mnemonic := rabbyTestMnemonic("number", 12)
	path := writeRabbyImportFixture(t, "wallets-by-number.csv", "environment_number,mnemonic\n2,"+mnemonic+"\n")

	rows, preview, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err != nil {
		t.Fatalf("parse environment number CSV: %v", err)
	}
	if len(rows) != 1 || rows[0].ProfileID != "profile-2" || len(preview) != 1 || preview[0].EnvironmentNumber != 2 || preview[0].ProfileName != "环境二" {
		t.Fatalf("unexpected number mapping rows=%#v preview=%#v", rows, preview)
	}
}

func TestParseWalletImportCSVByProfileName(t *testing.T) {
	mnemonic := rabbyTestMnemonic("name", 12)
	path := writeRabbyImportFixture(t, "wallets-by-name.csv", "profile_name,mnemonic\n环境一,"+mnemonic+"\n")

	rows, preview, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err != nil || len(rows) != 1 || rows[0].ProfileID != "profile-1" || len(preview) != 1 {
		t.Fatalf("expected exact name mapping rows=%#v preview=%#v err=%v", rows, preview, err)
	}
}

func TestParseWalletImportLegacyProfileIDColumnAcceptsVisibleNumber(t *testing.T) {
	mnemonic := rabbyTestMnemonic("legacy", 12)
	path := writeRabbyImportFixture(t, "wallets-legacy-number.csv", "profile_id,mnemonic\n#1,"+mnemonic+"\n")

	rows, _, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err != nil || len(rows) != 1 || rows[0].ProfileID != "profile-1" {
		t.Fatalf("legacy visible number did not resolve rows=%#v err=%v", rows, err)
	}
}

func TestParseWalletImportRejectsConflictingSelectors(t *testing.T) {
	mnemonic := rabbyTestMnemonic("conflict", 12)
	path := writeRabbyImportFixture(t, "wallets-conflict.csv", "environment_number,profile_id,mnemonic\n2,profile-1,"+mnemonic+"\n")

	_, _, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err == nil || !strings.Contains(err.Error(), "指向不同环境") {
		t.Fatalf("expected conflicting selector error, got %v", err)
	}
}

func TestParseWalletImportRejectsAmbiguousEnvironmentNumber(t *testing.T) {
	profiles := rabbyTestProfiles()
	profiles["profile-3"] = RabbyWalletImportPreviewRow{EnvironmentNumber: 2, ProfileID: "profile-3", ProfileName: "另一个环境二"}
	mnemonic := rabbyTestMnemonic("ambiguous", 12)
	path := writeRabbyImportFixture(t, "wallets-ambiguous.csv", "environment_number,mnemonic\n2,"+mnemonic+"\n")

	_, _, err := parseRabbyWalletImportFile(path, profiles)
	if err == nil || !strings.Contains(err.Error(), "匹配到多个环境") {
		t.Fatalf("expected ambiguous number error, got %v", err)
	}
}

func TestParseWalletImportExactIDDisambiguatesRepeatedNumber(t *testing.T) {
	profiles := rabbyTestProfiles()
	profiles["profile-3"] = RabbyWalletImportPreviewRow{EnvironmentNumber: 2, ProfileID: "profile-3", ProfileName: "另一个环境二"}
	mnemonic := rabbyTestMnemonic("disambiguated", 12)
	path := writeRabbyImportFixture(t, "wallets-disambiguated.csv", "environment_number,profile_id,mnemonic\n2,profile-2,"+mnemonic+"\n")

	rows, _, err := parseRabbyWalletImportFile(path, profiles)
	if err != nil || len(rows) != 1 || rows[0].ProfileID != "profile-2" {
		t.Fatalf("exact profile ID should disambiguate repeated number rows=%#v err=%v", rows, err)
	}
}

func TestParseRabbyWalletImportTXT(t *testing.T) {
	mnemonic := rabbyTestMnemonic("beta", 15)
	path := writeRabbyImportFixture(t, "wallets.txt", "# profile_id<Tab>mnemonic\nprofile-2\t"+mnemonic+"\n")

	rows, preview, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err != nil {
		t.Fatalf("parse TXT: %v", err)
	}
	if len(rows) != 1 || len(preview) != 1 || !preview[0].Running || preview[0].WordCount != 15 {
		t.Fatalf("unexpected result: rows=%#v preview=%#v", rows, preview)
	}
}

func TestParseRabbyWalletImportRejectsDuplicateProfile(t *testing.T) {
	path := writeRabbyImportFixture(t, "wallets.csv", "profile_id,mnemonic\nprofile-1,"+rabbyTestMnemonic("alpha", 12)+"\nprofile-1,"+rabbyTestMnemonic("beta", 12)+"\n")

	_, _, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err == nil || !strings.Contains(err.Error(), "重复使用同一环境") {
		t.Fatalf("expected duplicate profile error, got %v", err)
	}
}

func TestParseRabbyWalletImportRejectsDuplicateMnemonic(t *testing.T) {
	mnemonic := rabbyTestMnemonic("gamma", 12)
	path := writeRabbyImportFixture(t, "wallets.csv", "profile_id,mnemonic\nprofile-1,"+mnemonic+"\nprofile-2,"+mnemonic+"\n")

	_, _, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err == nil || !strings.Contains(err.Error(), "重复助记词") {
		t.Fatalf("expected duplicate mnemonic error, got %v", err)
	}
}

func TestParseRabbyWalletImportRejectsUnsupportedWordCount(t *testing.T) {
	path := writeRabbyImportFixture(t, "wallets.txt", "profile-1|"+rabbyTestMnemonic("delta", 13)+"\n")

	_, _, err := parseRabbyWalletImportFile(path, rabbyTestProfiles())
	if err == nil || !strings.Contains(err.Error(), "仅支持 12/15/18/21/24 词") {
		t.Fatalf("expected word-count error, got %v", err)
	}
}

func TestParseJupiterWalletImportOnlyAccepts12Or24Words(t *testing.T) {
	invalidPath := writeRabbyImportFixture(t, "jupiter-invalid.txt", "profile-1|"+rabbyTestMnemonic("jupiter", 15)+"\n")
	_, _, err := parseWalletImportFile(invalidPath, rabbyTestProfiles(), walletImportSpecs["jupiter"])
	if err == nil || !strings.Contains(err.Error(), "Jupiter 仅支持 12/24 词") {
		t.Fatalf("expected Jupiter word-count error, got %v", err)
	}

	validPath := writeRabbyImportFixture(t, "jupiter-valid.txt", "profile-1|"+rabbyTestMnemonic("jupiter", 24)+"\n")
	rows, preview, err := parseWalletImportFile(validPath, rabbyTestProfiles(), walletImportSpecs["jupiter"])
	if err != nil || len(rows) != 1 || len(preview) != 1 || preview[0].WordCount != 24 {
		t.Fatalf("expected valid 24-word Jupiter row, rows=%#v preview=%#v err=%v", rows, preview, err)
	}
}

func TestOfficialWalletExtensionRejectsArbitraryDownloadOrigin(t *testing.T) {
	app := NewApp(t.TempDir())
	spec := walletImportSpecs["metamask"]
	if err := os.MkdirAll(app.globalExtensionDir(spec.ExtensionID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.globalExtensionDir(spec.ExtensionID), "manifest.json"), []byte(`{"manifest_version":3,"name":"MetaMask","version":"1.0"}`), 0600); err != nil {
		t.Fatal(err)
	}

	evil := globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{{
		ExtensionID:     spec.ExtensionID,
		DownloadAddress: "https://example.invalid/" + spec.ExtensionID + "/wallet.crx",
	}}}
	if err := app.saveGlobalExtensionRegistry(evil); err != nil {
		t.Fatal(err)
	}
	if app.officialWalletExtensionInstalled(spec) {
		t.Fatal("arbitrary download origin was trusted as official MetaMask")
	}

	official := globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{{
		ExtensionID:     spec.ExtensionID,
		DownloadAddress: "https://chromewebstore.google.com/detail/metamask/" + spec.ExtensionID,
	}}}
	if err := app.saveGlobalExtensionRegistry(official); err != nil {
		t.Fatal(err)
	}
	if !app.officialWalletExtensionInstalled(spec) {
		t.Fatal("official Chrome Web Store MetaMask was rejected")
	}
}

func TestClearRabbySecretRows(t *testing.T) {
	rows := []rabbyWalletImportSecretRow{{Mnemonic: rabbyTestMnemonic("erase", 12)}}
	clearRabbySecretRows(rows)
	if rows[0].Mnemonic != "" {
		t.Fatal("mnemonic was not cleared")
	}
}

func TestCleanupExpiredRabbyImportClearsSecret(t *testing.T) {
	app := NewApp("")
	rows := []rabbyWalletImportSecretRow{{Mnemonic: rabbyTestMnemonic("expire", 12)}}
	app.rabbyImports["expired"] = &rabbyWalletImportSession{
		CreatedAt: time.Now().Add(-rabbyImportSessionTTL - time.Second),
		Rows:      rows,
	}
	app.rabbyImportMu.Lock()
	app.cleanupExpiredRabbyImportsLocked(time.Now())
	app.rabbyImportMu.Unlock()
	if _, exists := app.rabbyImports["expired"]; exists {
		t.Fatal("expired session was not removed")
	}
	if rows[0].Mnemonic != "" {
		t.Fatal("expired session mnemonic was not cleared")
	}
}

func TestBrowserStartBlockedDuringRabbyImport(t *testing.T) {
	app := NewApp("")
	app.rabbyImportActive["profile-1"] = true
	_, err := app.BrowserInstanceStart("profile-1")
	if err == nil || !strings.Contains(err.Error(), "正在执行钱包批量导入") {
		t.Fatalf("expected Rabby import lock error, got %v", err)
	}
}
