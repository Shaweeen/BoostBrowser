//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var boostBrowserSingleInstanceMutex windows.Handle
var boostBrowserSyncPanelMutex windows.Handle

var (
	user32SingleInstance           = windows.NewLazySystemDLL("user32.dll")
	procEnumWindowsSingleInstance  = user32SingleInstance.NewProc("EnumWindows")
	procGetWindowTextLengthW       = user32SingleInstance.NewProc("GetWindowTextLengthW")
	procGetWindowTextW             = user32SingleInstance.NewProc("GetWindowTextW")
	procIsWindowVisibleSingle      = user32SingleInstance.NewProc("IsWindowVisible")
	procShowWindow                 = user32SingleInstance.NewProc("ShowWindow")
	procSetForegroundWindow        = user32SingleInstance.NewProc("SetForegroundWindow")
	procBringWindowToTop           = user32SingleInstance.NewProc("BringWindowToTop")
	procGetWindowThreadProcessIDSI = user32SingleInstance.NewProc("GetWindowThreadProcessId")
)

const (
	swRestore = 9
	swShow    = 5
)

type existingWindowSearch struct {
	currentPID uint32
	keywords   []string
	target     windows.Handle
}

// Windows callbacks are process-global resources. Reuse one callback instead
// of allocating a new callback every time the main window or sync panel is
// focused; repeated open/close cycles would otherwise exhaust the callback
// table and terminate the client with a fatal runtime error.
var existingWindowEnumCallback = windows.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
	if hwnd == 0 || lparam == 0 {
		return 1
	}
	search := (*existingWindowSearch)(unsafe.Pointer(lparam))
	var pid uint32
	procGetWindowThreadProcessIDSI.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || pid == search.currentPID {
		return 1
	}

	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return 1
	}
	buf := make([]uint16, length+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), length+1)
	title := strings.TrimSpace(windows.UTF16ToString(buf))
	if title == "" {
		return 1
	}
	lowerTitle := strings.ToLower(title)
	for _, keyword := range search.keywords {
		if strings.Contains(lowerTitle, strings.ToLower(keyword)) {
			search.target = windows.Handle(hwnd)
			return 0
		}
	}
	return 1
})

func acquireNamedMutex(name string, target *windows.Handle) (bool, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}

	h, err := windows.CreateMutex(nil, false, namePtr)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			if h != 0 {
				windows.CloseHandle(h)
			}
			*target = 0
			return false, nil
		}
		return false, fmt.Errorf("create named mutex: %w", err)
	}
	*target = h

	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		*target = 0
		return false, nil
	}
	return true, nil
}

func releaseNamedMutex(target *windows.Handle) {
	if *target != 0 {
		windows.CloseHandle(*target)
		*target = 0
	}
}

func acquireSingleInstanceLock() (bool, error) {
	return acquireNamedMutex(`Local\BoostBrowser_SingleInstance_Mutex_v1`, &boostBrowserSingleInstanceMutex)
}

func releaseSingleInstanceLock() {
	releaseNamedMutex(&boostBrowserSingleInstanceMutex)
}

func acquireSyncPanelLock() (bool, error) {
	return acquireNamedMutex(`Local\BoostBrowser_WindowSyncPanel_Mutex_v1`, &boostBrowserSyncPanelMutex)
}

func releaseSyncPanelLock() {
	releaseNamedMutex(&boostBrowserSyncPanelMutex)
}

// focusExistingMainWindow is only used when a second boost-browser.exe is launched.
// It activates the already-running Wails main window. Browser profile windows are not limited.
func focusExistingMainWindow() bool {
	return focusExistingWindowByKeywords([]string{"BrowserStudio", "Boost Browser", "Ant Browser", "boost-browser"})
}

func focusExistingSyncPanelWindow() bool {
	return focusExistingWindowByKeywords([]string{"BrowserStudio · 同步工具", "BrowserStudio · 窗口同步", "Boost Browser · 同步工具", "窗口同步", "同步工具"})
}

func focusExistingWindowByKeywords(keywords []string) bool {
	search := &existingWindowSearch{
		currentPID: uint32(windows.GetCurrentProcessId()),
		keywords:   append([]string(nil), keywords...),
	}
	procEnumWindowsSingleInstance.Call(existingWindowEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)
	if search.target == 0 {
		return false
	}

	visible, _, _ := procIsWindowVisibleSingle.Call(uintptr(search.target))
	if visible == 0 {
		procShowWindow.Call(uintptr(search.target), swShow)
	}
	procShowWindow.Call(uintptr(search.target), swRestore)
	procBringWindowToTop.Call(uintptr(search.target))
	procSetForegroundWindow.Call(uintptr(search.target))
	return true
}
