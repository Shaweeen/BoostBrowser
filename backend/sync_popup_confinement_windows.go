//go:build windows

package backend

import (
	"runtime"
	"strings"
	"time"
	"unsafe"

	"boost-browser/backend/internal/logger"

	"golang.org/x/sys/windows"
)

const (
	syncPopupBoundsInterval = 40 * time.Millisecond
	syncPopupBoundsInset    = 2
)

type syncPopupOwnerWindow struct {
	hwnd windows.HWND
	pid  uint32
	rect winRect
}

type syncPopupBoundsSearch struct {
	owners []syncPopupOwnerWindow
}

var syncPopupBoundsEnumCallback = windows.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
	defer func() { _ = recover() }()
	search := (*syncPopupBoundsSearch)(unsafe.Pointer(lParam))
	if search == nil || !isWindowVisible(hwnd) {
		return 1
	}

	for _, owner := range search.owners {
		if hwnd == owner.hwnd {
			return 1
		}
	}

	className := strings.TrimSpace(getWindowClassName(hwnd))
	lowerClass := strings.ToLower(className)
	if !strings.HasPrefix(lowerClass, "chrome_widgetwin_") && !strings.EqualFold(className, "Chrome_MainWindow") {
		return 1
	}
	title := strings.TrimSpace(getWindowTitle(hwnd))
	if isAuxiliaryIMEWindowTitleOrClass(title, className) || looksLikeServiceWorkerDevToolsTitle(strings.ToLower(title)) {
		return 1
	}

	popupRect, ok := getTopLevelWindowRect(hwnd)
	if !ok {
		return 1
	}
	owner, ownerLinked, ok := findSyncPopupOwner(hwnd, search.owners)
	if !ok || !isSyncPopupSurfaceCandidate(title, popupRect, owner.rect, ownerLinked) {
		return 1
	}

	x, y, width, height, shouldMove := constrainSyncPopupRect(popupRect, owner.rect, syncPopupBoundsInset)
	if !shouldMove {
		return 1
	}
	procSetWindowPos.Call(
		uintptr(hwnd),
		0,
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		SWP_NOZORDER|SWP_NOACTIVATE,
	)
	return 1
})

func (s *InputSyncer) syncPopupBoundsLoop(stop <-chan struct{}) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.New("InputSyncer").Error("sync popup bounds loop panic recovered", logger.F("error", recovered))
		}
	}()
	ticker := time.NewTicker(syncPopupBoundsInterval)
	defer ticker.Stop()

	// Run once immediately. Toolbar menus can be opened before the first timer
	// tick when the user starts sync with the master already focused.
	s.constrainSyncPopupSurfaces()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.constrainSyncPopupSurfaces()
		}
	}
}

func (s *InputSyncer) constrainSyncPopupSurfaces() {
	if s == nil || !s.IsActive() {
		return
	}
	mainWindows := append([]windows.HWND{s.masterHwnd}, s.getFollowerSnapshot()...)
	owners := make([]syncPopupOwnerWindow, 0, len(mainWindows))
	seen := make(map[windows.HWND]struct{}, len(mainWindows))
	for _, hwnd := range mainWindows {
		if hwnd == 0 || !isWindow(hwnd) {
			continue
		}
		if _, duplicate := seen[hwnd]; duplicate {
			continue
		}
		seen[hwnd] = struct{}{}
		rect, ok := getTopLevelWindowRect(hwnd)
		if !ok || rect.Right <= rect.Left || rect.Bottom <= rect.Top {
			continue
		}
		owners = append(owners, syncPopupOwnerWindow{hwnd: hwnd, pid: windowPID(hwnd), rect: rect})
	}
	if len(owners) == 0 {
		return
	}
	search := &syncPopupBoundsSearch{owners: owners}
	procEnumWindows.Call(syncPopupBoundsEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)
}

func findSyncPopupOwner(hwnd windows.HWND, owners []syncPopupOwnerWindow) (syncPopupOwnerWindow, bool, bool) {
	// Native Chrome submenus are often owned by the previous popup rather than
	// directly by the browser frame. Walk the complete owner chain so second and
	// third level menus remain attached to the correct tiled environment.
	current := hwnd
	for depth := 0; depth < 12 && current != 0; depth++ {
		ownerHwnd, _, _ := procGetWindow.Call(uintptr(current), 4) // GW_OWNER
		if ownerHwnd == 0 || windows.HWND(ownerHwnd) == current {
			break
		}
		current = windows.HWND(ownerHwnd)
		for _, owner := range owners {
			if current == owner.hwnd {
				return owner, true, true
			}
		}
	}

	pid := windowPID(hwnd)
	if pid != 0 {
		for _, owner := range owners {
			if owner.pid == pid {
				return owner, false, true
			}
		}
	}
	return syncPopupOwnerWindow{}, false, false
}

func isSyncPopupSurfaceCandidate(title string, popupRect, ownerRect winRect, ownerLinked bool) bool {
	width := int(popupRect.Right - popupRect.Left)
	height := int(popupRect.Bottom - popupRect.Top)
	ownerWidth := int(ownerRect.Right - ownerRect.Left)
	ownerHeight := int(ownerRect.Bottom - ownerRect.Top)
	if width <= 8 || height <= 8 || ownerWidth <= 0 || ownerHeight <= 0 || width > 10000 || height > 10000 {
		return false
	}
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	if looksLikeServiceWorkerDevToolsTitle(lowerTitle) {
		return false
	}
	if ownerLinked {
		return true
	}
	if looksLikeMainBrowserWindowTitle(lowerTitle) && !isStrongExtensionPopupTitle(lowerTitle) && !isKnownWalletPopupProductTitle(lowerTitle) {
		return false
	}
	// Empty-title Aura widgets cover Chrome menus, comboboxes and nested menu
	// surfaces. Titled prompt/notification windows are also valid sync popups.
	return lowerTitle == "" || isStrongExtensionPopupTitle(lowerTitle) || isKnownWalletPopupProductTitle(lowerTitle) || width < ownerWidth || height < ownerHeight
}

func constrainSyncPopupRect(popup, owner winRect, inset int) (x, y, width, height int, changed bool) {
	if inset < 0 {
		inset = 0
	}
	left := int(owner.Left) + inset
	top := int(owner.Top) + inset
	right := int(owner.Right) - inset
	bottom := int(owner.Bottom) - inset
	availableWidth := right - left
	availableHeight := bottom - top
	if availableWidth <= 0 || availableHeight <= 0 {
		return int(popup.Left), int(popup.Top), int(popup.Right - popup.Left), int(popup.Bottom - popup.Top), false
	}

	width = int(popup.Right - popup.Left)
	height = int(popup.Bottom - popup.Top)
	if width <= 0 || height <= 0 {
		return int(popup.Left), int(popup.Top), width, height, false
	}
	if width > availableWidth {
		width = availableWidth
	}
	if height > availableHeight {
		height = availableHeight
	}
	x = int(popup.Left)
	y = int(popup.Top)
	if x < left {
		x = left
	}
	if y < top {
		y = top
	}
	if x+width > right {
		x = right - width
	}
	if y+height > bottom {
		y = bottom - height
	}
	changed = x != int(popup.Left) || y != int(popup.Top) || width != int(popup.Right-popup.Left) || height != int(popup.Bottom-popup.Top)
	return x, y, width, height, changed
}
