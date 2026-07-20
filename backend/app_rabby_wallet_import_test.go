package backend

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		"profile-1": {ProfileID: "profile-1", ProfileName: "环境一"},
		"profile-2": {ProfileID: "profile-2", ProfileName: "环境二", Running: true},
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
	if err == nil || !strings.Contains(err.Error(), "正在执行 Rabby 钱包导入") {
		t.Fatalf("expected Rabby import lock error, got %v", err)
	}
}
