//go:build !windows

package backend

func enforceBrowserWindowBounds(pid, width, height int) {}

func enforceMainEnvironmentWindowOnStart(pid int) {}
