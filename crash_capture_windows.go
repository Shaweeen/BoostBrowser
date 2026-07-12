//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sync"

	"boost-browser/backend"

	"golang.org/x/sys/windows"
)

var (
	crashLogOnce sync.Once
	crashLogFile *os.File
)

// installCrashLogCapture preserves Go runtime fatal/panic output for packaged
// GUI builds, which otherwise have no console and only expose exit_code=2.
func installCrashLogCapture(root string) {
	crashLogOnce.Do(func() {
		path := backend.ResolveRuntimePath(root, filepath.Join("logs", "main-crash.log"))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		crashLogFile = file
		os.Stderr = file
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(file.Fd()))
	})
}
