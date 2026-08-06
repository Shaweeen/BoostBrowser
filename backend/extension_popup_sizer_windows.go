//go:build windows

package backend

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const swRestore = 9

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// defaultEnvironmentMainWindowWidth/Height are applied only to the environment
// main Chrome frame once per user-initiated start. Popups, extension windows
// and sync-arranged tiles are never forced to this size.
const (
	defaultEnvironmentMainWindowWidth  = 1400
	defaultEnvironmentMainWindowHeight = 600
)

// enforceBrowserWindowBounds applies the startup main-window template once:
//   - only the environment's primary browser frame (not wallet/extension popups);
//   - only when the user starts an environment (caller is the start path);
//   - skipped while an input-sync session is active so tile/stack/horizontal
//     layouts fully follow the user-selected arrangement and confinement rules.
//
// There is no continuous worker: one SetWindowPos, then hand off.
func enforceBrowserWindowBounds(pid, width, height int) {
	if pid <= 0 || width <= 0 || height <= 0 {
		return
	}
	// Sync panel owns geometry while input sync is running.
	if inputSyncSessionActive() {
		return
	}
	hwnd := findMainEnvironmentBrowserWindow(pid)
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(uintptr(hwnd), swRestore)
	// Default on-screen placement for a free (non-sync) start only.
	procSetWindowPos.Call(uintptr(hwnd), 0, 80, 80, uintptr(width), uintptr(height), SWP_NOZORDER|SWP_SHOWWINDOW)
}

// enforceMainEnvironmentWindowOnStart is the only entry the start path should
// call. It never sizes popups or extension surfaces.
func enforceMainEnvironmentWindowOnStart(pid int) {
	enforceBrowserWindowBounds(pid, defaultEnvironmentMainWindowWidth, defaultEnvironmentMainWindowHeight)
}

func inputSyncSessionActive() bool {
	syncState.mu.Lock()
	active := syncState.active
	syncState.mu.Unlock()
	return active
}

// findMainEnvironmentBrowserWindow returns the primary Chromium frame for an
// environment process tree, excluding extension/wallet popups and DevTools.
func findMainEnvironmentBrowserWindow(rootPID int) windows.HWND {
	if rootPID <= 0 {
		return 0
	}
	// Prefer the root process, then children (Cloak may host the frame in a child).
	if hwnd := bestMainEnvironmentWindowForPID(rootPID); hwnd != 0 {
		return hwnd
	}
	children := snapshotProcessChildren()
	if children == nil {
		return 0
	}
	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]bool{rootPID: true}
	var best processWindowCandidate
	found := false
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if candidate, ok := bestMainEnvironmentWindowCandidateForPID(pid); ok {
			if !found || candidate.score > best.score {
				best = candidate
				found = true
			}
		}
		queue = append(queue, children[pid]...)
	}
	if !found {
		return 0
	}
	return best.hwnd
}

func bestMainEnvironmentWindowForPID(pid int) windows.HWND {
	candidate, ok := bestMainEnvironmentWindowCandidateForPID(pid)
	if !ok {
		return 0
	}
	return candidate.hwnd
}

func bestMainEnvironmentWindowCandidateForPID(pid int) (processWindowCandidate, bool) {
	search := &processWindowSearch{pid: pid}
	procEnumWindows.Call(processWindowEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)
	var best processWindowCandidate
	found := false
	for _, c := range search.candidates {
		title := getWindowTitle(c.hwnd)
		if !isMainEnvironmentBrowserFrame(c.hwnd, title) {
			continue
		}
		// Re-score: large main frames beat small popup-class surfaces that
		// still passed the generic Chrome window filter.
		score := c.score
		if looksLikeMainBrowserWindowTitle(title) {
			score += 50000
		}
		if w, h, ok := getClientSize(c.hwnd); ok {
			score += w * h / 100
		}
		c.score = score
		if !found || c.score > best.score {
			best = c
			found = true
		}
	}
	return best, found
}

func isMainEnvironmentBrowserFrame(hwnd windows.HWND, title string) bool {
	if hwnd == 0 {
		return false
	}
	if looksLikeServiceWorkerDevToolsTitle(title) {
		return false
	}
	if isAuxiliaryIMEWindowTitleOrClass(title, getWindowClassName(hwnd)) {
		return false
	}
	w, h, ok := getClientSize(hwnd)
	if !ok {
		return false
	}
	return environmentFrameLooksLikeMain(w, h, title)
}

func getWindowClassName(hwnd windows.HWND) string {
	buf := make([]uint16, 256)
	ret, _, _ := procGetClassNameW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func getWindowTitle(hwnd windows.HWND) string {
	length, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd))
	if length <= 0 {
		return ""
	}
	buf := make([]uint16, int(length)+1)
	procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

func getTopLevelWindowRect(hwnd windows.HWND) (winRect, bool) {
	var rect winRect
	ret, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	return rect, ret != 0
}
