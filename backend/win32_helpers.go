//go:build windows

package backend

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ============================================================================
// Win32 常量（共享）
// ============================================================================

const (
	WM_MOUSEMOVE   = 0x0200
	WM_LBUTTONDOWN = 0x0201
	WM_LBUTTONUP   = 0x0202
	WM_RBUTTONDOWN = 0x0204
	WM_RBUTTONUP   = 0x0205
	WM_MBUTTONDOWN = 0x0207
	WM_MBUTTONUP   = 0x0208
	WM_MOUSEWHEEL  = 0x020A
	WM_KEYDOWN     = 0x0100
	WM_KEYUP       = 0x0101
	WM_CHAR        = 0x0102
	WM_SYSKEYDOWN  = 0x0104
	WM_SYSKEYUP    = 0x0105
	MK_LBUTTON     = 0x0001
	MK_RBUTTON     = 0x0002
	MK_SHIFT       = 0x0004
	MK_CONTROL     = 0x0008
	MK_MBUTTON     = 0x0010
	MK_NONE        = 0x0000
	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
	SWP_SHOWWINDOW = 0x0040

	// Virtual Key Codes
	VK_CONTROL = 0x11
	VK_SHIFT   = 0x10
	VK_MENU    = 0x12 // Alt
	VK_F1      = 0x70
	VK_F2      = 0x71
	VK_F3      = 0x72
	VK_F4      = 0x73
	VK_F5      = 0x74
	VK_F6      = 0x75
	VK_F7      = 0x76
	VK_F8      = 0x77
	VK_F9      = 0x78
	VK_F10     = 0x79
	VK_F11     = 0x7A
	VK_F12     = 0x7B

	// Arrow keys and navigation keys (used for scroll sync)
	VK_UP     = 0x26
	VK_DOWN   = 0x28
	VK_LEFT   = 0x25
	VK_RIGHT  = 0x27
	VK_PRIOR  = 0x21 // Page Up
	VK_NEXT   = 0x22 // Page Down
	VK_HOME   = 0x24
	VK_END    = 0x23
	VK_INSERT = 0x2D
	VK_DELETE = 0x2E
)

var (
	user32dll   = windows.NewLazyDLL("user32.dll")
	gdi32dll    = windows.NewLazyDLL("gdi32.dll")
	kernel32dll = windows.NewLazyDLL("kernel32.dll")

	procFindWindowW              = user32dll.NewProc("FindWindowW")
	procEnumWindows              = user32dll.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32dll.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32dll.NewProc("IsWindowVisible")
	procGetWindowTextLengthW     = user32dll.NewProc("GetWindowTextLengthW")
	procGetForegroundWindow      = user32dll.NewProc("GetForegroundWindow")
	procShowWindow               = user32dll.NewProc("ShowWindow")
	procSetForegroundWindow      = user32dll.NewProc("SetForegroundWindow")
	procSendMessageW             = user32dll.NewProc("SendMessageW")
	procPostMessageW             = user32dll.NewProc("PostMessageW")
	procScreenToClient           = user32dll.NewProc("ScreenToClient")
	procSetWindowPos             = user32dll.NewProc("SetWindowPos")
	procGetSystemMetrics         = user32dll.NewProc("GetSystemMetrics")
	procGetDC                    = user32dll.NewProc("GetDC")
	procReleaseDC                = user32dll.NewProc("ReleaseDC")
	procCreateCompatibleDC       = gdi32dll.NewProc("CreateCompatibleDC")
	procDeleteDC                 = gdi32dll.NewProc("DeleteDC")
	procCreateDIBSection         = gdi32dll.NewProc("CreateDIBSection")
	procDeleteObject             = gdi32dll.NewProc("DeleteObject")
	procLoadImageW               = user32dll.NewProc("LoadImageW")
	procDestroyIcon              = user32dll.NewProc("DestroyIcon")
	procIsWindow                 = user32dll.NewProc("IsWindow")
	procDrawIconEx               = user32dll.NewProc("DrawIconEx")
	procGetAsyncKeyState         = user32dll.NewProc("GetAsyncKeyState")
	procSelectObject             = gdi32dll.NewProc("SelectObject")
	procGetClassLongPtrW         = user32dll.NewProc("GetClassLongPtrW")
	procGetWindowTextW           = user32dll.NewProc("GetWindowTextW")
	procGetClassNameW            = user32dll.NewProc("GetClassNameW")
	procGetClientRect            = user32dll.NewProc("GetClientRect")
	procClientToScreen           = user32dll.NewProc("ClientToScreen")
	procMapVirtualKeyW           = user32dll.NewProc("MapVirtualKeyW")
	procGetWindow                = user32dll.NewProc("GetWindow")
	procCreateToolhelp32Snapshot = kernel32dll.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32dll.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32dll.NewProc("Process32NextW")
)

// MAKELONG 模拟 Win32 MAKELONG 宏
func MAKELONG(lo, hi uint16) uintptr {
	return uintptr(lo) | (uintptr(hi) << 16)
}

// makeKeyLParam 生成 WM_KEYDOWN/WM_KEYUP 的 lParam
// https://learn.microsoft.com/en-us/windows/win32/inputdev/wm-keydown
// lParam 格式:
//
//	Bits 0-15:  Repeat count (keydown 为 1, keyup 为 0 不重要)
//	Bits 16-23: Scan code (MapVirtualKey 获取)
//	Bit 24:     Extended key flag (方向键、Page Up/Down 等为 1)
//	Bits 25-28: Reserved
//	Bit 29:     Context code (Alt 按下时为 1)
//	Bit 30:     Previous key state (keydown 为 0, keyup 为 1)
//	Bit 31:     Transition state (keydown 为 0, keyup 为 1)
func makeKeyLParam(vk uint32, isKeyDown bool) uintptr {
	scanCode, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0) // MAPVK_VK_TO_VSC = 0

	var extended uint32
	// Extended keys: 方向键、Page Up/Down、Insert/Delete、Home/End 等
	switch vk {
	case 0x25, 0x26, 0x27, 0x28, // VK_LEFT, VK_UP, VK_RIGHT, VK_DOWN
		0x21, 0x22, // VK_PRIOR, VK_NEXT (Page Up, Page Down)
		0x24, 0x23, // VK_HOME, VK_END
		0x2D, 0x2E, // VK_INSERT, VK_DELETE
		0x11: // VK_CONTROL
		extended = 1
	}

	repeatCount := uint32(1)
	previousState := uint32(0)
	transitionState := uint32(0)
	if !isKeyDown {
		previousState = 1
		transitionState = 1
		repeatCount = 0 // keyup 不重要
	}

	lparam := uintptr(repeatCount & 0xFFFF)    // Bits 0-15
	lparam |= uintptr((scanCode & 0xFF) << 16) // Bits 16-23
	lparam |= uintptr(extended << 24)          // Bit 24
	lparam |= uintptr(previousState << 30)     // Bit 30
	lparam |= uintptr(transitionState << 31)   // Bit 31

	return lparam
}

func isWindow(hwnd windows.HWND) bool {
	ret, _, _ := procIsWindow.Call(uintptr(hwnd))
	return ret != 0
}

func isWindowVisible(hwnd windows.HWND) bool {
	ret, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
	return ret != 0
}

func isAuxiliaryIMEWindowTitleOrClass(title, className string) bool {
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	lowerClass := strings.ToLower(strings.TrimSpace(className))
	return strings.Contains(lowerTitle, "default ime") ||
		strings.Contains(lowerTitle, "ime") ||
		strings.Contains(lowerClass, "ime")
}

func screenToClient(hwnd windows.HWND, x, y int) (int, int) {
	type POINT struct{ X, Y int32 }
	pt := POINT{X: int32(x), Y: int32(y)}
	procScreenToClient.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pt)))
	return int(pt.X), int(pt.Y)
}

func getClientSize(hwnd windows.HWND) (int, int, bool) {
	type RECT struct{ Left, Top, Right, Bottom int32 }
	var rect RECT
	ret, _, _ := procGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return 0, 0, false
	}
	return int(rect.Right - rect.Left), int(rect.Bottom - rect.Top), true
}

// findProcessWindow 通过进程 PID 找到该进程的主浏览器窗口句柄。
// Windows/Chrome 有时会给同一 PID 暴露 "Default IME" 等辅助顶级窗口；
// 如果直接按标题最长选择，会给这些 IME 辅助窗口设置任务栏 badge，导致 Alt+Tab/任务栏
// 出现“不应该有”的 Default IME 缩略图。因此这里优先选择 Chrome_WidgetWin_* 主窗口，
// 并显式排除 IME/输入法辅助窗口。
type processWindowCandidate struct {
	hwnd  windows.HWND
	score int
}

type processWindowSearch struct {
	pid        int
	candidates []processWindowCandidate
}

// EnumWindows callbacks allocated through windows.NewCallback are backed by a
// process-wide, finite Go callback table and cannot be released. Creating one
// on every lookup eventually terminates the Wails host (especially while badge
// retries and input sync repeatedly resolve nine or more Chromium windows).
// Keep exactly one callback and pass per-call state through lParam instead.
var processWindowEnumCallback = windows.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
	defer func() {
		_ = recover()
	}()
	search := (*processWindowSearch)(unsafe.Pointer(lParam))
	if search == nil {
		return 0
	}
	var windowPID uint32
	procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&windowPID)))
	if int(windowPID) != search.pid {
		return 1 // 继续
	}

	visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
	if visible == 0 {
		return 1
	}

	title := getWindowTitle(hwnd)
	className := getWindowClassName(hwnd)
	if isAuxiliaryIMEWindowTitleOrClass(title, className) {
		return 1
	}
	// Only a real Chrome top-level browser frame may participate in sync.
	// Chrome_WidgetWin_0 and titled renderer/extension helper HWNDs can be
	// visible transiently; selecting one makes tiling/show operations surface
	// an undecorated white window over the page.
	isPrimaryClass := strings.EqualFold(className, "Chrome_WidgetWin_1") || strings.EqualFold(className, "Chrome_MainWindow")
	isCloakTopLevelFallback := strings.EqualFold(className, "Chrome_WidgetWin_0") && strings.TrimSpace(title) != ""
	if !isPrimaryClass && !isCloakTopLevelFallback {
		return 1
	}
	// Chrome_WidgetWin_0 is also used by transient helpers. Only accept an
	// unowned, titled top-level frame as the CloakBrowser fallback.
	if isCloakTopLevelFallback {
		const gwOwner = 4
		owner, _, _ := procGetWindow.Call(uintptr(hwnd), gwOwner)
		if owner != 0 {
			return 1
		}
	}
	clientW, clientH, ok := getClientSize(hwnd)
	if !ok || clientW < 320 || clientH < 240 || clientW > 10000 || clientH > 10000 {
		return 1
	}

	score := len(title)
	score += 10000 + clientW*clientH/1000
	search.candidates = append(search.candidates, processWindowCandidate{hwnd: hwnd, score: score})
	return 1 // 继续找
})

func findProcessWindow(pid int) (windows.HWND, error) {
	search := &processWindowSearch{pid: pid}

	procEnumWindows.Call(processWindowEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)

	if len(search.candidates) == 0 {
		return 0, fmt.Errorf("未找到 PID=%d 的浏览器主窗口", pid)
	}

	best := search.candidates[0]
	for _, c := range search.candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}
	return best.hwnd, nil
}

// findProcessTreeWindow resolves the real Chrome frame even when the launcher
// PID hands the browser window to a child process. CloakBrowser and current
// Chrome builds can both exhibit this during startup/recovery.
func findProcessTreeWindow(rootPID int) (windows.HWND, error) {
	if hwnd, err := findProcessWindow(rootPID); err == nil {
		return hwnd, nil
	}
	const th32csSnapProcess = 0x00000002
	const invalidHandleValue = ^uintptr(0)
	type processEntry32 struct {
		Size            uint32
		Usage           uint32
		ProcessID       uint32
		DefaultHeapID   uintptr
		ModuleID        uint32
		Threads         uint32
		ParentProcessID uint32
		PriClassBase    int32
		Flags           uint32
		ExeFile         [260]uint16
	}
	snapshot, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snapshot == invalidHandleValue || snapshot == 0 {
		return 0, fmt.Errorf("无法读取 PID=%d 的进程树", rootPID)
	}
	defer windows.CloseHandle(windows.Handle(snapshot))

	children := map[int][]int{}
	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		children[int(entry.ParentProcessID)] = append(children[int(entry.ParentProcessID)], int(entry.ProcessID))
		entry.Size = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}

	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if hwnd, err := findProcessWindow(pid); err == nil {
			return hwnd, nil
		}
		queue = append(queue, children[pid]...)
	}
	return 0, fmt.Errorf("未找到 PID=%d 进程树中的浏览器主窗口", rootPID)
}
