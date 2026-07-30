//go:build windows

package backend

import (
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const syncPopupBoundsInset = 2

func syncPopupBoundsIntervalForFollowers(followerCount int) time.Duration {
	// Environment multi-open confinement is not input-critical. Prefer lower
	// CPU over sub-frame popup repositioning; geometry still settles quickly.
	switch {
	case followerCount >= 20:
		return 450 * time.Millisecond
	case followerCount >= 8:
		return 350 * time.Millisecond
	default:
		return 280 * time.Millisecond
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

	// Chrome and the extension own the popup's natural content size. The sync
	// assistant only keeps that surface inside its current environment cell.
	// In particular, do not apply the browser startup template or a wallet-
	// specific width/height here: those fixed sizes create blank canvas after
	// the environment has been tiled, stacked, or arranged horizontally. The
	// native client remains untouched, so its vertical/horizontal overflow and
	// the synchronizer's WM_MOUSEWHEEL/WM_MOUSEHWHEEL delivery keep working.
	x, y, width, height, shouldMove := constrainSyncPopupRect(popupRect, owner.rect, syncPopupBoundsInset)
	search.placements = append(search.placements, syncPopupPlacement{
		hwnd:            hwnd,
		owner:           owner,
		x:               x,
		y:               y,
		width:           width,
		height:          height,
		geometryChanged: shouldMove,
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

func syncPopupPlacementFlags(geometryChanged, zOrderChanged bool) uintptr {
	flags := uintptr(SWP_NOACTIVATE)
	if !geometryChanged {
		flags |= SWP_NOMOVE | SWP_NOSIZE
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
			if placement.geometryChanged || zOrderChanged {
				insertAfter := HWND_TOP
				if expectedAbove != 0 {
					insertAfter = uintptr(expectedAbove)
				}
				procSetWindowPos.Call(
					uintptr(placement.hwnd),
					insertAfter,
					uintptr(placement.x),
					uintptr(placement.y),
					uintptr(placement.width),
					uintptr(placement.height),
					syncPopupPlacementFlags(placement.geometryChanged, zOrderChanged),
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

	naturalWidth := int(popup.Right - popup.Left)
	naturalHeight := int(popup.Bottom - popup.Top)
	if naturalWidth <= 0 || naturalHeight <= 0 {
		return int(popup.Left), int(popup.Top), naturalWidth, naturalHeight, false
	}
	// Keep the extension's natural aspect ratio. Only shrink uniformly when the
	// popup cannot fit the current environment cell (tiled/stacked/side-by-side).
	// Never stretch or apply a fixed wallet size template.
	width, height = naturalWidth, naturalHeight
	if width > availableWidth || height > availableHeight {
		scaleW := float64(availableWidth) / float64(width)
		scaleH := float64(availableHeight) / float64(height)
		scale := scaleW
		if scaleH < scaleW {
			scale = scaleH
		}
		if scale < 1 {
			width = int(float64(naturalWidth) * scale)
			height = int(float64(naturalHeight) * scale)
			if width < 1 {
				width = 1
			}
			if height < 1 {
				height = 1
			}
			if width > availableWidth {
				width = availableWidth
			}
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
	if x+width > right {
		x = right - width
	}
	if y+height > bottom {
		y = bottom - height
	}
	changed = x != int(popup.Left) || y != int(popup.Top) || width != naturalWidth || height != naturalHeight
	return x, y, width, height, changed
}
