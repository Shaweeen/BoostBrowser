package backend

import "testing"

func TestListAPIsReturnEmptyBeforeStartupCompletes(t *testing.T) {
	app := &App{}

	if profiles := app.BrowserProfileList(); profiles == nil || len(profiles) != 0 {
		t.Fatalf("profile list before startup = %#v, want non-nil empty list", profiles)
	}
	if cores := app.BrowserCoreList(); cores == nil || len(cores) != 0 {
		t.Fatalf("core list before startup = %#v, want non-nil empty list", cores)
	}
	if proxies := app.BrowserProxyList(); proxies == nil || len(proxies) != 0 {
		t.Fatalf("proxy list before startup = %#v, want non-nil empty list", proxies)
	}
}

func TestListAPIsReturnEmptyOnNilApp(t *testing.T) {
	var app *App

	if profiles := app.BrowserProfileList(); profiles == nil || len(profiles) != 0 {
		t.Fatalf("nil app profile list = %#v, want non-nil empty list", profiles)
	}
	if cores := app.BrowserCoreList(); cores == nil || len(cores) != 0 {
		t.Fatalf("nil app core list = %#v, want non-nil empty list", cores)
	}
	if proxies := app.BrowserProxyList(); proxies == nil || len(proxies) != 0 {
		t.Fatalf("nil app proxy list = %#v, want non-nil empty list", proxies)
	}
}
