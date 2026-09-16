//go:build windows

package backend

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var syncGetDPIForWindow = user32dll.NewProc("GetDpiForWindow")
var syncSendMessageTimeout = user32dll.NewProc("SendMessageTimeoutW")

func syncWindowDPI(hwnd windows.HWND) int {
	if syncGetDPIForWindow.Find() == nil {
		if dpi, _, _ := syncGetDPIForWindow.Call(uintptr(hwnd)); dpi != 0 {
			return int(dpi)
		}
	}
	return 96
}

func mapChromeToolbarInput(screenX, screenY int, master, follower windows.HWND) (uintptr, bool) {
	// The normal page mapper intentionally ignores render children smaller than
	// 50 px to avoid popup noise. Dense layouts can legitimately make a page
	// shorter than that, so toolbar calibration uses the class/top only.
	mr, fr := findChromeToolbarRenderChild(master), findChromeToolbarRenderChild(follower)
	if mr == 0 || fr == 0 {
		return 0, false
	}
	ml, mt, _, _ := getWindowRect(mr)
	fl, ft, _, _ := getWindowRect(fr)
	_, masterTop := screenToClient(master, int(ml), int(mt))
	_, followerTop := screenToClient(follower, int(fl), int(ft))
	x, y := screenToClient(master, screenX, screenY)
	mw, _, mok := getClientSize(master)
	fw, _, fok := getClientSize(follower)
	if !mok || !fok {
		return 0, false
	}
	x, y, ok := mapChromeToolbarPoint(x, y, mw, fw, masterTop, followerTop, syncWindowDPI(master), syncWindowDPI(follower))
	return MAKELONG(uint16(int16(x)), uint16(int16(y))), ok
}

var chromeToolbarRenderChildEnumCallback = windows.NewCallback(func(child windows.HWND, lParam uintptr) uintptr {
	defer func() { _ = recover() }()
	found := (*windows.HWND)(unsafe.Pointer(lParam))
	if found == nil || !isWindowVisible(child) || getWindowClassName(child) != "Chrome_RenderWidgetHostHWND" {
		return 1
	}
	left, top, right, bottom := getWindowRect(child)
	if right <= left || bottom <= top {
		return 1
	}
	*found = child
	return 0
})

func findChromeToolbarRenderChild(hwnd windows.HWND) windows.HWND {
	var found windows.HWND
	procEnumChildWindows.Call(uintptr(hwnd), chromeToolbarRenderChildEnumCallback, uintptr(unsafe.Pointer(&found)))
	runtime.KeepAlive(&found)
	return found
}

// Native frame buttons must not receive replayed toolbar clicks. A bounded
// WM_NCHITTEST asks Chrome itself, so this does not guess caption dimensions.
// Failure/timeout rejects the click instead of interpreting it as HTCLIENT.
func chromeClientMousePoint(hwnd windows.HWND, lparam uintptr) bool {
	pt := struct{ X, Y int32 }{int32(int16(lparam & 0xffff)), int32(int16((lparam >> 16) & 0xffff))}
	if ok, _, _ := procClientToScreen.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return false
	}
	if pt.X < -32768 || pt.X > 32767 || pt.Y < -32768 || pt.Y > 32767 {
		return false
	}
	var hit uintptr
	ok, _, _ := syncSendMessageTimeout.Call(uintptr(hwnd), 0x0084, 0,
		MAKELONG(uint16(int16(pt.X)), uint16(int16(pt.Y))), 2, 20, uintptr(unsafe.Pointer(&hit)))
	return ok != 0 && hit == 1 // WM_NCHITTEST, SMTO_ABORTIFHUNG, HTCLIENT
}
