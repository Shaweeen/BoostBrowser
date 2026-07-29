//go:build !windows

package backend

import (
	"errors"
	"os"
	"syscall"
)

// The shared close barrier uses a signal-0 liveness check without modifying the
// process. EPERM means the process exists but is owned by another privilege.
func isProcessAlivePID(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

// Kept for the Windows-only stop helper referenced by shared source.
func isProcessAliveWindows(pid int) (bool, error) {
	return false, nil
}

func requestSoftProcessStopPID(pid int) error {
	if pid <= 0 {
		return errors.New("浏览器进程 ID 不可用")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}
