package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"strings"
	"testing"
)

func TestBatchCreateRejectsExistingNameBeforeCreatingAnyProfile(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.Profiles["existing"] = &BrowserProfile{ProfileId: "existing", ProfileName: "实例-2"}

	created, err := app.BrowserProfileBatchCreate("实例", 1, 3, BrowserProfileInput{})
	if err == nil || !strings.Contains(err.Error(), "实例-2") {
		t.Fatalf("expected a visible name conflict for 实例-2, got %v", err)
	}
	if len(created) != 0 || len(app.browserMgr.Profiles) != 1 {
		t.Fatalf("conflicting batch must not create partial profiles: created=%d profiles=%d", len(created), len(app.browserMgr.Profiles))
	}
}

func TestDeletedProfileNameCanBeReused(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.Profiles["deleted"] = &BrowserProfile{ProfileId: "deleted", ProfileName: "实例-1"}
	delete(app.browserMgr.Profiles, "deleted")

	created, err := app.BrowserProfileBatchCreate("实例", 1, 1, BrowserProfileInput{})
	if err != nil {
		t.Fatalf("deleted name should be reusable: %v", err)
	}
	if len(created) != 1 || created[0].ProfileName != "实例-1" {
		t.Fatalf("expected 实例-1 to be reused, got %#v", created)
	}
}

func TestSingleProfileCreateRejectsExistingName(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.Profiles["existing"] = &BrowserProfile{ProfileId: "existing", ProfileName: "实例-8"}

	_, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: " 实例-8 "})
	if err == nil || !strings.Contains(err.Error(), "环境名称已存在") {
		t.Fatalf("expected duplicate single-create rejection, got %v", err)
	}
}
