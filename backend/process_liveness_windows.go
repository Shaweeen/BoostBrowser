//go:build windows

package backend

import (
	"errors"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

// isProcessAliveWindows uses the native process API instead of spawning
// tasklist. Runtime snapshot reconciliation calls this frequently, so keeping
// it allocation-free avoids a large process-creation penalty with many
// browser environments.
func isProcessAliveWindows(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}

	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(pid),
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		// Access denied still means Windows found a live process. This can
		// happen for protected processes owned by another privilege level.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, err
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false, err
	}
	return exitCode == windowsStillActive, nil
}
