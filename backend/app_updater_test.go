package backend

import (
	"context"
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

func TestPrepareApplyUpdateQuitUnblocksCloseFlow(t *testing.T) {
	app := NewApp(t.TempDir())
	app.prepareApplyUpdateQuit()

	if !app.forceQuit {
		t.Fatal("apply-update 必须设置 forceQuit，否则 Windows OnBeforeClose 会拦截 runtime.Quit")
	}
	if app.quitMode != quitModeFull {
		t.Fatalf("apply-update 应使用 quitModeFull，got %v", app.quitMode)
	}
	// 设置 forceQuit 后，关闭流程必须放行：updater 在等待主进程退出替换 exe，
	// 若被关闭确认框拦截，主程序不退出，updater 只能等 30s 强杀，升级卡住。
	if ShouldBlockClose(app, context.Background()) {
		t.Fatal("apply-update 后 ShouldBlockClose 必须放行")
	}
}

func TestFetchLatestReleaseUsesQuotaFreeRedirectBeforeAPI(t *testing.T) {
	mux := http.NewServeMux()
	apiCalls := 0
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
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
	if apiCalls != 0 {
		t.Fatalf("quota-free redirect succeeded but API was called %d times", apiCalls)
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

func TestFetchLatestReleaseFallsBackToAPIWhenRedirectUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusBadGateway)
	})
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v8.8.8","assets":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rel, err := fetchLatestReleaseWithFallback(&http.Client{Timeout: 2 * time.Second}, srv.URL+"/api/releases/latest", srv.URL+"/releases/latest")
	if err != nil || rel.TagName != "v8.8.8" {
		t.Fatalf("expected API fallback v8.8.8, got rel=%+v err=%v", rel, err)
	}
}
