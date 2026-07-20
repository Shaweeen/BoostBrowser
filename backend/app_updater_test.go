package backend

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeUpdateSHA256(t *testing.T) {
	valid := strings.Repeat("a", 64)
	got, err := normalizeUpdateSHA256(strings.ToUpper(valid))
	if err != nil || got != valid {
		t.Fatalf("expected normalized hash, got %q err=%v", got, err)
	}
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("z", 64)} {
		if _, err := normalizeUpdateSHA256(value); err == nil {
			t.Fatalf("expected invalid SHA256 to be rejected: %q", value)
		}
	}
}

func TestValidateUpdateAssetURL(t *testing.T) {
	valid := "https://github.com/Shaweeen/BoostBrowser/releases/download/v1.7.18/boost-browser.exe"
	if _, err := validateUpdateAssetURL(valid, "boost-browser.exe"); err != nil {
		t.Fatalf("trusted release URL rejected: %v", err)
	}
	invalid := []string{
		"http://github.com/Shaweeen/BoostBrowser/releases/download/v1.7.18/boost-browser.exe",
		"https://example.com/Shaweeen/BoostBrowser/releases/download/v1.7.18/boost-browser.exe",
		"https://github.com/Shaweeen/BrowserStudio/releases/download/v1.7.18/boost-browser.exe",
		"https://github.com/Shaweeen/BoostBrowser/releases/download/v1.7.18/updater.exe",
		"https://github.com/Shaweeen/BoostBrowser/releases/download/v1.7.18/boost-browser.exe?raw=1",
	}
	for _, value := range invalid {
		if _, err := validateUpdateAssetURL(value, "boost-browser.exe"); err == nil {
			t.Fatalf("untrusted release URL accepted: %s", value)
		}
	}
}

func TestIsWindowsPEFile(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.exe")
	invalid := filepath.Join(dir, "invalid.exe")
	if err := os.WriteFile(valid, []byte{'M', 'Z', 0, 0}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte("not-pe"), 0600); err != nil {
		t.Fatal(err)
	}
	if !isWindowsPEFile(valid) || isWindowsPEFile(invalid) || isWindowsPEFile(filepath.Join(dir, "missing.exe")) {
		t.Fatal("PE header validation returned an unexpected result")
	}
}

func TestFetchLatestReleaseFallsBackToGithubLatestRedirectWhenAPIRateLimited(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "API rate limit exceeded", http.StatusForbidden)
	})
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Shaweeen/BoostBrowser/releases/tag/v9.9.9", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	rel, err := fetchLatestReleaseWithFallback(client, srv.URL+"/api/releases/latest", srv.URL+"/releases/latest")
	if err != nil {
		t.Fatalf("fetchLatestReleaseWithFallback returned error: %v", err)
	}
	if rel.TagName != "v9.9.9" {
		t.Fatalf("expected fallback tag v9.9.9, got %q", rel.TagName)
	}

	var exeURL, shaURL string
	for _, asset := range rel.Assets {
		switch asset.Name {
		case "boost-browser.exe":
			exeURL = asset.BrowserDownloadURL
		case "boost-browser.exe.sha256":
			shaURL = asset.BrowserDownloadURL
		}
	}
	if !strings.Contains(exeURL, "/releases/download/v9.9.9/boost-browser.exe") {
		t.Fatalf("fallback exe asset URL not constructed from tag: %q", exeURL)
	}
	if !strings.Contains(shaURL, "/releases/download/v9.9.9/boost-browser.exe.sha256") {
		t.Fatalf("fallback sha asset URL not constructed from tag: %q", shaURL)
	}
}
