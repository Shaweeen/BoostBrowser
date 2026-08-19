//go:build windows

package backend

import (
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const startupWalletHomepageDismissWindow = 2500 * time.Millisecond

// dismissStartupWalletNotificationHosts WM_CLOSEs auto-opened wallet
// Notification hosts for one environment process tree. Bounded, async, no
// CDP, no Preferences/LES/Cookie writes. User-initiated Connect after this
// window still opens a new Notification host.
func dismissStartupWalletNotificationHosts(rootPID int) {
	if rootPID <= 0 {
		return
	}
	deadline := time.Now().Add(startupWalletHomepageDismissWindow)
	for {
		_ = closeStartupWalletNotificationHostsOnce(rootPID)
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func closeStartupWalletNotificationHostsOnce(rootPID int) int {
	if rootPID <= 0 {
		return 0
	}
	pids := map[int]bool{rootPID: true}
	if children := snapshotProcessChildren(); children != nil {
		queue := append([]int(nil), children[rootPID]...)
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			if pids[pid] {
				continue
			}
			pids[pid] = true
			queue = append(queue, children[pid]...)
		}
	}
	search := &startupWalletNotifySearch{pids: pids}
	procEnumWindows.Call(startupWalletNotifyEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)
	return search.closed
}

type startupWalletNotifySearch struct {
	pids   map[int]bool
	closed int
}

var startupWalletNotifyEnumCallback = windows.NewCallback(func(hwnd windows.HWND, lparam uintptr) uintptr {
	search := (*startupWalletNotifySearch)(unsafe.Pointer(lparam))
	var pid uint32
	procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
	if !search.pids[int(pid)] {
		return 1
	}
	if vis, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); vis == 0 {
		return 1
	}
	title := getWindowTitle(hwnd)
	if !isAutoOpenedWalletHomepageTitle(title) {
		return 1
	}
	className := getWindowClassName(hwnd)
	lowerClass := strings.ToLower(className)
	if !strings.HasPrefix(lowerClass, "chrome_widgetwin_") && !strings.EqualFold(className, "Chrome_MainWindow") {
		return 1
	}
	if posted, _, _ := procPostMessageW.Call(uintptr(hwnd), WM_CLOSE, 0, 0); posted != 0 {
		search.closed++
	}
	return 1
})
