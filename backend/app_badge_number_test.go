package backend

import (
	"boost-browser/backend/internal/browser"
	"testing"
)

func TestResolveBadgeDisplayNumberUsesNameNumber(t *testing.T) {
	profiles := map[string]*browser.Profile{
		"profile-a": {ProfileId: "profile-a", ProfileName: "实例-3"},
	}
	if got := resolveBadgeDisplayNumber("profile-a", "实例-3", profiles); got != 3 {
		t.Fatalf("expected explicit name number 3, got %d", got)
	}
}

func TestResolveBadgeDisplayNumberUsesStableProfileOrder(t *testing.T) {
	profiles := map[string]*browser.Profile{
		"profile-c": {ProfileId: "profile-c", ProfileName: "Gamma"},
		"profile-a": {ProfileId: "profile-a", ProfileName: "Alpha"},
		"profile-b": {ProfileId: "profile-b", ProfileName: "Beta"},
	}
	if got := resolveBadgeDisplayNumber("profile-b", "Beta", profiles); got != 2 {
		t.Fatalf("expected stable sorted number 2, got %d", got)
	}
}
