//go:build windows

package backend

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"boost-browser/backend/internal/logger"

	"golang.org/x/sys/windows"
)

// ============================================================================
// InputSyncer — 输入同步引擎（参照 Python Chrome-Manager 实现）
//
// 核心原则：
// 1. 网页内容区通过 CDP 同步，避免后台 renderer 忽略 Win32 消息。
// 2. Chrome 原生界面按实际输入表面映射：主框架对主框架、菜单/确认框对对应弹层。
// 3. 弹层使用同步 Win32 消息推进 hover/submenu 状态，滚轮保留原始 delta。
// 4. 原生输入区同步主控键盘布局；网页输入框额外镜像实际值以覆盖 IME composition。
// ============================================================================

type InputSyncer struct {
	mu            sync.Mutex
	masterHwnd    windows.HWND
	followerHwnds []windows.HWND
	masterPid     int
	masterDebug   int
	followerDebug []int

	// 原子状态：钩子回调中只读 atomic，不加锁
	active             int32 // 1=活跃, 0=停止
	mouseEnabled       int32 // 1=启用, 0=禁用
	keyEnabled         int32 // 1=启用, 0=禁用
	randomDelayEnabled int32
	randomDelayMinMs   int32
	randomDelayMaxMs   int32
	randomDelayMu      sync.Mutex
	randomDelayNext    map[windows.HWND]time.Time

	mouseHook    uintptr
	keyHook      uintptr
	stopCh       chan struct{}
	stopOnce     sync.Once
	hookThreadID uint32

	lastMoveTime          int64 // Unix nano
	pageKeyboardFocus     int32 // last master click was inside the renderer
	pointerInsideMaster   int32 // pointer is inside master frame or an owned Chrome popup
	activePageMouseButton int32 // Win32 button-down message while dragging page content/scrollbars
	cdpKeyQueue           chan cdpKeyEvent
	cdpKeyDrops           int32
	pageInputQueue        chan func()
	pageInputDrops        int32

	// URL 同步
	urlStopCh                chan struct{}
	lastSyncURL              string
	lastFocusedEditableState string

	// 跟随窗口列表原子快照
	followerSnapshot []windows.HWND
	followerMu       sync.RWMutex

	// 诊断计数器
	clickCount   int32
	moveCount    int32
	wheelCount   int32
	keyCount     int32
	hookInstalls int32

	lifecycleLogger func(event string, fields ...string)
}

// Low-level hook callbacks are process-global resources in the Go Windows
// runtime. Allocate them once and route to the single active sync session.
// Recreating callbacks on every start eventually exhausts the callback table.
var activeInputSyncer atomic.Pointer[InputSyncer]

var processMouseHookCallback = windows.NewCallback(func(nCode int, wParam, lParam uintptr) uintptr {
	if syncer := activeInputSyncer.Load(); syncer != nil {
		return syncer.mouseHookCallback(nCode, wParam, lParam)
	}
	return callNextHook(nCode, wParam, lParam)
})

var processKeyHookCallback = windows.NewCallback(func(nCode int, wParam, lParam uintptr) uintptr {
	if syncer := activeInputSyncer.Load(); syncer != nil {
		return syncer.keyHookCallback(nCode, wParam, lParam)
	}
	return callNextHook(nCode, wParam, lParam)
})

// SyncConfig 同步配置
type SyncConfig struct {
	MouseEnabled       bool `json:"mouseEnabled"`
	KeyEnabled         bool `json:"keyEnabled"`
	RandomDelayEnabled bool `json:"randomDelayEnabled"`
	RandomDelayMinMs   int  `json:"randomDelayMinMs"`
	RandomDelayMaxMs   int  `json:"randomDelayMaxMs"`
}

type cdpKeyEvent struct {
	ports     []int
	hwnds     []windows.HWND
	down      bool
	vk        uint32
	character rune
	modifiers int
}

// NewInputSyncer 创建输入同步器
func NewInputSyncer() *InputSyncer {
	return NewInputSyncerWithLogger(nil)
}

func NewInputSyncerWithLogger(lifecycleLogger func(event string, fields ...string)) *InputSyncer {
	return &InputSyncer{
		stopCh:          make(chan struct{}),
		lifecycleLogger: lifecycleLogger,
		randomDelayNext: make(map[windows.HWND]time.Time),
		cdpKeyQueue:     make(chan cdpKeyEvent, 512),
		pageInputQueue:  make(chan func(), 512),
	}
}

func (s *InputSyncer) lifecycle(event string, fields ...string) {
	if s != nil && s.lifecycleLogger != nil {
		s.lifecycleLogger(event, fields...)
	}
}

func syncURLSyncEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("BOOST_BROWSER_ENABLE_SYNC_URL_SYNC")))
	// Navigation synchronization is safe inside the isolated sync-panel process
	// and is required for omnibox input: Chrome does not route background
	// WM_CHAR messages to its address bar. Allow an explicit opt-out for
	// diagnostics, but enable the reliable CDP path by default.
	if value == "" {
		return true
	}
	switch value {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func syncDebugLogEnabled() bool {
	return syncEnvFlagEnabled("BOOST_BROWSER_SYNC_DEBUG_LOG")
}

func syncEnvFlagEnabled(name string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Start 启动输入同步
func (s *InputSyncer) Start(masterHwnd windows.HWND, followerHwnds []windows.HWND, masterPid int) error {
	if atomic.LoadInt32(&s.active) == 1 {
		// Stop takes s.mu internally. Do not call it while holding s.mu or a
		// restart from the sync panel deadlocks the backend request.
		s.Stop()
		time.Sleep(100 * time.Millisecond)
	}

	s.mu.Lock()

	s.masterHwnd = masterHwnd
	s.masterPid = masterPid
	// 过滤掉主控窗口本身
	filtered := make([]windows.HWND, 0, len(followerHwnds))
	for _, h := range followerHwnds {
		if h != masterHwnd {
			filtered = append(filtered, h)
		}
	}
	s.followerHwnds = filtered

	// 更新原子快照
	s.followerMu.Lock()
	s.followerSnapshot = make([]windows.HWND, len(filtered))
	copy(s.followerSnapshot, filtered)
	s.followerMu.Unlock()

	atomic.StoreInt32(&s.active, 1)
	atomic.StoreInt32(&s.mouseEnabled, 1)
	atomic.StoreInt32(&s.keyEnabled, 1)
	// Default to immediate delivery. Delay is enabled only after the user
	// explicitly selects a preset in the sync assistant.
	atomic.StoreInt32(&s.randomDelayEnabled, 0)
	atomic.StoreInt32(&s.randomDelayMinMs, 0)
	atomic.StoreInt32(&s.randomDelayMaxMs, 0)
	s.stopCh = make(chan struct{})
	s.stopOnce = sync.Once{}
	s.cdpKeyQueue = make(chan cdpKeyEvent, 512)
	s.pageInputQueue = make(chan func(), 512)
	ready := make(chan error, 1)

	// 重置诊断计数器
	atomic.StoreInt32(&s.clickCount, 0)
	atomic.StoreInt32(&s.moveCount, 0)
	atomic.StoreInt32(&s.wheelCount, 0)
	atomic.StoreInt32(&s.keyCount, 0)
	atomic.StoreInt32(&s.cdpKeyDrops, 0)
	atomic.StoreInt32(&s.pageInputDrops, 0)
	atomic.StoreInt32(&s.activePageMouseButton, 0)
	if x, y, ok := currentCursorPosition(); ok && pointInsideMasterInputRegion(masterHwnd, x, y) {
		atomic.StoreInt32(&s.pointerInsideMaster, 1)
	} else {
		atomic.StoreInt32(&s.pointerInsideMaster, 0)
	}

	log := logger.New("InputSyncer")
	log.Info("输入同步已启动",
		logger.F("master_hwnd", masterHwnd),
		logger.F("master_pid", masterPid),
		logger.F("follower_count", len(filtered)),
	)
	s.lifecycle("sync-input-start", fmt.Sprintf("master_hwnd=%#x", masterHwnd), fmt.Sprintf("master_pid=%d", masterPid), fmt.Sprintf("follower_count=%d", len(filtered)))

	// 诊断日志
	syncLog("=== InputSyncer Start (Chrome-Manager style) ===")
	syncLog("masterHwnd=%#x pid=%d", masterHwnd, masterPid)
	mRLeft, mRTop, mRRight, mRBottom := getWindowRect(masterHwnd)
	syncLog("master rect=(%d,%d,%d,%d) size=%dx%d", mRLeft, mRTop, mRRight, mRBottom, mRRight-mRLeft, mRBottom-mRTop)
	for i, fhwnd := range filtered {
		fL, fT, fR, fB := getWindowRect(fhwnd)
		syncLog("follower[%d]=%#x rect=(%d,%d,%d,%d) size=%dx%d", i, fhwnd, fL, fT, fR, fB, fR-fL, fB-fT)
	}

	activeInputSyncer.Store(s)
	s.mu.Unlock()
	go s.cdpKeyDispatchLoop(s.stopCh, s.cdpKeyQueue)
	go s.pageInputDispatchLoop(s.stopCh, s.pageInputQueue)

	// 安装全局鼠标和键盘钩子。启动必须等待安装结果；旧逻辑在安装
	// 失败时仍立即返回成功，前端因此会显示“同步中”但没有任何事件。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				activeInputSyncer.CompareAndSwap(s, nil)
				atomic.StoreInt32(&s.active, 0)
				logger.New("InputSyncer").Error("installHooks goroutine panic recovered",
					logger.F("error", r),
				)
				select {
				case ready <- fmt.Errorf("键鼠 Hook 安装异常: %v", r):
				default:
				}
			}
		}()
		s.installHooks(ready)
	}()

	select {
	case err := <-ready:
		if err != nil {
			activeInputSyncer.CompareAndSwap(s, nil)
			atomic.StoreInt32(&s.active, 0)
			return err
		}
		return nil
	case <-time.After(3 * time.Second):
		activeInputSyncer.CompareAndSwap(s, nil)
		atomic.StoreInt32(&s.active, 0)
		s.signalStop()
		s.mu.Lock()
		hookThreadID := s.hookThreadID
		s.mu.Unlock()
		if hookThreadID != 0 {
			user32dll.NewProc("PostThreadMessageW").Call(uintptr(hookThreadID), 0x0012, 0, 0)
		}
		return fmt.Errorf("键鼠 Hook 安装超时，请重新启动同步助手")
	}
}

// StartWithURLSync 启动带 CDP URL 同步的输入同步
func (s *InputSyncer) StartWithURLSync(masterHwnd windows.HWND, followerHwnds []windows.HWND, masterPid int, masterDebugPort int, followerDebugPorts []int) error {
	err := s.Start(masterHwnd, followerHwnds, masterPid)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.masterDebug = masterDebugPort
	s.followerDebug = followerDebugPorts
	s.mu.Unlock()

	if !syncURLSyncEnabled() {
		log := logger.New("InputSyncer")
		log.Info("CDP URL 同步已通过环境变量关闭", logger.F("disable_env", "BOOST_BROWSER_ENABLE_SYNC_URL_SYNC=0"))
		s.lifecycle("sync-url-sync", "state=disabled", "reason=env-opt-out")
		return nil
	}

	// 启动 URL 同步协程
	s.urlStopCh = make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.New("InputSyncer").Error("urlSyncLoop goroutine panic recovered",
					logger.F("error", r),
				)
			}
		}()
		s.urlSyncLoop()
	}()

	log := logger.New("InputSyncer")
	log.Info("CDP URL 同步已启动",
		logger.F("master_debug", masterDebugPort),
		logger.F("follower_count", len(followerDebugPorts)),
	)
	s.lifecycle("sync-url-sync", "state=enabled", "source=env:BOOST_BROWSER_ENABLE_SYNC_URL_SYNC")

	return nil
}

// Stop 停止输入同步
func (s *InputSyncer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if atomic.LoadInt32(&s.active) == 0 && s.mouseHook == 0 && s.keyHook == 0 && s.hookThreadID == 0 {
		return
	}

	syncLog("=== InputSyncer Stop (clicks=%d, moves=%d, wheels=%d, keys=%d) ===",
		atomic.LoadInt32(&s.clickCount), atomic.LoadInt32(&s.moveCount),
		atomic.LoadInt32(&s.wheelCount), atomic.LoadInt32(&s.keyCount))

	atomic.StoreInt32(&s.active, 0)
	activeInputSyncer.CompareAndSwap(s, nil)
	s.signalStop()

	// 停止 URL 同步
	if s.urlStopCh != nil {
		select {
		case <-s.urlStopCh:
		default:
			close(s.urlStopCh)
		}
		s.urlStopCh = nil
	}

	// 卸载钩子
	if s.mouseHook != 0 {
		procUnhookWindowsHookEx := user32dll.NewProc("UnhookWindowsHookEx")
		procUnhookWindowsHookEx.Call(uintptr(s.mouseHook))
		s.mouseHook = 0
	}
	if s.keyHook != 0 {
		procUnhookWindowsHookEx := user32dll.NewProc("UnhookWindowsHookEx")
		procUnhookWindowsHookEx.Call(uintptr(s.keyHook))
		s.keyHook = 0
	}
	if s.hookThreadID != 0 {
		procPostThreadMessageW := user32dll.NewProc("PostThreadMessageW")
		procPostThreadMessageW.Call(uintptr(s.hookThreadID), 0x0012, 0, 0) // WM_QUIT
		s.hookThreadID = 0
	}

	log := logger.New("InputSyncer")
	log.Info("输入同步已停止")
	s.lifecycle("sync-input-stop", fmt.Sprintf("clicks=%d", atomic.LoadInt32(&s.clickCount)), fmt.Sprintf("moves=%d", atomic.LoadInt32(&s.moveCount)), fmt.Sprintf("wheels=%d", atomic.LoadInt32(&s.wheelCount)), fmt.Sprintf("keys=%d", atomic.LoadInt32(&s.keyCount)))
}

func (s *InputSyncer) signalStop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// IsActive 返回同步是否活跃
func (s *InputSyncer) IsActive() bool {
	return atomic.LoadInt32(&s.active) == 1
}

// SetConfig 更新同步配置
func (s *InputSyncer) SetConfig(mouseEnabled, keyEnabled bool) {
	if mouseEnabled {
		atomic.StoreInt32(&s.mouseEnabled, 1)
	} else {
		atomic.StoreInt32(&s.mouseEnabled, 0)
	}
	if keyEnabled {
		atomic.StoreInt32(&s.keyEnabled, 1)
	} else {
		atomic.StoreInt32(&s.keyEnabled, 0)
	}
}

func (s *InputSyncer) SetRandomDelay(enabled bool, minMs, maxMs int) {
	if minMs < 0 {
		minMs = 0
	}
	if maxMs < minMs {
		maxMs = minMs
	}
	if maxMs > 5000 {
		maxMs = 5000
	}
	atomic.StoreInt32(&s.randomDelayMinMs, int32(minMs))
	atomic.StoreInt32(&s.randomDelayMaxMs, int32(maxMs))
	if enabled {
		atomic.StoreInt32(&s.randomDelayEnabled, 1)
	} else {
		atomic.StoreInt32(&s.randomDelayEnabled, 0)
	}
}

func (s *InputSyncer) dispatchWithRandomDelay(hwnd windows.HWND, action func()) {
	if atomic.LoadInt32(&s.randomDelayEnabled) == 0 {
		action()
		return
	}
	minMs := int(atomic.LoadInt32(&s.randomDelayMinMs))
	maxMs := int(atomic.LoadInt32(&s.randomDelayMaxMs))
	delayMs := minMs
	if maxMs > minMs {
		delayMs += rand.Intn(maxMs - minMs + 1)
	}
	now := time.Now()
	due := now.Add(time.Duration(delayMs) * time.Millisecond)
	s.randomDelayMu.Lock()
	if previous := s.randomDelayNext[hwnd]; !previous.IsZero() && !due.After(previous) {
		due = previous.Add(time.Millisecond)
	}
	s.randomDelayNext[hwnd] = due
	s.randomDelayMu.Unlock()
	time.AfterFunc(time.Until(due), action)
}

func (s *InputSyncer) postMessageWithRandomDelay(hwnd windows.HWND, msg, wparam, lparam uintptr) {
	s.dispatchWithRandomDelay(hwnd, func() {
		procPostMessageW.Call(uintptr(hwnd), msg, wparam, lparam)
	})
}

// GetConfig 返回当前同步配置
func (s *InputSyncer) GetConfig() SyncConfig {
	return SyncConfig{
		MouseEnabled:       atomic.LoadInt32(&s.mouseEnabled) == 1,
		KeyEnabled:         atomic.LoadInt32(&s.keyEnabled) == 1,
		RandomDelayEnabled: atomic.LoadInt32(&s.randomDelayEnabled) == 1,
		RandomDelayMinMs:   int(atomic.LoadInt32(&s.randomDelayMinMs)),
		RandomDelayMaxMs:   int(atomic.LoadInt32(&s.randomDelayMaxMs)),
	}
}

// GetStats 返回同步诊断统计
func (s *InputSyncer) GetStats() map[string]int32 {
	return map[string]int32{
		"clicks":              atomic.LoadInt32(&s.clickCount),
		"moves":               atomic.LoadInt32(&s.moveCount),
		"wheels":              atomic.LoadInt32(&s.wheelCount),
		"keys":                atomic.LoadInt32(&s.keyCount),
		"hooks":               atomic.LoadInt32(&s.hookInstalls),
		"cdpKeyDrops":         atomic.LoadInt32(&s.cdpKeyDrops),
		"pageInputDrops":      atomic.LoadInt32(&s.pageInputDrops),
		"pointerInsideMaster": atomic.LoadInt32(&s.pointerInsideMaster),
	}
}

func (s *InputSyncer) PointerInsideMaster() bool {
	return atomic.LoadInt32(&s.pointerInsideMaster) == 1
}

// ============================================================================
// 窗口辅助函数
// ============================================================================

var procGetAncestor = user32dll.NewProc("GetAncestor")
var procWindowFromPoint = user32dll.NewProc("WindowFromPoint")
var procSendMessageTimeoutW = user32dll.NewProc("SendMessageTimeoutW")
var procGetGUIThreadInfo = user32dll.NewProc("GetGUIThreadInfo")
var procGetKeyboardLayout = user32dll.NewProc("GetKeyboardLayout")
var procGetKeyboardState = user32dll.NewProc("GetKeyboardState")
var procGetCursorPos = user32dll.NewProc("GetCursorPos")
var imm32dll = windows.NewLazySystemDLL("imm32.dll")
var procImmIsIME = imm32dll.NewProc("ImmIsIME")

func getAncestor(hwnd windows.HWND, flags uint32) windows.HWND {
	ret, _, _ := procGetAncestor.Call(uintptr(hwnd), uintptr(flags))
	return windows.HWND(ret)
}

const GA_ROOT = 2

// isMasterForeground 检查主控窗口或其子窗口是否在前台
func (s *InputSyncer) isMasterForeground() bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	foreground := windows.HWND(fg)
	if foreground == s.masterHwnd {
		return true
	}
	root := getAncestor(foreground, GA_ROOT)
	if root == s.masterHwnd {
		return true
	}
	// Chromium can place omnibox/render focus on another top-level HWND owned by
	// the same browser process. Requiring exact HWND equality filters every
	// keyboard/mouse hook event even though the selected master is foreground.
	var masterWindowPID, foregroundPID uint32
	procGetWindowThreadProcessID.Call(uintptr(s.masterHwnd), uintptr(unsafe.Pointer(&masterWindowPID)))
	procGetWindowThreadProcessID.Call(uintptr(root), uintptr(unsafe.Pointer(&foregroundPID)))
	return masterWindowPID != 0 && masterWindowPID == foregroundPID
}

// getFollowerSnapshot 获取跟随窗口列表的原子快照（钩子回调中使用）
func (s *InputSyncer) getFollowerSnapshot() []windows.HWND {
	s.followerMu.RLock()
	defer s.followerMu.RUnlock()
	snapshot := make([]windows.HWND, len(s.followerSnapshot))
	copy(snapshot, s.followerSnapshot)
	return snapshot
}

// ============================================================================
// 坐标映射（Chrome-Manager 风格，不找 render child）
//
// 关键思路：直接用顶层窗口的 GetWindowRect 做比例换算
// 主控窗口 rect (包含标题栏/地址栏) → 计算相对坐标 → 跟随窗口 rect → 计算客户区坐标
// Chrome 内部会根据 Y 坐标将消息路由到标签栏/地址栏/render child
// ============================================================================

// mapCoordsChromeManager 将主控屏幕坐标映射到跟随窗口的客户区坐标。
//
// 多平铺方式（横向/竖列/网格）引入后，单纯基于顶层窗口 / render child 的混合映射
// 在某些宽高比下会把坐标送偏，表现为“同步像失效了一样”。
// 这里改回与 Python 稳定版一致的策略：
// 1) 先把屏幕坐标转成主控 top-level client 坐标
// 2) 按 master/follower 的 client 区比例做映射
// 3) render-content 映射只保留为兜底
func mapCoordsChromeManager(screenX, screenY int, masterHwnd, followerHwnd windows.HWND) (uintptr, bool) {
	if lparam, ok := mapCoordsViaClientArea(screenX, screenY, masterHwnd, followerHwnd); ok {
		return lparam, true
	}

	// 兜底：保留 render child 内容区映射，避免特殊窗口结构完全失效。
	if lparam, ok := mapCoordsViaRenderContent(screenX, screenY, masterHwnd, followerHwnd); ok {
		return lparam, true
	}

	return 0, false
}

type syncInputSurfaceCandidate struct {
	hwnd      windows.HWND
	className string
	left      int
	top       int
	width     int
	height    int
}

type syncInputSurfaceSearch struct {
	pid          uint32
	mainHwnd     windows.HWND
	master       syncInputSurfaceCandidate
	expectedLeft int
	expectedTop  int
	best         windows.HWND
	bestScore    int64
}

// syncInputSurfaceEnumCallback is process-global for the same reason as the
// other Win32 callbacks: windows.NewCallback entries cannot be released.
var syncInputSurfaceEnumCallback = windows.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
	defer func() { _ = recover() }()
	search := (*syncInputSurfaceSearch)(unsafe.Pointer(lParam))
	if search == nil || hwnd == search.mainHwnd || !isWindowVisible(hwnd) {
		return 1
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
	if pid != search.pid {
		return 1
	}
	className := getWindowClassName(hwnd)
	if !strings.EqualFold(className, search.master.className) || isAuxiliaryIMEWindowTitleOrClass(getWindowTitle(hwnd), className) {
		return 1
	}
	left, top, right, bottom := getWindowRect(hwnd)
	w, h := int(right-left), int(bottom-top)
	if w <= 8 || h <= 8 || w > 10000 || h > 10000 {
		return 1
	}
	score := popupSurfaceMatchScore(search.master, syncInputSurfaceCandidate{
		hwnd: hwnd, className: className, left: int(left), top: int(top), width: w, height: h,
	}, search.expectedLeft, search.expectedTop)
	if search.best == 0 || score < search.bestScore {
		search.best = hwnd
		search.bestScore = score
	}
	return 1
})

func popupSurfaceMatchScore(master, candidate syncInputSurfaceCandidate, expectedLeft, expectedTop int) int64 {
	sizeDelta := absSyncInt(master.width-candidate.width) + absSyncInt(master.height-candidate.height)
	positionDelta := absSyncInt(expectedLeft-candidate.left) + absSyncInt(expectedTop-candidate.top)
	return int64(sizeDelta)*1000 + int64(positionDelta)
}

func absSyncInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func windowFromScreenPoint(x, y int) windows.HWND {
	// POINT is passed by value as two packed signed 32-bit LONG values.
	packed := uintptr(uint64(uint32(int32(x))) | uint64(uint32(int32(y)))<<32)
	hwnd, _, _ := procWindowFromPoint.Call(packed)
	return windows.HWND(hwnd)
}

func windowPID(hwnd windows.HWND) uint32 {
	var pid uint32
	procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
	return pid
}

func currentCursorPosition() (int, int, bool) {
	type point struct{ X, Y int32 }
	var pt point
	ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return int(pt.X), int(pt.Y), ok != 0
}

func pointInsideWindow(hwnd windows.HWND, screenX, screenY int) bool {
	left, top, right, bottom := getWindowRect(hwnd)
	return right > left && bottom > top && screenX >= int(left) && screenX < int(right) && screenY >= int(top) && screenY < int(bottom)
}

func pointInsideMasterInputRegion(masterHwnd windows.HWND, screenX, screenY int) bool {
	if masterHwnd == 0 || !isWindow(masterHwnd) {
		return false
	}
	if pointInsideWindow(masterHwnd, screenX, screenY) {
		return true
	}
	// Menus and extension confirmation prompts are separate top-level Chrome
	// widgets and may extend a few pixels outside the tiled master frame.
	hit := windowFromScreenPoint(screenX, screenY)
	if hit == 0 {
		return false
	}
	root := getAncestor(hit, GA_ROOT)
	if root == 0 {
		root = hit
	}
	if windowPID(root) == 0 || windowPID(root) != windowPID(masterHwnd) {
		return false
	}
	className := strings.ToLower(getWindowClassName(root))
	return strings.HasPrefix(className, "chrome_widgetwin_") || strings.EqualFold(className, "chrome_mainwindow")
}

func chromeInputSurfaceAtPoint(mainHwnd windows.HWND, screenX, screenY int) windows.HWND {
	hit := windowFromScreenPoint(screenX, screenY)
	if hit == 0 {
		return mainHwnd
	}
	root := getAncestor(hit, GA_ROOT)
	if root == 0 {
		root = hit
	}
	if windowPID(root) != windowPID(mainHwnd) {
		return mainHwnd
	}
	className := getWindowClassName(root)
	if !strings.HasPrefix(strings.ToLower(className), "chrome_widgetwin_") && !strings.EqualFold(className, "Chrome_MainWindow") {
		return mainHwnd
	}
	return root
}

func findMatchingChromeInputSurface(masterSurface, masterMain, followerMain windows.HWND) windows.HWND {
	if masterSurface == 0 || masterSurface == masterMain {
		return followerMain
	}
	ml, mt, mr, mb := getWindowRect(masterSurface)
	mml, mmt, mmr, _ := getWindowRect(masterMain)
	fl, ft, fr, _ := getWindowRect(followerMain)
	master := syncInputSurfaceCandidate{
		hwnd: masterSurface, className: getWindowClassName(masterSurface),
		left: int(ml), top: int(mt), width: int(mr - ml), height: int(mb - mt),
	}
	search := &syncInputSurfaceSearch{
		pid: windowPID(followerMain), mainHwnd: followerMain, master: master,
		expectedLeft: expectedPopupSurfaceLeft(int(ml), int(mr), int(mml), int(mmr), int(fl), int(fr)),
		expectedTop:  int(ft) + int(mt-mmt),
		bestScore:    int64(^uint64(0) >> 1),
	}
	procEnumWindows.Call(syncInputSurfaceEnumCallback, uintptr(unsafe.Pointer(search)))
	runtime.KeepAlive(search)
	if search.best != 0 {
		return search.best
	}
	return followerMain
}

func expectedPopupSurfaceLeft(popupLeft, popupRight, masterLeft, masterRight, followerLeft, followerRight int) int {
	masterCenter := masterLeft + (masterRight-masterLeft)/2
	popupCenter := popupLeft + (popupRight-popupLeft)/2
	if popupCenter >= masterCenter {
		return followerRight - (masterRight - popupLeft)
	}
	return followerLeft + (popupLeft - masterLeft)
}

func mapPointBetweenInputSurfaces(screenX, screenY int, masterSurface, followerSurface windows.HWND) (uintptr, bool) {
	mx, my := screenToClient(masterSurface, screenX, screenY)
	mw, mh, ok := getClientSize(masterSurface)
	if !ok || mw <= 0 || mh <= 0 || mx < 0 || my < 0 || mx > mw || my > mh {
		return 0, false
	}
	fw, fh, ok := getClientSize(followerSurface)
	if !ok || fw <= 0 || fh <= 0 {
		return 0, false
	}
	fx := int(float64(mx) / float64(mw) * float64(fw))
	fy := int(float64(my) / float64(mh) * float64(fh))
	if fx < -32768 || fx > 32767 || fy < -32768 || fy > 32767 {
		return 0, false
	}
	return MAKELONG(uint16(int16(fx)), uint16(int16(fy))), true
}

func mapChromeInputTarget(screenX, screenY int, masterMain, followerMain windows.HWND) (windows.HWND, uintptr, bool) {
	masterSurface := chromeInputSurfaceAtPoint(masterMain, screenX, screenY)
	if masterSurface == masterMain {
		lparam, ok := mapCoordsChromeManager(screenX, screenY, masterMain, followerMain)
		return followerMain, lparam, ok
	}
	followerSurface := findMatchingChromeInputSurface(masterSurface, masterMain, followerMain)
	if followerSurface == followerMain {
		return 0, 0, false
	}
	lparam, ok := mapPointBetweenInputSurfaces(screenX, screenY, masterSurface, followerSurface)
	return followerSurface, lparam, ok
}

func sendChromeUIMouseMessage(hwnd windows.HWND, msg, wparam, lparam uintptr, popupSurface bool) {
	// Chrome menus are separate top-level widgets. Synchronous delivery makes
	// hover/submenu state advance before the following click is replayed.
	// Main-frame toolbar traffic stays asynchronous so many tiled followers
	// cannot stall the low-level hook thread while the pointer is moving.
	if !popupSurface {
		procPostMessageW.Call(uintptr(hwnd), msg, wparam, lparam)
		return
	}
	const smtoAbortIfHung = 0x0002
	procSendMessageTimeoutW.Call(uintptr(hwnd), msg, wparam, lparam, smtoAbortIfHung, 120, 0)
}

func chromeKeyboardTarget(mainHwnd windows.HWND) windows.HWND {
	type rect struct{ Left, Top, Right, Bottom int32 }
	type guiThreadInfo struct {
		Size          uint32
		Flags         uint32
		Active        windows.HWND
		Focus         windows.HWND
		Capture       windows.HWND
		MenuOwner     windows.HWND
		MoveSize      windows.HWND
		Caret         windows.HWND
		CaretPosition rect
	}
	threadID, _, _ := procGetWindowThreadProcessID.Call(uintptr(mainHwnd), 0)
	if threadID == 0 {
		return mainHwnd
	}
	info := guiThreadInfo{Size: uint32(unsafe.Sizeof(guiThreadInfo{}))}
	ok, _, _ := procGetGUIThreadInfo.Call(threadID, uintptr(unsafe.Pointer(&info)))
	if ok == 0 || info.Focus == 0 || windowPID(info.Focus) != windowPID(mainHwnd) {
		return mainHwnd
	}
	return info.Focus
}

func foregroundKeyboardLayout() uintptr {
	foreground, _, _ := procGetForegroundWindow.Call()
	if foreground == 0 {
		return 0
	}
	threadID, _, _ := procGetWindowThreadProcessID.Call(foreground, 0)
	if threadID == 0 {
		return 0
	}
	layout, _, _ := procGetKeyboardLayout.Call(threadID)
	return layout
}

func keyboardLayoutUsesIME(layout uintptr) bool {
	if layout == 0 {
		return false
	}
	result, _, _ := procImmIsIME.Call(layout)
	return result != 0
}

func mapCoordsViaClientArea(screenX, screenY int, masterHwnd, followerHwnd windows.HWND) (uintptr, bool) {
	mClientX, mClientY := screenToClient(masterHwnd, screenX, screenY)
	mW, mH, ok := getClientSize(masterHwnd)
	if !ok || mW <= 0 || mH <= 0 || mW > 10000 || mH > 10000 {
		return 0, false
	}

	relX := float64(mClientX) / float64(mW)
	relY := float64(mClientY) / float64(mH)
	if relX < 0 {
		relX = 0
	}
	if relX > 1 {
		relX = 1
	}
	if relY < 0 {
		relY = 0
	}
	if relY > 1 {
		relY = 1
	}

	fW, fH, ok := getClientSize(followerHwnd)
	if !ok || fW <= 0 || fH <= 0 || fW > 10000 || fH > 10000 {
		return 0, false
	}

	clientX := int(float64(fW) * relX)
	clientY := int(float64(fH) * relY)
	if clientX < -32768 || clientX > 32767 || clientY < -32768 || clientY > 32767 {
		return 0, false
	}
	return MAKELONG(uint16(int16(clientX)), uint16(int16(clientY))), true
}

// mapScreenPointToFollower maps a physical screen point from the master client
// to the equivalent physical screen point in a follower. WM_MOUSEWHEEL requires
// screen coordinates (unlike button and move messages, which use client
// coordinates), so reusing mapCoordsChromeManager here causes DPI-dependent
// drift and incorrect scrolling targets.
func mapScreenPointToFollower(screenX, screenY int, masterHwnd, followerHwnd windows.HWND) (int, int, bool) {
	mClientX, mClientY := screenToClient(masterHwnd, screenX, screenY)
	mW, mH, ok := getClientSize(masterHwnd)
	if !ok || mW <= 0 || mH <= 0 || mClientX < 0 || mClientY < 0 || mClientX > mW || mClientY > mH {
		return 0, 0, false
	}
	fW, fH, ok := getClientSize(followerHwnd)
	if !ok || fW <= 0 || fH <= 0 {
		return 0, 0, false
	}
	x := int(float64(mClientX) / float64(mW) * float64(fW))
	y := int(float64(mClientY) / float64(mH) * float64(fH))
	left, top, _, _ := getWindowRect(followerHwnd)
	return int(left) + x, int(top) + y, true
}

var procEnumChildWindows = user32dll.NewProc("EnumChildWindows")

func mapCoordsViaRenderContent(screenX, screenY int, masterHwnd, followerHwnd windows.HWND) (uintptr, bool) {
	masterRender := findChromeRenderChild(masterHwnd)
	followerRender := findChromeRenderChild(followerHwnd)
	if masterRender == 0 || followerRender == 0 {
		return 0, false
	}

	mLeft, mTop, mRight, mBottom := getWindowRect(masterRender)
	mW := mRight - mLeft
	mH := mBottom - mTop
	if mW <= 50 || mH <= 50 || mW > 10000 || mH > 10000 {
		return 0, false
	}
	if screenX < int(mLeft) || screenX > int(mRight) || screenY < int(mTop) || screenY > int(mBottom) {
		return 0, false
	}

	relX := float64(screenX-int(mLeft)) / float64(mW)
	relY := float64(screenY-int(mTop)) / float64(mH)
	if relX < 0 || relX > 1 || relY < 0 || relY > 1 {
		return 0, false
	}

	fLeft, fTop, fRight, fBottom := getWindowRect(followerRender)
	fW := fRight - fLeft
	fH := fBottom - fTop
	if fW <= 50 || fH <= 50 || fW > 10000 || fH > 10000 {
		return 0, false
	}

	targetScreenX := int(fLeft) + int(float64(fW)*relX)
	targetScreenY := int(fTop) + int(float64(fH)*relY)
	clientX, clientY := screenToClient(followerHwnd, targetScreenX, targetScreenY)
	if clientX < -32768 || clientX > 32767 || clientY < -32768 || clientY > 32767 {
		return 0, false
	}
	return MAKELONG(uint16(int16(clientX)), uint16(int16(clientY))), true
}

var chromeRenderChildEnumCallback = windows.NewCallback(func(child windows.HWND, lParam uintptr) uintptr {
	defer func() {
		_ = recover()
	}()
	found := (*windows.HWND)(unsafe.Pointer(lParam))
	if found == nil {
		return 0
	}
	if !isWindowVisible(child) {
		return 1
	}
	if getWindowClassName(child) != "Chrome_RenderWidgetHostHWND" {
		return 1
	}
	left, top, right, bottom := getWindowRect(child)
	if right-left <= 50 || bottom-top <= 50 {
		return 1
	}
	*found = child
	return 0
})

func findChromeRenderChild(hwnd windows.HWND) windows.HWND {
	var found windows.HWND
	procEnumChildWindows.Call(uintptr(hwnd), chromeRenderChildEnumCallback, uintptr(unsafe.Pointer(&found)))
	runtime.KeepAlive(&found)
	return found
}

func pointInsideChromeRender(hwnd windows.HWND, screenX, screenY int) bool {
	render := findChromeRenderChild(hwnd)
	if render == 0 {
		return false
	}
	return pointInsideWindow(render, screenX, screenY)
}

// ============================================================================
// 钩子安装和消息循环
// ============================================================================

func (s *InputSyncer) installHooks(ready chan<- error) {
	// WH_MOUSE_LL/WH_KEYBOARD_LL callbacks are delivered to the thread that
	// installed them. A Go goroutine may migrate between OS threads unless it is
	// pinned, leaving GetMessage on a different thread and producing zero input.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	syncLog("installHooks: 开始安装钩子...")

	procGetCurrentThreadId := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
	threadID, _, _ := procGetCurrentThreadId.Call()
	s.mu.Lock()
	s.hookThreadID = uint32(threadID)
	s.mu.Unlock()

	// WH_MOUSE_LL = 14, WH_KEYBOARD_LL = 13
	setHookEx := user32dll.NewProc("SetWindowsHookExW")
	mouseHook, mouseErr, mouseErrno := setHookEx.Call(14, processMouseHookCallback, 0, 0)
	keyHook, keyErr, keyErrno := setHookEx.Call(13, processKeyHookCallback, 0, 0)

	syncLog("installHooks: mouseHook=%#x err=%v errno=%d", mouseHook, mouseErr, mouseErrno)
	syncLog("installHooks: keyHook=%#x err=%v errno=%d", keyHook, keyErr, keyErrno)

	if mouseHook == 0 {
		syncLog("installHooks: ❌ 鼠标钩子安装失败！err=%v errno=%d", mouseErr, mouseErrno)
	}
	if keyHook == 0 {
		syncLog("installHooks: ❌ 键盘钩子安装失败！err=%v errno=%d", keyErr, keyErrno)
	}

	s.mu.Lock()
	s.mouseHook = mouseHook
	s.keyHook = keyHook
	s.mu.Unlock()

	if mouseHook != 0 && keyHook != 0 {
		atomic.StoreInt32(&s.hookInstalls, 2)
		syncLog("installHooks: ✅ 钩子安装成功，开始消息循环")
		s.lifecycle("sync-hooks", "state=installed", fmt.Sprintf("mouse_hook=%#x", mouseHook), fmt.Sprintf("key_hook=%#x", keyHook))
	} else {
		unhookWindowsHookEx := user32dll.NewProc("UnhookWindowsHookEx")
		if mouseHook != 0 {
			unhookWindowsHookEx.Call(mouseHook)
		}
		if keyHook != 0 {
			unhookWindowsHookEx.Call(keyHook)
		}
		s.mu.Lock()
		s.mouseHook = 0
		s.keyHook = 0
		s.mu.Unlock()
		syncLog("installHooks: ❌ Hook 未完整安装，中止同步")
		s.lifecycle("sync-hooks", "state=partial", fmt.Sprintf("mouse_hook=%#x", mouseHook), fmt.Sprintf("key_hook=%#x", keyHook))
		ready <- fmt.Errorf("键鼠 Hook 安装失败（mouse=%#x, keyboard=%#x, mouseErr=%v/%d, keyErr=%v/%d）", mouseHook, keyHook, mouseErr, mouseErrno, keyErr, keyErrno)
		return
	}
	defer func() {
		unhookWindowsHookEx := user32dll.NewProc("UnhookWindowsHookEx")
		if mouseHook != 0 {
			unhookWindowsHookEx.Call(mouseHook)
		}
		if keyHook != 0 {
			unhookWindowsHookEx.Call(keyHook)
		}
		s.mu.Lock()
		if s.mouseHook == mouseHook {
			s.mouseHook = 0
		}
		if s.keyHook == keyHook {
			s.keyHook = 0
		}
		s.hookThreadID = 0
		s.mu.Unlock()
	}()
	ready <- nil

	type MSG struct {
		HWnd    windows.HWND
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}

	getMessageW := user32dll.NewProc("GetMessageW")
	translateMessage := user32dll.NewProc("TranslateMessage")
	dispatchMessageW := user32dll.NewProc("DispatchMessageW")

	for {
		select {
		case <-s.stopCh:
			syncLog("installHooks: 收到停止信号，退出消息循环")
			return
		default:
		}

		var msg MSG
		ret, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 || ret == 0xFFFFFFFF {
			syncLog("installHooks: GetMessageW 返回 %d，退出消息循环", ret)
			s.lifecycle("sync-hook-loop-exit", fmt.Sprintf("ret=%d", ret))
			return
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// ============================================================================
// 鼠标钩子回调
// ============================================================================

// MSLLHOOKSTRUCT 结构体（64位对齐）
type MSLLHOOKSTRUCT struct {
	Pt          struct{ X, Y int32 }
	MouseData   uint32
	Flags       uint32
	Time        uint32
	_           uint32 // padding，确保 dwExtraInfo 在 8 字节边界
	DwExtraInfo uintptr
}

func (s *InputSyncer) mouseHookCallback(nCode int, wParam uintptr, lParam uintptr) uintptr {
	defer func() {
		if r := recover(); r != nil {
			logger.New("InputSyncer").Error("mouse hook callback panic recovered",
				logger.F("error", r),
			)
		}
	}()

	if nCode < 0 || atomic.LoadInt32(&s.active) == 0 || atomic.LoadInt32(&s.mouseEnabled) == 0 || !s.isMasterForeground() {
		return callNextHook(nCode, wParam, lParam)
	}
	if lParam == 0 {
		return callNextHook(nCode, wParam, lParam)
	}

	hook := (*MSLLHOOKSTRUCT)(unsafe.Pointer(lParam))
	msg := uint32(wParam)
	screenX := int(hook.Pt.X)
	screenY := int(hook.Pt.Y)
	if !pointInsideMasterInputRegion(s.masterHwnd, screenX, screenY) {
		if buttonDown := uint32(atomic.LoadInt32(&s.activePageMouseButton)); buttonDown != 0 {
			s.dispatchPageMouseViaCDP(pageMouseButtonUpMessage(buttonDown), screenX, screenY)
		}
		atomic.StoreInt32(&s.pointerInsideMaster, 0)
		atomic.StoreInt32(&s.pageKeyboardFocus, 0)
		atomic.StoreInt32(&s.activePageMouseButton, 0)
		return callNextHook(nCode, wParam, lParam)
	}
	atomic.StoreInt32(&s.pointerInsideMaster, 1)

	if msg == WM_LBUTTONDOWN || msg == WM_RBUTTONDOWN || msg == WM_MBUTTONDOWN {
		insidePage := pointInsideChromeRender(s.masterHwnd, screenX, screenY)
		if insidePage {
			atomic.StoreInt32(&s.pageKeyboardFocus, 1)
			atomic.StoreInt32(&s.activePageMouseButton, int32(msg))
		} else {
			atomic.StoreInt32(&s.pageKeyboardFocus, 0)
			atomic.StoreInt32(&s.activePageMouseButton, 0)
		}
	}

	// 获取快照（原子读取，不加锁）
	followers := s.getFollowerSnapshot()

	switch msg {
	case WM_LBUTTONDOWN, WM_LBUTTONUP, WM_RBUTTONDOWN, WM_RBUTTONUP, WM_MBUTTONDOWN, WM_MBUTTONUP:
		atomic.AddInt32(&s.clickCount, 1)
		if atomic.LoadInt32(&s.pageKeyboardFocus) == 1 {
			s.dispatchPageMouseViaCDP(msg, screenX, screenY)
			if msg == WM_LBUTTONUP || msg == WM_RBUTTONUP || msg == WM_MBUTTONUP {
				atomic.StoreInt32(&s.activePageMouseButton, 0)
			}
			return callNextHook(nCode, wParam, lParam)
		}
		for _, hwnd := range followers {
			if !isWindow(hwnd) {
				continue
			}

			// 映射坐标到跟随窗口客户区坐标（Chrome-Manager 风格）
			targetHwnd, lparam, ok := mapChromeInputTarget(screenX, screenY, s.masterHwnd, hwnd)
			if !ok {
				continue
			}

			// 构造 wParam（按键状态）
			var wparam uintptr
			switch msg {
			case WM_LBUTTONDOWN:
				wparam = MK_LBUTTON
			case WM_LBUTTONUP:
				wparam = 0
			case WM_RBUTTONDOWN:
				wparam = MK_RBUTTON
			case WM_RBUTTONUP:
				wparam = 0
			case WM_MBUTTONDOWN:
				wparam = MK_MBUTTON
			case WM_MBUTTONUP:
				wparam = 0
			}

			// 发到顶层窗口：先 WM_MOUSEMOVE 让 Chrome 更新 hover 状态
			s.dispatchWithRandomDelay(hwnd, func() {
				popupSurface := targetHwnd != hwnd
				sendChromeUIMouseMessage(targetHwnd, WM_MOUSEMOVE, wparam, lparam, popupSurface)
				sendChromeUIMouseMessage(targetHwnd, uintptr(msg), wparam, lparam, popupSurface)
			})

			// 仅在首次点击时记录详细日志（避免日志过多）
			if atomic.LoadInt32(&s.clickCount) <= 5 {
				mL, mT, mR, mB := getWindowRect(s.masterHwnd)
				fL, fT, fR, fB := getWindowRect(hwnd)
				syncLog("CLICK #%d: msg=%#x screen(%d,%d) masterRect=(%d,%d,%d,%d) followerRect=(%d,%d,%d,%d) lparam=%#x",
					atomic.LoadInt32(&s.clickCount), msg, screenX, screenY,
					mL, mT, mR, mB, fL, fT, fR, fB, lparam)
			}
		}

	case WM_MOUSEWHEEL, WM_MOUSEHWHEEL:
		atomic.AddInt32(&s.wheelCount, 1)
		// Preserve the exact signed delta, including high-resolution trackpad
		// values smaller than WHEEL_DELTA. Keyboard approximation loses both
		// magnitude and cursor target and makes followers scroll at a different
		// speed. Modifier state is carried in the low word as Win32 expects.
		wheelDelta := uint16(hook.MouseData >> 16)
		keyState := uint16(0)
		if isKeyDown(VK_CONTROL) {
			keyState |= MK_CONTROL
		}
		if isKeyDown(VK_SHIFT) {
			keyState |= MK_SHIFT
		}
		wheelWParam := uintptr(uint32(keyState) | uint32(wheelDelta)<<16)
		if pointInsideChromeRender(s.masterHwnd, screenX, screenY) {
			s.dispatchPageWheelViaCDP(msg, screenX, screenY, int16(wheelDelta), keyState)
			return callNextHook(nCode, wParam, lParam)
		}
		for _, hwnd := range followers {
			if !isWindow(hwnd) {
				continue
			}
			targetX, targetY, ok := mapScreenPointToFollower(screenX, screenY, s.masterHwnd, hwnd)
			if !ok || targetX < -32768 || targetX > 32767 || targetY < -32768 || targetY > 32767 {
				continue
			}
			wheelLParam := MAKELONG(uint16(int16(targetX)), uint16(int16(targetY)))
			s.dispatchWithRandomDelay(hwnd, func() {
				procPostMessageW.Call(uintptr(hwnd), uintptr(msg), wheelWParam, wheelLParam)
			})
		}

	case WM_MOUSEMOVE:
		atomic.AddInt32(&s.moveCount, 1)
		// 跟随窗口越多，鼠标移动同步越容易把整机拖卡。
		// 这里按窗口数动态降采样：少量窗口保留手感，多窗口优先稳。
		throttle := 8 * time.Millisecond
		switch followerCount := len(followers); {
		case followerCount >= 10:
			throttle = 24 * time.Millisecond
		case followerCount >= 6:
			throttle = 16 * time.Millisecond
		case followerCount >= 3:
			throttle = 12 * time.Millisecond
		}
		now := time.Now().UnixNano()
		last := atomic.LoadInt64(&s.lastMoveTime)
		if now-last < int64(throttle) {
			return callNextHook(nCode, wParam, lParam)
		}
		atomic.StoreInt64(&s.lastMoveTime, now)
		if buttonDown := uint32(atomic.LoadInt32(&s.activePageMouseButton)); buttonDown != 0 {
			s.dispatchPageMouseMoveViaCDP(screenX, screenY, buttonDown)
			return callNextHook(nCode, wParam, lParam)
		}

		for _, hwnd := range followers {
			if !isWindow(hwnd) {
				continue
			}
			targetHwnd, lparam, ok := mapChromeInputTarget(screenX, screenY, s.masterHwnd, hwnd)
			if !ok {
				continue
			}
			s.dispatchWithRandomDelay(hwnd, func() {
				sendChromeUIMouseMessage(targetHwnd, WM_MOUSEMOVE, 0, lparam, targetHwnd != hwnd)
			})
		}
	}

	return callNextHook(nCode, wParam, lParam)
}

// ============================================================================
// 键盘钩子回调
// ============================================================================

func (s *InputSyncer) keyHookCallback(nCode int, wParam uintptr, lParam uintptr) uintptr {
	defer func() {
		if r := recover(); r != nil {
			logger.New("InputSyncer").Error("key hook callback panic recovered",
				logger.F("error", r),
			)
		}
	}()

	if nCode < 0 || atomic.LoadInt32(&s.active) == 0 || atomic.LoadInt32(&s.keyEnabled) == 0 || atomic.LoadInt32(&s.pointerInsideMaster) == 0 || !s.isMasterForeground() {
		return callNextHook(nCode, wParam, lParam)
	}
	if lParam == 0 {
		return callNextHook(nCode, wParam, lParam)
	}

	type KBDLLHOOKSTRUCT struct {
		VkCode      uint32
		ScanCode    uint32
		Flags       uint32
		Time        uint32
		DwExtraInfo uintptr
	}
	hook := (*KBDLLHOOKSTRUCT)(unsafe.Pointer(lParam))

	vk := hook.VkCode
	msg := uint32(wParam)

	if msg != WM_KEYDOWN && msg != WM_KEYUP && msg != WM_SYSKEYDOWN && msg != WM_SYSKEYUP {
		return callNextHook(nCode, wParam, lParam)
	}

	atomic.AddInt32(&s.keyCount, 1)
	if atomic.LoadInt32(&s.pageKeyboardFocus) == 1 {
		s.dispatchPageKeyViaCDP(msg, hook.VkCode, hook.ScanCode, hook.Flags)
		return callNextHook(nCode, wParam, lParam)
	}

	// 获取跟随窗口快照
	followers := s.getFollowerSnapshot()

	// 检测修饰键状态
	ctrlPressed := isKeyDown(VK_CONTROL)
	altPressed := isKeyDown(VK_MENU)
	keyboardLayout := foregroundKeyboardLayout()
	imeActive := keyboardLayoutUsesIME(keyboardLayout)

	for _, hwnd := range followers {
		if !isWindow(hwnd) {
			continue
		}
		targetHwnd := chromeKeyboardTarget(hwnd)
		if keyboardLayout != 0 {
			// WM_INPUTLANGCHANGEREQUEST keeps native Chrome controls on the same
			// input layout as the selected master before replaying the key.
			s.postMessageWithRandomDelay(targetHwnd, 0x0050, 0, keyboardLayout)
		}

		if msg == WM_KEYDOWN {
			// 构造正确的 lParam
			keyParam := makeKeyLParam(vk, true)

			// Ctrl+A/C/V/X/Z 组合键
			if ctrlPressed {
				switch vk {
				case 0x41, 0x43, 0x56, 0x58, 0x5A: // A, C, V, X, Z
					s.postMessageWithRandomDelay(targetHwnd, WM_KEYDOWN, VK_CONTROL, makeKeyLParam(VK_CONTROL, true))
					s.postMessageWithRandomDelay(targetHwnd, WM_KEYDOWN, uintptr(vk), keyParam)
					s.postMessageWithRandomDelay(targetHwnd, WM_KEYUP, uintptr(vk), makeKeyLParam(vk, false))
					s.postMessageWithRandomDelay(targetHwnd, WM_KEYUP, VK_CONTROL, makeKeyLParam(VK_CONTROL, false))
					continue
				}
			}

			// Alt 组合键
			if altPressed {
				s.postMessageWithRandomDelay(targetHwnd, WM_SYSKEYDOWN, uintptr(vk), keyParam)
				continue
			}

			// IME must receive the original key sequence; turning it into WM_CHAR
			// bypasses composition and leaves follower omniboxes unchanged.
			if imeActive {
				s.postMessageWithRandomDelay(targetHwnd, WM_KEYDOWN, uintptr(vk), keyParam)
				continue
			}

			// 特殊键：只发 WM_KEYDOWN
			if isSpecialKey(vk) {
				s.postMessageWithRandomDelay(targetHwnd, WM_KEYDOWN, uintptr(vk), keyParam)
			} else {
				// 普通字符：只发 WM_CHAR
				ch := toUnicode(uint16(vk), uint16(hook.ScanCode), (hook.Flags&0x01) != 0)
				if ch != 0 {
					s.postMessageWithRandomDelay(targetHwnd, WM_CHAR, uintptr(ch), keyParam)
				}
			}
		} else if msg == WM_KEYUP {
			if !imeActive && !isSpecialKey(vk) && vk != VK_CONTROL && vk != VK_SHIFT && vk != VK_MENU {
				continue
			}
			if vk == VK_CONTROL || vk == VK_SHIFT || vk == VK_MENU {
				continue
			}
			s.postMessageWithRandomDelay(targetHwnd, WM_KEYUP, uintptr(vk), makeKeyLParam(vk, false))
		} else if msg == WM_SYSKEYDOWN {
			s.postMessageWithRandomDelay(targetHwnd, uintptr(msg), uintptr(vk), makeKeyLParam(vk, true))
		} else if msg == WM_SYSKEYUP {
			s.postMessageWithRandomDelay(targetHwnd, uintptr(msg), uintptr(vk), makeKeyLParam(vk, false))
		}
	}

	return callNextHook(nCode, wParam, lParam)
}

func (s *InputSyncer) dispatchPageKeyViaCDP(msg uint32, vk, scanCode, flags uint32) {
	s.mu.Lock()
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	if len(hwnds) == 0 {
		return
	}
	ctrl := isKeyDown(VK_CONTROL)
	alt := isKeyDown(VK_MENU)
	shift := isKeyDown(VK_SHIFT)
	modifiers := 0
	if alt {
		modifiers |= 1
	}
	if ctrl {
		modifiers |= 2
	}
	if shift {
		modifiers |= 8
	}
	down := msg == WM_KEYDOWN || msg == WM_SYSKEYDOWN
	ch := toUnicode(uint16(vk), uint16(scanCode), (flags&0x01) != 0)
	event := cdpKeyEvent{ports: ports, hwnds: hwnds, down: down, vk: vk, character: ch, modifiers: modifiers}
	select {
	case s.cdpKeyQueue <- event:
	default:
		atomic.AddInt32(&s.cdpKeyDrops, 1)
	}
}

func (s *InputSyncer) cdpKeyDispatchLoop(stopCh <-chan struct{}, queue <-chan cdpKeyEvent) {
	for {
		select {
		case <-stopCh:
			return
		case event := <-queue:
			var wg sync.WaitGroup
			for i, hwnd := range event.hwnds {
				port := 0
				if i < len(event.ports) {
					port = event.ports[i]
				}
				wg.Add(1)
				go func(debugPort int, follower windows.HWND) {
					s.dispatchWithRandomDelay(follower, func() {
						defer wg.Done()
						if debugPort <= 0 {
							s.dispatchPageKeyFallback(follower, event)
							return
						}
						if event.down && event.character != 0 && event.modifiers&3 == 0 {
							if _, err := cdpCall(debugPort, "Input.insertText", map[string]any{"text": string(event.character)}); err != nil {
								s.dispatchPageKeyFallback(follower, event)
							}
							return
						}
						params := map[string]any{
							"type":                  map[bool]string{true: "keyDown", false: "keyUp"}[event.down],
							"windowsVirtualKeyCode": int(event.vk), "nativeVirtualKeyCode": int(event.vk),
							"modifiers": event.modifiers, "key": cdpKeyName(event.vk),
						}
						if _, err := cdpCall(debugPort, "Input.dispatchKeyEvent", params); err != nil {
							s.dispatchPageKeyFallback(follower, event)
						}
					})
				}(port, hwnd)
			}
			wg.Wait()
		}
	}
}

func (s *InputSyncer) dispatchPageKeyFallback(hwnd windows.HWND, event cdpKeyEvent) {
	if !isWindow(hwnd) {
		return
	}
	target := findChromeRenderChild(hwnd)
	if target == 0 {
		target = hwnd
	}
	if event.down && event.character != 0 && event.modifiers&3 == 0 {
		s.postMessageWithRandomDelay(target, WM_CHAR, uintptr(event.character), makeKeyLParam(event.vk, true))
		return
	}
	message := uintptr(WM_KEYUP)
	if event.down {
		message = WM_KEYDOWN
	}
	s.postMessageWithRandomDelay(target, message, uintptr(event.vk), makeKeyLParam(event.vk, event.down))
}

func (s *InputSyncer) dispatchPageMouseViaCDP(msg uint32, screenX, screenY int) {
	s.enqueuePageInput(func() {
		s.dispatchPageMouseViaCDPNow(msg, screenX, screenY)
	})
}

func (s *InputSyncer) enqueuePageInput(action func()) {
	if action == nil || atomic.LoadInt32(&s.active) != 1 {
		return
	}
	select {
	case s.pageInputQueue <- action:
	default:
		atomic.AddInt32(&s.pageInputDrops, 1)
	}
}

func (s *InputSyncer) pageInputDispatchLoop(stopCh <-chan struct{}, queue <-chan func()) {
	for {
		select {
		case <-stopCh:
			return
		case action := <-queue:
			if action != nil && atomic.LoadInt32(&s.active) == 1 {
				action()
			}
		}
	}
}

func (s *InputSyncer) dispatchPageMouseViaCDPNow(msg uint32, screenX, screenY int) {
	masterRender := findChromeRenderChild(s.masterHwnd)
	if masterRender == 0 {
		return
	}
	ml, mt, mr, mb := getWindowRect(masterRender)
	if mr <= ml || mb <= mt {
		return
	}
	rx := float64(screenX-int(ml)) / float64(mr-ml)
	ry := float64(screenY-int(mt)) / float64(mb-mt)
	s.mu.Lock()
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	button := "left"
	if msg == WM_RBUTTONDOWN || msg == WM_RBUTTONUP {
		button = "right"
	}
	if msg == WM_MBUTTONDOWN || msg == WM_MBUTTONUP {
		button = "middle"
	}
	eventType := "mousePressed"
	if msg == WM_LBUTTONUP || msg == WM_RBUTTONUP || msg == WM_MBUTTONUP {
		eventType = "mouseReleased"
	}
	var wg sync.WaitGroup
	for i, hwnd := range hwnds {
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		wg.Add(1)
		go func(port int, hwnd windows.HWND) {
			defer wg.Done()
			if port <= 0 {
				s.dispatchPageMouseFallback(hwnd, msg, screenX, screenY)
				return
			}
			render := findChromeRenderChild(hwnd)
			if render == 0 {
				s.dispatchPageMouseFallback(hwnd, msg, screenX, screenY)
				return
			}
			fl, ft, fr, fb := getWindowRect(render)
			x, y := rx*float64(fr-fl), ry*float64(fb-ft)
			buttons := 0
			if eventType == "mousePressed" {
				_, buttons = pageMouseButton(msg)
			}
			s.dispatchWithRandomDelay(hwnd, func() {
				if _, err := cdpCall(port, "Input.dispatchMouseEvent", map[string]any{
					"type": eventType, "x": x, "y": y, "button": button, "buttons": buttons, "clickCount": 1,
				}); err != nil {
					s.dispatchPageMouseFallback(hwnd, msg, screenX, screenY)
				}
			})
		}(port, hwnd)
	}
	wg.Wait()
}

func pageMouseButton(buttonDownMsg uint32) (string, int) {
	switch buttonDownMsg {
	case WM_RBUTTONDOWN:
		return "right", 2
	case WM_MBUTTONDOWN:
		return "middle", 4
	default:
		return "left", 1
	}
}

func pageMouseButtonUpMessage(buttonDownMsg uint32) uint32 {
	switch buttonDownMsg {
	case WM_RBUTTONDOWN:
		return WM_RBUTTONUP
	case WM_MBUTTONDOWN:
		return WM_MBUTTONUP
	default:
		return WM_LBUTTONUP
	}
}

func (s *InputSyncer) dispatchPageMouseMoveViaCDP(screenX, screenY int, buttonDownMsg uint32) {
	s.enqueuePageInput(func() {
		s.dispatchPageMouseMoveViaCDPNow(screenX, screenY, buttonDownMsg)
	})
}

func (s *InputSyncer) dispatchPageMouseMoveViaCDPNow(screenX, screenY int, buttonDownMsg uint32) {
	masterRender := findChromeRenderChild(s.masterHwnd)
	if masterRender == 0 {
		return
	}
	ml, mt, mr, mb := getWindowRect(masterRender)
	if mr <= ml || mb <= mt {
		return
	}
	rx := float64(screenX-int(ml)) / float64(mr-ml)
	ry := float64(screenY-int(mt)) / float64(mb-mt)
	button, buttons := pageMouseButton(buttonDownMsg)
	s.mu.Lock()
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for i, hwnd := range hwnds {
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		wg.Add(1)
		go func(port int, hwnd windows.HWND) {
			defer wg.Done()
			render := findChromeRenderChild(hwnd)
			if port <= 0 || render == 0 {
				return
			}
			fl, ft, fr, fb := getWindowRect(render)
			x, y := rx*float64(fr-fl), ry*float64(fb-ft)
			s.dispatchWithRandomDelay(hwnd, func() {
				_, _ = cdpCall(port, "Input.dispatchMouseEvent", map[string]any{
					"type": "mouseMoved", "x": x, "y": y, "button": button, "buttons": buttons,
				})
			})
		}(port, hwnd)
	}
	wg.Wait()
}

func (s *InputSyncer) dispatchPageWheelViaCDP(msg uint32, screenX, screenY int, delta int16, keyState uint16) {
	s.enqueuePageInput(func() {
		s.dispatchPageWheelViaCDPNow(msg, screenX, screenY, delta, keyState)
	})
}

func (s *InputSyncer) dispatchPageWheelViaCDPNow(msg uint32, screenX, screenY int, delta int16, keyState uint16) {
	masterRender := findChromeRenderChild(s.masterHwnd)
	if masterRender == 0 {
		return
	}
	ml, mt, mr, mb := getWindowRect(masterRender)
	if mr <= ml || mb <= mt {
		return
	}
	rx := float64(screenX-int(ml)) / float64(mr-ml)
	ry := float64(screenY-int(mt)) / float64(mb-mt)
	deltaX, deltaY := float64(0), float64(0)
	if msg == WM_MOUSEHWHEEL {
		deltaX = float64(delta)
	} else {
		// Win32 positive means wheel-up; CDP positive deltaY scrolls down.
		deltaY = -float64(delta)
	}
	modifiers := 0
	if keyState&MK_CONTROL != 0 {
		modifiers |= 2
	}
	if keyState&MK_SHIFT != 0 {
		modifiers |= 8
	}
	s.mu.Lock()
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for i, hwnd := range hwnds {
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		wg.Add(1)
		go func(port int, hwnd windows.HWND) {
			defer wg.Done()
			render := findChromeRenderChild(hwnd)
			if port <= 0 || render == 0 {
				s.dispatchPageWheelFallback(hwnd, msg, screenX, screenY, delta, keyState)
				return
			}
			fl, ft, fr, fb := getWindowRect(render)
			x, y := rx*float64(fr-fl), ry*float64(fb-ft)
			s.dispatchWithRandomDelay(hwnd, func() {
				if _, err := cdpCall(port, "Input.dispatchMouseEvent", map[string]any{
					"type": "mouseWheel", "x": x, "y": y,
					"deltaX": deltaX, "deltaY": deltaY, "modifiers": modifiers,
				}); err != nil {
					s.dispatchPageWheelFallback(hwnd, msg, screenX, screenY, delta, keyState)
				}
			})
		}(port, hwnd)
	}
	wg.Wait()
}

func (s *InputSyncer) dispatchPageWheelFallback(hwnd windows.HWND, msg uint32, screenX, screenY int, delta int16, keyState uint16) {
	targetX, targetY, ok := mapScreenPointToFollower(screenX, screenY, s.masterHwnd, hwnd)
	if !ok || targetX < -32768 || targetX > 32767 || targetY < -32768 || targetY > 32767 {
		return
	}
	wparam := uintptr(uint32(keyState) | uint32(uint16(delta))<<16)
	lparam := MAKELONG(uint16(int16(targetX)), uint16(int16(targetY)))
	procPostMessageW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
}

func (s *InputSyncer) dispatchPageMouseFallback(hwnd windows.HWND, msg uint32, screenX, screenY int) {
	if !isWindow(hwnd) {
		return
	}
	lparam, ok := mapCoordsChromeManager(screenX, screenY, s.masterHwnd, hwnd)
	if !ok {
		return
	}
	wparam := uintptr(0)
	if msg == WM_LBUTTONDOWN {
		wparam = MK_LBUTTON
	} else if msg == WM_RBUTTONDOWN {
		wparam = MK_RBUTTON
	} else if msg == WM_MBUTTONDOWN {
		wparam = MK_MBUTTON
	}
	s.postMessageWithRandomDelay(hwnd, uintptr(msg), wparam, lparam)
}

func cdpKeyName(vk uint32) string {
	switch vk {
	case 0x08:
		return "Backspace"
	case 0x09:
		return "Tab"
	case 0x0D:
		return "Enter"
	case 0x1B:
		return "Escape"
	case VK_LEFT:
		return "ArrowLeft"
	case VK_RIGHT:
		return "ArrowRight"
	case VK_UP:
		return "ArrowUp"
	case VK_DOWN:
		return "ArrowDown"
	case VK_DELETE:
		return "Delete"
	case VK_HOME:
		return "Home"
	case VK_END:
		return "End"
	}
	if vk >= 0x41 && vk <= 0x5A {
		return strings.ToLower(string(rune(vk)))
	}
	return "Unidentified"
}

// isSpecialKey 判断是否为非打印特殊键
func isSpecialKey(vk uint32) bool {
	switch vk {
	case 0x08, 0x09, 0x0D, 0x1B, 0x20, // Backspace, Tab, Enter, Esc, Space
		0x25, 0x26, 0x27, 0x28, // Left, Up, Right, Down
		0x21, 0x22, 0x23, 0x24, // Page Up, Page Down, End, Home
		0x2D, 0x2E, // Insert, Delete
		0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7A, 0x7B: // F1-F12
		return true
	}
	return false
}

// ============================================================================
// 辅助函数
// ============================================================================

var procGetWindowRect = user32dll.NewProc("GetWindowRect")

func getWindowRect(hwnd windows.HWND) (left, top, right, bottom int32) {
	type RECT struct {
		Left, Top, Right, Bottom int32
	}
	var rect RECT
	procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	return rect.Left, rect.Top, rect.Right, rect.Bottom
}

func isKeyDown(vk uint32) bool {
	r1, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r1&0x8000 != 0
}

func buildKeyLParam(sc uint32, isExtended bool, isUp bool) uintptr {
	var lparam uintptr
	lparam = uintptr(sc) << 16
	if isExtended {
		lparam |= 1 << 24
	}
	if isUp {
		lparam |= 1<<30 | 1<<31
	}
	return lparam
}

var procToUnicodeEx = user32dll.NewProc("ToUnicodeEx")

func toUnicode(vk uint16, sc uint16, isExtended bool) rune {
	if isExtended {
		return 0
	}
	if vk >= 0x03 {
		switch {
		case vk >= 0x08 && vk <= 0x09:
			return 0
		case vk >= 0x0D && vk <= 0x0E:
			return 0
		case vk >= 0x10 && vk <= 0x12:
			return 0
		case vk == 0x1B:
			return 0
		case vk >= 0x20 && vk <= 0x2E:
			return 0
		case vk >= 0x70 && vk <= 0x87:
			return 0
		case vk >= 0x90 && vk <= 0x97:
			return 0
		}
	}

	var state [256]byte
	var buf [4]uint16
	procGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0])))
	for _, modifier := range []uint32{VK_SHIFT, VK_CONTROL, VK_MENU} {
		if isKeyDown(modifier) {
			state[modifier] |= 0x80
		} else {
			state[modifier] &^= 0x80
		}
	}

	scanCode := uint32(sc)
	if isExtended {
		scanCode |= 0x100
	}

	ret, _, _ := procToUnicodeEx.Call(
		uintptr(vk),
		uintptr(scanCode),
		uintptr(unsafe.Pointer(&state[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		4,
		0,
		foregroundKeyboardLayout(),
	)

	if ret == 1 {
		return rune(buf[0])
	}
	return 0
}

func callNextHook(nCode int, wParam uintptr, lParam uintptr) uintptr {
	procCallNextHook := user32dll.NewProc("CallNextHookEx")
	ret, _, _ := procCallNextHook.Call(0, uintptr(nCode), wParam, lParam)
	return ret
}

// ============================================================================
// 调试日志
// ============================================================================

var syncLogFile *os.File
var syncLogOnce sync.Once

func syncLog(format string, args ...interface{}) {
	if !syncDebugLogEnabled() {
		return
	}
	syncLogOnce.Do(func() {
		var err error
		syncLogFile, err = os.OpenFile(`C:\sync_debug.txt`, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return
		}
	})
	if syncLogFile != nil {
		msg := fmt.Sprintf(format, args...)
		syncLogFile.WriteString(time.Now().Format("15:04:05.000") + " " + msg + "\n")
		syncLogFile.Sync()
	}
}

// ============================================================================
// CDP URL 同步
// ============================================================================

func (s *InputSyncer) urlSyncLoop() {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.urlStopCh:
			return
		case <-ticker.C:
		}

		if atomic.LoadInt32(&s.active) == 0 {
			return
		}
		if atomic.LoadInt32(&s.pointerInsideMaster) == 0 {
			s.lastFocusedEditableState = ""
			continue
		}

		s.mu.Lock()
		masterDebug := s.masterDebug
		followerDebug := make([]int, len(s.followerDebug))
		copy(followerDebug, s.followerDebug)
		s.mu.Unlock()

		if masterDebug > 0 && len(followerDebug) > 0 {
			if atomic.LoadInt32(&s.pageKeyboardFocus) == 1 {
				if state := s.getMasterFocusedEditableState(masterDebug); state != "" && state != s.lastFocusedEditableState {
					s.lastFocusedEditableState = state
					var inputWG sync.WaitGroup
					for _, port := range followerDebug {
						if port <= 0 {
							continue
						}
						inputWG.Add(1)
						go func(debugPort int) {
							defer inputWG.Done()
							s.applyFollowerFocusedEditableState(debugPort, state)
						}(port)
					}
					inputWG.Wait()
				}
			} else {
				s.lastFocusedEditableState = ""
			}

			url := s.getMasterURL(masterDebug)
			if url != "" && url != s.lastSyncURL && !isAboutBlank(url) {
				s.lastSyncURL = url
				var wg sync.WaitGroup
				for _, port := range followerDebug {
					if port > 0 {
						wg.Add(1)
						go func(debugPort int) {
							defer wg.Done()
							s.navigateFollower(debugPort, url)
						}(port)
					}
				}
				wg.Wait()
			}
		}
	}
}

func cdpRuntimeValue(result map[string]any) (any, bool) {
	if value, ok := result["value"]; ok {
		return value, true
	}
	remote, ok := result["result"].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := remote["value"]
	return value, ok
}

func (s *InputSyncer) getMasterURL(debugPort int) string {
	result, err := cdpCall(debugPort, "Runtime.evaluate", map[string]any{
		"expression":    "location.href",
		"returnByValue": true,
	})
	if err != nil {
		return ""
	}
	val, ok := cdpRuntimeValue(result)
	if !ok {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		return ""
	}
	return str
}

func (s *InputSyncer) getMasterFocusedEditableState(debugPort int) string {
	const expression = `(() => {
		const e = document.activeElement;
		if (!e || (e.tagName !== 'INPUT' && e.tagName !== 'TEXTAREA')) return '';
		return JSON.stringify({
			value: String(e.value ?? ''),
			start: typeof e.selectionStart === 'number' ? e.selectionStart : -1,
			end: typeof e.selectionEnd === 'number' ? e.selectionEnd : -1
		});
	})()`
	result, err := cdpCall(debugPort, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true,
	})
	if err != nil {
		return ""
	}
	value, ok := cdpRuntimeValue(result)
	if !ok {
		return ""
	}
	state, _ := value.(string)
	return state
}

func (s *InputSyncer) applyFollowerFocusedEditableState(debugPort int, state string) {
	if state == "" {
		return
	}
	expression := `(() => {
		const s = JSON.parse(` + strconv.Quote(state) + `);
		const e = document.activeElement;
		if (!e || (e.tagName !== 'INPUT' && e.tagName !== 'TEXTAREA')) return false;
		if (String(e.value ?? '') !== s.value) {
			const proto = e.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
			const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
			if (setter) setter.call(e, s.value); else e.value = s.value;
			e.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: null }));
		}
		if (s.start >= 0 && typeof e.setSelectionRange === 'function') {
			try { e.setSelectionRange(s.start, s.end); } catch (_) {}
		}
		return true;
	})()`
	_, _ = cdpCall(debugPort, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true,
	})
}

func (s *InputSyncer) navigateFollower(debugPort int, url string) {
	_, _ = cdpCall(debugPort, "Page.navigate", map[string]any{
		"url": url,
	})
}

func isAboutBlank(url string) bool {
	return url == "about:blank" || url == "about:blank#" || url == ""
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
