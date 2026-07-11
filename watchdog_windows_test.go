//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchdogRestartLoopFuse(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	for i := 0; i < 3; i++ {
		if watchdogRestartLimitReached(root, now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("restart %d should still be allowed", i+1)
		}
	}
	if !watchdogRestartLimitReached(root, now.Add(4*time.Second)) {
		t.Fatal("fourth restart in one minute must trip the fuse")
	}

	path := filepath.Join(root, "data", ".watchdog-restarts")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("restart state was not persisted: %v", err)
	}
}

func TestWatchdogRestartLoopFuseExpires(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = watchdogRestartLimitReached(root, now.Add(time.Duration(i)*time.Second))
	}
	if watchdogRestartLimitReached(root, now.Add(2*time.Minute)) {
		t.Fatal("old restart attempts must expire")
	}
}
