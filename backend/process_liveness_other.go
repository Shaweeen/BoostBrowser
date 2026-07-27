//go:build !windows

package backend

// The cross-platform caller only reaches this function on Windows. Keeping a
// stub allows non-Windows builds and tests to type-check the shared code.
func isProcessAliveWindows(pid int) (bool, error) {
	return false, nil
}
