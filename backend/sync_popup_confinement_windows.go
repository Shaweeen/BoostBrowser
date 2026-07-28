//go:build windows

package backend

import (
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"boost-browser/backend/internal/logger"

	"golang.org/x/sys/windows"
)

const syncPopupBoundsInset = 2

func syncPopupBoundsIntervalForFollowers(followerCount int) time.Duration {
	switch {
	case followerCount >= 20:
		return 250 * time.Millisecond
	case followerCount >= 8:
		return 180 * time.Millisecond
	default:
		return 120 * time.Millisecond
	}
}

type syncPopupOwnerWindow struct {
	hwnd windows.HWND
	pid  uint32
	rect winRect
}

type syncPopupBoundsSearch struct {
	owners              []syncPopupOwnerWindow
	processOwners       map[int]int
	processOwnersLoaded bool
}

func (search *syncPopupBoundsSearch) findProcessTreeOwner(pid uint32) (syncPopupOwnerWindow, bool) {
	if search == nil || pid == 0 {
		return syncPopupOwnerWindow{}, false
	}
	if !search.processOwnersLoaded {
		rootPIDs := make([]int, 0, len(search.owners))
		for _, owner := range search.owners {
			if owner.pid != 0 {
				rootPIDs = append(rootPIDs, int(owner.pid))
			}
		}
		search.processOwners = mapProcessTreeRoots(rootPIDs)
		search.processOwnersLoaded = true
	}
	rootPID, ok := search.processOwners[int(pid)]
	if !ok {
		return syncPopupOwnerWindow{}, false
	}
	for _, owner := range search.owners {
		if int(owner.pid) == rootPID {
			return owner, true
		}
	}
	return syncPopupOwnerWindow{}, false
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
	lowerTitle := strings.ToLower(title)
	if !ok && isCompactExtensionPopupTitle(lowerTitle) {
		if processOwner, found := search.findProcessTreeOwner(windowPID(hwnd)); found {
			owner = processOwner
			ok = true
		}
	}
	if !ok || !isSyncPopupSurfaceCandidate(title, popupRect, owner.rect, ownerLinked) {
		return 1
	}

	// Chrome and the extension own the popup's natural content size. The sync
	// assistant only keeps that surface inside its current environment cell.
	// In particular, do not apply the browser startup template or a wallet-
	// specific width/height here: those fixed sizes create blank canvas after
	// the environment has been tiled, stacked, or arranged horizontally.
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
	ticker := time.NewTicker(syncPopupBoundsIntervalForFollowers(len(s.getFollowerSnapshot())))
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

func syncPopupConfinementEnabled(active, paused bool, layoutUpdating int32) bool {
	// `paused` intentionally does not disable geometry ownership. Pause controls
	// input delivery only; popup containment lasts for the active sync session.
	_ = paused
	return active && layoutUpdating == 0
}

func (s *InputSyncer) constrainSyncPopupSurfaces() {
	// Pausing input synchronization must not abandon popup geometry. Users can
	// still open wallet prompts while paused, and those surfaces must remain
	// inside the environment's current arranged rectangle. StopSync still ends
	// this loop and releases all synchronization-owned resources.
	if s == nil || !syncPopupConfinementEnabled(s.IsActive(), s.IsPaused(), atomic.LoadInt32(&s.layoutUpdating)) {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.popupUpdating, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.popupUpdating, 0)
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
	if looksLikeMainBrowserWindowTitle(lowerTitle) && !isDefinitiveExtensionPopupTitle(lowerTitle) {
		return false
	}
	// Empty-title Aura widgets cover Chrome menus, comboboxes and nested menu
	// surfaces. Titled prompt/notification windows are also valid sync popups.
	return lowerTitle == "" || isDefinitiveExtensionPopupTitle(lowerTitle) || width < ownerWidth || height < ownerHeight
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
