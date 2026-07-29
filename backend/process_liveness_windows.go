//go:build windows

package backend

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

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

func isProcessAlivePID(pid int) (bool, error) {
	return isProcessAliveWindows(pid)
}

type softCloseProcessWindowsSearch struct {
	rootPID int
	roots   map[int]int
	count   int
}

var softCloseProcessWindowsCallback = windows.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
	defer func() { _ = recover() }()
	search := (*softCloseProcessWindowsSearch)(unsafe.Pointer(lParam))
	if search == nil || !isWindow(hwnd) {
		return 1
	}
	pid := int(windowPID(hwnd))
	if pid <= 0 || search.roots[pid] != search.rootPID {
		return 1
	}
	className := strings.TrimSpace(getWindowClassName(hwnd))
	lowerClass := strings.ToLower(className)
	if !strings.HasPrefix(lowerClass, "chrome_widgetwin_") && !strings.EqualFold(className, "Chrome_MainWindow") {
		return 1
	}
	if isAuxiliaryIMEWindowTitleOrClass(getWindowTitle(hwnd), className) {
		return 1
	}
	if posted, _, _ := procPostMessageW.Call(uintptr(hwnd), WM_CLOSE, 0, 0); posted != 0 {
		search.count++
	}
	return 1
})

// requestSoftProcessStopPID asks every top-level Chromium window in one
// environment process tree to close through WM_CLOSE. It never calls taskkill
// or TerminateProcess; the shared close barrier separately verifies that the
// process, debug endpoint and profile lock have all disappeared.
func requestSoftProcessStopPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("浏览器进程 ID 不可用")
	}
	alive, err := isProcessAliveWindows(pid)
	if err == nil && !alive {
		return nil
	}
	roots := mapProcessTreeRoots([]int{pid})
	search := &softCloseProcessWindowsSearch{rootPID: pid, roots: roots}
	procEnumWindows.Call(softCloseProcessWindowsCallback, uintptr(unsafe.Pointer(search)))
	if search.count == 0 {
		return fmt.Errorf("未找到可正常关闭的浏览器窗口")
	}
	return nil
}
