package backend

import "testing"

func TestSyncCollectionReplacesWholeGeneration(t *testing.T) {
	var c syncCollection
	first := []SyncProfileInfo{{ProfileId: "a", Pid: 1, Hwnd: 11}, {ProfileId: "b", Pid: 2, Hwnd: 22}}
	if got := c.replace(first); got != 1 {
		t.Fatalf("generation=%d", got)
	}
	first[0].Hwnd = 0
	_, saved := c.snapshot()
	if len(saved) != 2 || saved[0].Hwnd != 11 {
		t.Fatalf("snapshot aliased caller: %+v", saved)
	}
	if got := c.replace([]SyncProfileInfo{{ProfileId: "c", Pid: 3, Hwnd: 33}}); got != 2 {
		t.Fatalf("generation=%d", got)
	}
	_, saved = c.snapshot()
	if len(saved) != 1 || saved[0].ProfileId != "c" {
		t.Fatalf("batches merged: %+v", saved)
	}
}

func TestSyncCollectionTokenFromArgs(t *testing.T) {
	if got := syncCollectionTokenFromArgs([]string{"--sync-panel", "--sync-collection-token=abc123"}); got != "abc123" {
		t.Fatalf("token=%q", got)
	}
}
