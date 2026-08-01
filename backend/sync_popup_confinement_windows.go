//go:build windows

package backend

import (
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tight inset when nudging popup origin into the owner cell.
const syncPopupBoundsInset = 1

// Wallet/extension first paint is fragile under multi-open: SetWindowPos size
// thrash during open leaves Rabby/MetaMask Notification hosts permanently white
// (user must restart the environment). Skip force-fit resize for this grace,
// and never resize titled *Notification* hosts.
const popupForceFitGrace = 2 * time.Second

var (
	popupFirstSeenMu sync.Mutex
	popupFirstSeen   = map[windows.HWND]time.Time{}
)

func notePopupFirstSeen(hwnd windows.HWND) time.Time {
	if hwnd == 0 {
		return time.Time{}
	}
	now := time.Now()
	popupFirstSeenMu.Lock()
	defer popupFirstSeenMu.Unlock()
	if t, ok := popupFirstSeen[hwnd]; ok {
		return t
	}
	popupFirstSeen[hwnd] = now
	// Opportunistic prune of dead windows so the map cannot grow forever under
	// multi-batch open (200 envs × many short-lived extension shells).
	if len(popupFirstSeen) > 256 {
		for h, seen := range popupFirstSeen {
			if now.Sub(seen) > 2*time.Minute || !isWindow(h) {
				delete(popupFirstSeen, h)
			}
		}
	}
	return now
}

// isWalletNotificationHostTitle matches MV3 wallet notification windows that
// blank permanently when resized mid-load (Rabby Wallet Notification, etc.).
func isWalletNotificationHostTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "notification") ||
		strings.Contains(lower, "通知")
}

// popupAllowForceFitResize: true only after grace and never for notification hosts.
func popupAllowForceFitResize(hwnd windows.HWND, title string) bool {
	if isWalletNotificationHostTitle(title) {
		return false
	}
	first := notePopupFirstSeen(hwnd)
	if first.IsZero() {
		return false
	}
	return time.Since(first) >= popupForceFitGrace
}

func syncPopupBoundsIntervalForFollowers(followerCount int) time.Duration {
	// Slower ticks under multi-open: less SetWindowPos fighting Chromium paint.
	switch {
	case followerCount >= 20:
		return 900 * time.Millisecond
	case followerCount >= 8:
		return 650 * time.Millisecond
	default:
		return 450 * time.Millisecond
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
	placements          []syncPopupPlacement
}

type syncPopupPlacement struct {
	hwnd            windows.HWND
	owner           syncPopupOwnerWindow
	x               int
	y               int
	width           int
	height          int
	geometryChanged bool
	sizeChanged     bool // always false for wallets; keep SWP_NOSIZE to avoid reflow flicker
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
	processLinked := false
	if !ok {
		// MetaMask/Rabby notification hosts often live in a Chrome child process
		// whose Win32 owner is not the browser frame. Resolve via the process
		// tree so multi-environment wallets still map to the correct cell.
		if processOwner, found := search.findProcessTreeOwner(windowPID(hwnd)); found {
			owner = processOwner
			ok = true
			processLinked = true
		}
	} else if !ownerLinked {
		// Same-PID match without GW_OWNER is still a process-level link.
		processLinked = true
	}
	if !ok || !isSyncPopupSurfaceCandidate(title, popupRect, owner.rect, ownerLinked, processLinked) {
		return 1
	}

	// Placement policy:
	//  - always nudge origin into the owner cell;
	//  - force-fit size only after open grace, and never for *Notification* hosts
	//    (resize mid-paint → permanent white Rabby/MetaMask notification).
	allowResize := popupAllowForceFitResize(hwnd, title)
	x, y, width, height, shouldMove := constrainSyncPopupRectOptions(popupRect, owner.rect, syncPopupBoundsInset, allowResize)
	natW := int(popupRect.Right - popupRect.Left)
	natH := int(popupRect.Bottom - popupRect.Top)
	sizeChanged := allowResize && (width != natW || height != natH)
	// geometryChanged must include size for SetWindowPos flag selection when we
	// intentionally shrink; position-only moves keep sizeChanged false.
	search.placements = append(search.placements, syncPopupPlacement{
		hwnd:            hwnd,
		owner:           owner,
		x:               x,
		y:               y,
		width:           width,
		height:          height,
		geometryChanged: shouldMove,
		sizeChanged:     sizeChanged,
	})
	return 1
})

// constrainPopupSurfacesToOwners is the single popup geometry implementation used
// by the environment-wide confiner. Callers supply the current environment main
// windows (every running profile), not only the active sync master/followers.
func constrainPopupSurfacesToOwners(owners []syncPopupOwnerWindow) {
	if len(owners) == 0 {
		return
	}
	search := &syncPopupBoundsSearch{owners: owners}
	procEnumWindows.Call(syncPopupBoundsEnumCallback, uintptr(unsafe.Pointer(search)))
	search.applyPlacements()
	runtime.KeepAlive(search)
}

func syncPopupConfinementEnabled(active, paused bool, layoutUpdating int32) bool {
	// Legacy name kept for tests. Pause no longer gates geometry; multi-open
	// environments own confinement even without the sync assistant.
	_ = paused
	return active && layoutUpdating == 0
}

func findSyncPopupOwner(hwnd windows.HWND, owners []syncPopupOwnerWindow) (syncPopupOwnerWindow, bool, bool) {
	// Native Chrome submenus are often owned by the previous popup rather than
	// directly by the browser frame. Walk the complete owner chain so second and
	// third level menus remain attached to the correct tiled environment.
	current := hwnd
	for depth := 0; depth < 12 && current != 0; depth++ {
		ownerHwnd, _, _ := procGetWindow.Call(uintptr(current), GW_OWNER)
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

func syncPopupPlacementFlags(geometryChanged, zOrderChanged, sizeChanged bool) uintptr {
	flags := uintptr(SWP_NOACTIVATE)
	if !geometryChanged {
		flags |= SWP_NOMOVE
	}
	// Prefer NOSIZE unless we intentionally change dimensions. Wallet extensions
	// re-layout when resized, and continuous SetWindowPos size thrash flickers.
	if !sizeChanged {
		flags |= SWP_NOSIZE
	}
	if !zOrderChanged {
		flags |= SWP_NOZORDER
	}
	return flags
}

func windowIsTopmost(hwnd windows.HWND) bool {
	style, _, _ := procGetWindowLongW.Call(uintptr(hwnd), GWL_EXSTYLE)
	return style&WS_EX_TOPMOST != 0
}

func windowImmediatelyAbove(hwnd windows.HWND) windows.HWND {
	above, _, _ := procGetWindow.Call(uintptr(hwnd), GW_HWNDPREV)
	return windows.HWND(above)
}

func (search *syncPopupBoundsSearch) applyPlacements() {
	if search == nil || len(search.placements) == 0 {
		return
	}

	// Extension windows can be created in the desktop-wide topmost band. Demote
	// all candidates before calculating their normal-band order.
	for _, placement := range search.placements {
		if !windowIsTopmost(placement.hwnd) {
			continue
		}
		procSetWindowPos.Call(uintptr(placement.hwnd), HWND_NOTOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE)
	}

	ownerOrder := make([]windows.HWND, 0, len(search.owners))
	byOwner := make(map[windows.HWND][]syncPopupPlacement, len(search.owners))
	for _, placement := range search.placements {
		if _, exists := byOwner[placement.owner.hwnd]; !exists {
			ownerOrder = append(ownerOrder, placement.owner.hwnd)
		}
		byOwner[placement.owner.hwnd] = append(byOwner[placement.owner.hwnd], placement)
	}

	for _, ownerHwnd := range ownerOrder {
		group := byOwner[ownerHwnd]
		groupSet := make(map[windows.HWND]struct{}, len(group))
		for _, placement := range group {
			groupSet[placement.hwnd] = struct{}{}
		}

		// EnumWindows delivered the popup group from top to bottom. Locate the
		// nearest unrelated window above the owner, skipping this group's own
		// surfaces, then preserve that native top-to-bottom order as one block.
		// This keeps nested and sibling menus stable instead of making them swap
		// places on every confinement tick.
		boundary := windowImmediatelyAbove(ownerHwnd)
		for boundary != 0 {
			if _, belongsToGroup := groupSet[boundary]; !belongsToGroup {
				break
			}
			boundary = windowImmediatelyAbove(boundary)
		}

		expectedAbove := boundary
		for _, placement := range group {
			zOrderChanged := windowImmediatelyAbove(placement.hwnd) != expectedAbove
			// Skip pure z-order reshuffles. Under 20× batch multi-open, continuous
			// SetWindowPos z-order alone interrupts extension first paint and can
			// leave Rabby Notification permanently white. Geometry/size moves still
			// re-stack; Chromium keeps relative order for untouched surfaces.
			if !placement.geometryChanged && !placement.sizeChanged {
				expectedAbove = placement.hwnd
				continue
			}
			if placement.geometryChanged || zOrderChanged {
				insertAfter := HWND_TOP
				if expectedAbove != 0 {
					insertAfter = uintptr(expectedAbove)
				}
				w, h := placement.width, placement.height
				procSetWindowPos.Call(
					uintptr(placement.hwnd),
					insertAfter,
					uintptr(placement.x),
					uintptr(placement.y),
					uintptr(w),
					uintptr(h),
					syncPopupPlacementFlags(placement.geometryChanged, zOrderChanged, placement.sizeChanged),
				)
			}
			expectedAbove = placement.hwnd
		}
	}
}

func isSyncPopupSurfaceCandidate(title string, popupRect, ownerRect winRect, ownerLinked bool, processLinked bool) bool {
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
	// Direct Win32 owner chain: every Chrome surface under that frame belongs to
	// the tiled environment (menus, wallet prompts, nested submenus).
	if ownerLinked {
		return true
	}
	// Never adopt another full browser frame even when the PID matches.
	if looksLikeMainBrowserWindowTitle(lowerTitle) && !isDefinitiveExtensionPopupTitle(lowerTitle) {
		return false
	}
	// Process-tree ownership (typical for MV3 wallet notification hosts): accept
	// wallet/extension titles and any smaller-than-owner Chrome widget, including
	// empty-title Aura notification shells.
	if processLinked {
		if isDefinitiveExtensionPopupTitle(lowerTitle) || isCompactExtensionPopupTitle(title) {
			return true
		}
		return width < ownerWidth || height < ownerHeight
	}
	// Empty-title Aura widgets cover Chrome menus, comboboxes and nested menu
	// surfaces, but a full-size empty Chrome frame can be a second browser
	// window and must not be adopted as a popup. Titled prompt/notification
	// windows are also valid sync popups.
	return isDefinitiveExtensionPopupTitle(lowerTitle) || width < ownerWidth || height < ownerHeight
}

// constrainSyncPopupRect force-fits oversized popups into the owner cell.
// Production path uses constrainSyncPopupRectOptions with grace/notification
// policy; tests and delayed force-fit keep this full-fit behavior.
func constrainSyncPopupRect(popup, owner winRect, inset int) (x, y, width, height int, changed bool) {
	return constrainSyncPopupRectOptions(popup, owner, inset, true)
}

func constrainSyncPopupRectOptions(popup, owner winRect, inset int, allowResize bool) (x, y, width, height int, changed bool) {
	if inset < 0 {
		inset = 0
	}
	left := int(owner.Left) + inset
	top := int(owner.Top) + inset
	right := int(owner.Right) - inset
	bottom := int(owner.Bottom) - inset
	availableWidth := right - left
	availableHeight := bottom - top

	naturalWidth := int(popup.Right - popup.Left)
	naturalHeight := int(popup.Bottom - popup.Top)
	if naturalWidth <= 0 || naturalHeight <= 0 {
		return int(popup.Left), int(popup.Top), naturalWidth, naturalHeight, false
	}
	// Force-fit into the environment main cell when allowed:
	// shrink only if larger than the tiled owner; never stretch.
	// Notification hosts / open-grace use allowResize=false (position only).
	width, height = naturalWidth, naturalHeight
	if availableWidth <= 0 || availableHeight <= 0 {
		return int(popup.Left), int(popup.Top), width, height, false
	}
	if allowResize {
		if width > availableWidth {
			width = availableWidth
		}
		if height > availableHeight {
			height = availableHeight
		}
		// Minimum readable surface — avoid 1×1 thrash if owner is degenerate.
		if width < 80 && availableWidth >= 80 {
			width = 80
			if width > availableWidth {
				width = availableWidth
			}
		}
		if height < 80 && availableHeight >= 80 {
			height = 80
			if height > availableHeight {
				height = availableHeight
			}
		}
	}

	x = int(popup.Left)
	y = int(popup.Top)
	if x < left {
		x = left
	}
	if y < top {
		y = top
	}
	// When not resizing, still pin top-left into the cell so the visible chrome
	// stays with its environment (may spill bottom/right — better than blank).
	if allowResize {
		if x+width > right {
			x = right - width
			if x < left {
				x = left
			}
		}
		if y+height > bottom {
			y = bottom - height
			if y < top {
				y = top
			}
		}
	} else {
		// Position-only: if the whole popup is larger than the cell, keep the
		// origin at the cell top-left so title/controls remain clickable.
		if width > availableWidth {
			x = left
		} else if x+width > right {
			x = right - width
			if x < left {
				x = left
			}
		}
		if height > availableHeight {
			y = top
		} else if y+height > bottom {
			y = bottom - height
			if y < top {
				y = top
			}
		}
	}
	// Ignore 1px jitter so continuous confinement does not fight Chromium layout.
	posChanged := absSyncInt(x-int(popup.Left)) > 1 || absSyncInt(y-int(popup.Top)) > 1
	sizeChanged := allowResize && (absSyncInt(width-naturalWidth) > 2 || absSyncInt(height-naturalHeight) > 2)
	changed = posChanged || sizeChanged
	return x, y, width, height, changed
}
