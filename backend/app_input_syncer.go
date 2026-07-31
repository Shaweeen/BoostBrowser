//go:build windows

package backend

import (
	"fmt"
	"math"
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
	active             int32  // 1=活跃, 0=停止
	paused             int32  // 1=Esc 静默暂停，Hook 保持安装以便再次按 Esc 恢复
	escapeDown         int32  // 防止系统按键重复触发多次切换
	dispatchGeneration uint64 // 暂停/恢复后，切换前排队的动作永久失效
	mouseEnabled       int32  // 1=启用, 0=禁用
	keyEnabled         int32  // 1=启用, 0=禁用
	randomDelayEnabled int32
	randomDelayMinMs   int32
	randomDelayMaxMs   int32
	randomDelayMu      sync.Mutex
	randomDelayNext    map[windows.HWND]time.Time
	cdpPortLocks       sync.Map
	layoutUpdating     int32
	popupUpdating      int32

	mouseHook    uintptr
	keyHook      uintptr
	stopCh       chan struct{}
	stopOnce     sync.Once
	hookThreadID uint32
	workerWG     sync.WaitGroup

	lastMoveTime          int64 // Unix nano
	lastMoveScreenX       int32 // last throttled/observed move, flushed before click
	lastMoveScreenY       int32
	pendingMoveFlush      int32 // 1 when a throttled move still needs delivery
	pageKeyboardFocus     int32 // last master click was inside the renderer
	pointerInsideMaster   int32 // pointer is inside master frame or an owned Chrome popup
	activePageMouseButton int32 // Win32 button-down message while dragging page content/scrollbars
	cdpKeyQueue           chan cdpKeyEvent
	cdpKeyDrops           int32
	pageInputQueue        chan pageInputEvent
	pageInputDrops        int32

	// URL 同步
	urlStopCh                chan struct{}
	lastSyncURL              string
	lastFocusedEditableState string
	// After Esc pause/resume, re-baseline master URL/editable without pushing to
	// followers so resume only restores input sync — never reloads pages.
	urlSyncReseed int32

	// Short-lived master page target cache. Focused-target resolution used to
	// open a WebSocket per tab on every mouse event; that cost dominates with
	// 10+ followers and multi-tab profiles. Cache only metadata, never page content.
	cachedMasterPort      int
	cachedMasterTarget    cdpTarget
	cachedMasterTargetExp time.Time

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
	pauseChanged    func(paused bool)
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
	generation uint64
	masterPort int
	ports      []int
	hwnds      []windows.HWND
	down       bool
	vk         uint32
	character  rune
	modifiers  int
	imeActive  bool // when true, never Input.insertText — preserve composition
}

type pageInputKind uint8

const (
	pageInputCritical pageInputKind = iota + 1
	pageInputMove
)

type pageInputEvent struct {
	generation uint64
	kind       pageInputKind
	action     func()
}

const focusedMasterCDPTargetCacheTTL = 120 * time.Millisecond

func pageCDPTargets(debugPort int) []cdpTarget {
	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return nil
	}
	pages := make([]cdpTarget, 0, len(targets))
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(target.Type), "page") && strings.TrimSpace(target.WebSocketDebuggerUrl) != "" {
			pages = append(pages, target)
		}
	}
	return pages
}

func focusedCDPTarget(debugPort int) (cdpTarget, bool) {
	pages := pageCDPTargets(debugPort)
	if len(pages) == 0 {
		return cdpTarget{}, false
	}
	// Common case: one tab → no WebSocket round-trip. Multi-tab profiles still
	// pay for hasFocus so clicks land on the focused document, not the first
	// /json entry.
	if len(pages) == 1 {
		return pages[0], true
	}
	// Wallet popups/unlock pages often sit alongside the main tab. Prefer the
	// document that actually has OS/DOM focus, then one with an active editable
	// (password/auth input), and only fall back to the first /json page.
	const focusProbe = `(() => {
		const e = document.activeElement;
		const editable = !!(e && (
			e.tagName === 'INPUT' || e.tagName === 'TEXTAREA' ||
			(typeof e.isContentEditable === 'boolean' && e.isContentEditable)
		));
		return {focused: !!document.hasFocus(), editable: editable};
	})()`
	var focusedAny, editableAny cdpTarget
	haveFocused, haveEditable := false, false
	for _, target := range pages {
		result, err := cdpCallTarget(target, "Runtime.evaluate", map[string]any{
			"expression": focusProbe, "returnByValue": true,
		})
		if err != nil {
			continue
		}
		value, ok := cdpRuntimeValue(result)
		if !ok {
			continue
		}
		info, _ := value.(map[string]any)
		if info == nil {
			// Some CDP stacks return nested objects; tolerate bool-only legacy.
			if focused, isBool := value.(bool); isBool && focused && !haveFocused {
				focusedAny = target
				haveFocused = true
			}
			continue
		}
		focused, _ := info["focused"].(bool)
		editable, _ := info["editable"].(bool)
		if focused && editable {
			return target, true
		}
		if focused && !haveFocused {
			focusedAny = target
			haveFocused = true
		}
		if editable && !haveEditable {
			editableAny = target
			haveEditable = true
		}
	}
	if haveFocused {
		return focusedAny, true
	}
	if haveEditable {
		return editableAny, true
	}
	// Prefer an open wallet/extension surface over a random first tab when focus
	// probes all fail (common while the popup is still attaching).
	for _, target := range pages {
		if extensionLikeCDPTarget(target) {
			return target, true
		}
	}
	return pages[0], true
}

// focusedMasterCDPTarget returns the master page target with a short metadata
// cache so move/click storms do not re-open CDP sockets for every event.
func (s *InputSyncer) focusedMasterCDPTarget(debugPort int) (cdpTarget, bool) {
	if s == nil || debugPort <= 0 {
		return cdpTarget{}, false
	}
	now := time.Now()
	s.mu.Lock()
	if s.cachedMasterPort == debugPort && now.Before(s.cachedMasterTargetExp) && strings.TrimSpace(s.cachedMasterTarget.ID) != "" {
		target := s.cachedMasterTarget
		s.mu.Unlock()
		return target, true
	}
	s.mu.Unlock()

	target, ok := focusedCDPTarget(debugPort)
	if !ok {
		return cdpTarget{}, false
	}
	s.mu.Lock()
	s.cachedMasterPort = debugPort
	s.cachedMasterTarget = target
	s.cachedMasterTargetExp = now.Add(focusedMasterCDPTargetCacheTTL)
	s.mu.Unlock()
	return target, true
}

func (s *InputSyncer) invalidateMasterCDPTargetCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cachedMasterPort = 0
	s.cachedMasterTarget = cdpTarget{}
	s.cachedMasterTargetExp = time.Time{}
	s.mu.Unlock()
}

func cdpTargetURLWithoutFragment(raw string) string {
	raw = strings.TrimSpace(raw)
	if index := strings.IndexByte(raw, '#'); index >= 0 {
		raw = raw[:index]
	}
	return raw
}

func cdpTargetURLDocument(raw string) string {
	raw = cdpTargetURLWithoutFragment(raw)
	if index := strings.IndexByte(raw, '?'); index >= 0 {
		raw = raw[:index]
	}
	return raw
}

func chromeExtensionID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(raw, prefix) {
		return ""
	}
	rest := raw[len(prefix):]
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.IndexByte(rest, '?'); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.IndexByte(rest, '#'); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

func syncCDPTargetMatchScore(master, candidate cdpTarget) int {
	if !strings.EqualFold(strings.TrimSpace(master.Type), strings.TrimSpace(candidate.Type)) {
		return -1
	}
	score := 0
	masterURL := strings.TrimSpace(master.URL)
	candidateURL := strings.TrimSpace(candidate.URL)
	if masterURL != "" && masterURL == candidateURL {
		score += 10000
	} else if masterURL != "" && cdpTargetURLWithoutFragment(masterURL) == cdpTargetURLWithoutFragment(candidateURL) {
		score += 6000
	} else if masterURL != "" && cdpTargetURLDocument(masterURL) == cdpTargetURLDocument(candidateURL) {
		score += 4000
	}
	// Same extension ID (MetaMask/Rabby unlock vs popup hash routes) still maps
	// password/auth surfaces across followers even when the path fragment differs.
	if masterID := chromeExtensionID(masterURL); masterID != "" {
		if candidateID := chromeExtensionID(candidateURL); candidateID == masterID {
			score += 2500
		}
	}
	if master.Title != "" && strings.EqualFold(strings.TrimSpace(master.Title), strings.TrimSpace(candidate.Title)) {
		score += 1000
	}
	return score
}

// extensionLikeCDPTarget is any top-level chrome-extension document. Wallet
// unlock/home/onboarding pages are full extension documents, not only popup.html.
func extensionLikeCDPTarget(target cdpTarget) bool {
	url := strings.ToLower(strings.TrimSpace(target.URL))
	return strings.HasPrefix(url, "chrome-extension://")
}

func popupLikeCDPTarget(target cdpTarget) bool {
	if strings.TrimSpace(target.OpenerID) != "" {
		return true
	}
	if !extensionLikeCDPTarget(target) {
		return false
	}
	url := strings.ToLower(strings.TrimSpace(target.URL))
	// Wallet password/auth/number UIs live on popup, notification, unlock, home,
	// onboard, confirm, and sign routes — not only *popup* in the path.
	for _, token := range []string{
		"popup", "notification", "confirm", "unlock", "password",
		"onboard", "onboarding", "sign", "approve", "connect",
		"permission", "home.html", "index.html", "fullscreen",
	} {
		if strings.Contains(url, token) {
			return true
		}
	}
	// Any remaining chrome-extension page (unknown wallet) still needs follower
	// target matching rather than falling through to the first /json tab.
	return true
}

func matchingFollowerCDPTarget(master cdpTarget, debugPort int, waitForPopup bool) (cdpTarget, bool) {
	attempts := 1
	if waitForPopup {
		attempts = 5
	}
	for attempt := 0; attempt < attempts; attempt++ {
		pages := pageCDPTargets(debugPort)
		if len(pages) == 1 {
			if !popupLikeCDPTarget(master) || syncCDPTargetMatchScore(master, pages[0]) > 0 {
				return pages[0], true
			}
		}
		bestScore := -1
		var best cdpTarget
		for _, candidate := range pages {
			score := syncCDPTargetMatchScore(master, candidate)
			if score > bestScore {
				bestScore = score
				best = candidate
			}
		}
		if bestScore > 0 {
			return best, true
		}
		if attempt+1 < attempts {
			time.Sleep(40 * time.Millisecond)
		}
	}
	return cdpTarget{}, false
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
		pageInputQueue:  make(chan pageInputEvent, 512),
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
	atomic.StoreInt32(&s.paused, 0)
	atomic.StoreInt32(&s.escapeDown, 0)
	atomic.StoreUint64(&s.dispatchGeneration, 0)
	atomic.StoreInt32(&s.layoutUpdating, 0)
	atomic.StoreInt32(&s.popupUpdating, 0)
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
	s.pageInputQueue = make(chan pageInputEvent, 512)
	s.randomDelayMu.Lock()
	s.randomDelayNext = make(map[windows.HWND]time.Time)
	s.randomDelayMu.Unlock()
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
	s.runWorker(func() { s.cdpKeyDispatchLoop(s.stopCh, s.cdpKeyQueue) })
	s.runWorker(func() { s.pageInputDispatchLoop(s.stopCh, s.pageInputQueue) })
	// Popup geometry is owned solely by the main-process environmentPopupConfiner
	// (position-only). InputSyncer does not run a parallel SetWindowPos loop —
	// dual writers caused wallet flicker under multi-open + sync.

	// 安装全局鼠标和键盘钩子。启动必须等待安装结果；旧逻辑在安装
	// 失败时仍立即返回成功，前端因此会显示“同步中”但没有任何事件。
	s.runWorker(func() {
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
	})

	select {
	case err := <-ready:
		if err != nil {
			activeInputSyncer.CompareAndSwap(s, nil)
			atomic.StoreInt32(&s.active, 0)
			s.signalStop()
			s.Stop()
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
		s.Stop()
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

	// Capture an immutable stop channel for this worker. Reading the mutable
	// field from the loop raced with Stop clearing it and could delay shutdown.
	urlStopCh := make(chan struct{})
	s.mu.Lock()
	s.urlStopCh = urlStopCh
	s.mu.Unlock()
	s.runWorker(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.New("InputSyncer").Error("urlSyncLoop goroutine panic recovered",
					logger.F("error", r),
				)
			}
		}()
		s.urlSyncLoop(urlStopCh)
	})

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
	hadRuntime := atomic.LoadInt32(&s.active) != 0 ||
		s.masterHwnd != 0 || len(s.followerHwnds) > 0 ||
		s.mouseHook != 0 || s.keyHook != 0 || s.hookThreadID != 0
	if !hadRuntime {
		s.mu.Unlock()
		return
	}

	syncLog("=== InputSyncer Stop (clicks=%d, moves=%d, wheels=%d, keys=%d) ===",
		atomic.LoadInt32(&s.clickCount), atomic.LoadInt32(&s.moveCount),
		atomic.LoadInt32(&s.wheelCount), atomic.LoadInt32(&s.keyCount))

	atomic.StoreInt32(&s.active, 0)
	atomic.AddUint64(&s.dispatchGeneration, 1)
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

	// Detach native resources while holding the state lock, then release the
	// lock before waiting: worker shutdown paths also take s.mu.
	mouseHook := s.mouseHook
	keyHook := s.keyHook
	hookThreadID := s.hookThreadID
	s.mouseHook = 0
	s.keyHook = 0
	s.hookThreadID = 0
	s.mu.Unlock()

	if mouseHook != 0 {
		procUnhookWindowsHookEx := user32dll.NewProc("UnhookWindowsHookEx")
		procUnhookWindowsHookEx.Call(uintptr(mouseHook))
	}
	if keyHook != 0 {
		procUnhookWindowsHookEx := user32dll.NewProc("UnhookWindowsHookEx")
		procUnhookWindowsHookEx.Call(uintptr(keyHook))
	}
	if hookThreadID != 0 {
		procPostThreadMessageW := user32dll.NewProc("PostThreadMessageW")
		procPostThreadMessageW.Call(uintptr(hookThreadID), 0x0012, 0, 0) // WM_QUIT
	}

	// Stop does not return until URL polling, popup confinement, input queues,
	// and the native hook loop have all exited. This makes Stop a hard session
	// boundary before the UI is allowed to refresh and collect new windows.
	s.workerWG.Wait()
	s.clearRuntimeState()

	log := logger.New("InputSyncer")
	log.Info("输入同步已停止")
	s.lifecycle("sync-input-stop", fmt.Sprintf("clicks=%d", atomic.LoadInt32(&s.clickCount)), fmt.Sprintf("moves=%d", atomic.LoadInt32(&s.moveCount)), fmt.Sprintf("wheels=%d", atomic.LoadInt32(&s.wheelCount)), fmt.Sprintf("keys=%d", atomic.LoadInt32(&s.keyCount)))
}

func (s *InputSyncer) runWorker(worker func()) {
	s.workerWG.Add(1)
	go func() {
		defer s.workerWG.Done()
		worker()
	}()
}

func (s *InputSyncer) clearRuntimeState() {
	s.mu.Lock()
	s.masterHwnd = 0
	s.followerHwnds = nil
	s.masterPid = 0
	s.masterDebug = 0
	s.followerDebug = nil
	s.cdpKeyQueue = nil
	s.pageInputQueue = nil
	s.lastSyncURL = ""
	s.lastFocusedEditableState = ""
	s.cachedMasterPort = 0
	s.cachedMasterTarget = cdpTarget{}
	s.cachedMasterTargetExp = time.Time{}
	s.mu.Unlock()

	s.followerMu.Lock()
	s.followerSnapshot = nil
	s.followerMu.Unlock()

	s.randomDelayMu.Lock()
	clear(s.randomDelayNext)
	s.randomDelayMu.Unlock()

	atomic.StoreInt32(&s.paused, 0)
	atomic.StoreInt32(&s.urlSyncReseed, 0)
	atomic.StoreInt32(&s.escapeDown, 0)
	atomic.StoreInt32(&s.mouseEnabled, 0)
	atomic.StoreInt32(&s.keyEnabled, 0)
	atomic.StoreInt32(&s.randomDelayEnabled, 0)
	atomic.StoreInt32(&s.randomDelayMinMs, 0)
	atomic.StoreInt32(&s.randomDelayMaxMs, 0)
	atomic.StoreInt32(&s.pageKeyboardFocus, 0)
	atomic.StoreInt32(&s.pointerInsideMaster, 0)
	atomic.StoreInt32(&s.activePageMouseButton, 0)
	atomic.StoreInt32(&s.pendingMoveFlush, 0)
	atomic.StoreInt32(&s.lastMoveScreenX, 0)
	atomic.StoreInt32(&s.lastMoveScreenY, 0)
	atomic.StoreInt32(&s.layoutUpdating, 0)
	atomic.StoreInt32(&s.popupUpdating, 0)
}

func (s *InputSyncer) signalStop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// IsActive 返回同步是否活跃
func (s *InputSyncer) IsActive() bool {
	return atomic.LoadInt32(&s.active) == 1
}

// IsPaused reports the lightweight Esc pause state. Pausing keeps the hooks,
// validated windows and CDP sessions alive, so resume is immediate and does
// not reorder or rediscover 20+ follower windows.
func (s *InputSyncer) IsPaused() bool {
	return atomic.LoadInt32(&s.paused) == 1
}

func (s *InputSyncer) SetPauseChangedHandler(handler func(paused bool)) {
	s.pauseChanged = handler
}

func (s *InputSyncer) togglePausedFromEscape() bool {
	for {
		old := atomic.LoadInt32(&s.paused)
		next := int32(1)
		if old == 1 {
			next = 0
		}
		if !atomic.CompareAndSwapInt32(&s.paused, old, next) {
			continue
		}
		paused := next == 1
		atomic.AddUint64(&s.dispatchGeneration, 1)
		if paused {
			// Pause only freezes input dispatch. Drop any in-flight drag and
			// forget URL/editable mirrors so they cannot apply mid-pause.
			atomic.StoreInt32(&s.activePageMouseButton, 0)
			s.mu.Lock()
			s.lastSyncURL = ""
			s.lastFocusedEditableState = ""
			s.mu.Unlock()
			s.invalidateMasterCDPTargetCache()
		} else {
			// Resume input sync only. Re-baseline master URL/editable on the next
			// urlSyncLoop tick without Page.navigate / value push — followers keep
			// whatever page they had while the user was paused.
			s.invalidateMasterCDPTargetCache()
			s.mu.Lock()
			s.lastSyncURL = ""
			s.lastFocusedEditableState = ""
			s.mu.Unlock()
			atomic.StoreInt32(&s.urlSyncReseed, 1)
		}
		handler := s.pauseChanged
		// Never write logs or call Wails/UI work on the low-level hook thread.
		s.runWorker(func() {
			s.lifecycle("sync-input-pause", fmt.Sprintf("paused=%t", paused), "source=escape")
			if handler != nil {
				handler(paused)
			}
		})
		return paused
	}
}

func (s *InputSyncer) canDispatch() bool {
	// popupUpdating must not block input: geometry work is rare and was
	// freezing key/mouse for the whole multi-follower align pass.
	return atomic.LoadInt32(&s.active) == 1 &&
		atomic.LoadInt32(&s.paused) == 0 &&
		atomic.LoadInt32(&s.layoutUpdating) == 0
}

// BeginLayoutUpdate creates a hard boundary between window movement and input
// coordinate mapping. Events queued against the previous geometry are dropped.
// Confiner hold is owned by syncTileWindowsLocal (not nested here) so End does
// not release the hold mid-tile.
func (s *InputSyncer) BeginLayoutUpdate() {
	atomic.StoreInt32(&s.layoutUpdating, 1)
	atomic.AddUint64(&s.dispatchGeneration, 1)
	atomic.StoreInt32(&s.activePageMouseButton, 0)
}

func (s *InputSyncer) EndLayoutUpdate() {
	atomic.AddUint64(&s.dispatchGeneration, 1)
	atomic.StoreInt32(&s.layoutUpdating, 0)
}

// holdPopupConfinementForLayout stops the main confiner during any tile/stack
// from main or panel. Panel has no confiner but still writes the shared flag.
func holdPopupConfinementForLayout(hold bool) {
	if app := environmentPopupApp.Load(); app != nil {
		app.holdEnvironmentPopupConfinement(hold)
		return
	}
	setSharedLayoutHold(hold)
}

func (s *InputSyncer) withCDPPortLock(debugPort int, action func()) {
	if action == nil {
		return
	}
	if debugPort <= 0 {
		if s.canDispatch() {
			action()
		}
		return
	}
	value, _ := s.cdpPortLocks.LoadOrStore(debugPort, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if s.canDispatch() {
		action()
	}
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
	s.dispatchWithRandomDelayComplete(hwnd, action, nil)
}

func (s *InputSyncer) dispatchWithRandomDelayComplete(hwnd windows.HWND, action func(), complete func()) {
	if action == nil || !s.canDispatch() {
		if complete != nil {
			complete()
		}
		return
	}
	generation := atomic.LoadUint64(&s.dispatchGeneration)
	guarded := func() {
		if complete != nil {
			defer complete()
		}
		if s.canDispatch() && generation == atomic.LoadUint64(&s.dispatchGeneration) {
			action()
		}
	}
	if atomic.LoadInt32(&s.randomDelayEnabled) == 0 {
		guarded()
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
	time.AfterFunc(time.Until(due), guarded)
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

func syncMouseMoveThrottle(followerCount int) time.Duration {
	switch {
	case followerCount >= 20:
		return 28 * time.Millisecond
	case followerCount >= 10:
		return 20 * time.Millisecond
	case followerCount >= 6:
		return 14 * time.Millisecond
	case followerCount >= 3:
		return 10 * time.Millisecond
	default:
		return 6 * time.Millisecond
	}
}

// Drag/select paths need denser samples than idle hover so scrollbars and text
// selection stay aligned across followers.
func syncMouseDragThrottle(followerCount int) time.Duration {
	switch {
	case followerCount >= 20:
		return 12 * time.Millisecond
	case followerCount >= 10:
		return 10 * time.Millisecond
	case followerCount >= 6:
		return 8 * time.Millisecond
	default:
		return 4 * time.Millisecond
	}
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
	title     string
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
	title := getWindowTitle(hwnd)
	if isAuxiliaryIMEWindowTitleOrClass(title, className) {
		return 1
	}
	// Chrome-Manager accepts any Chrome_WidgetWin_* for wallet/popup surfaces;
	// requiring exact class equality missed some notification hosts.
	lowerClass := strings.ToLower(className)
	if !strings.HasPrefix(lowerClass, "chrome_widgetwin_") && !strings.EqualFold(className, "Chrome_MainWindow") {
		return 1
	}
	left, top, right, bottom := getWindowRect(hwnd)
	w, h := int(right-left), int(bottom-top)
	if w <= 8 || h <= 8 || w > 10000 || h > 10000 {
		return 1
	}
	score := popupSurfaceMatchScore(search.master, syncInputSurfaceCandidate{
		hwnd: hwnd, className: className, title: title,
		left: int(left), top: int(top), width: w, height: h,
	}, search.expectedLeft, search.expectedTop)
	if search.best == 0 || score < search.bestScore {
		search.best = hwnd
		search.bestScore = score
	}
	return 1
})

func popupSurfaceMatchScore(master, candidate syncInputSurfaceCandidate, expectedLeft, expectedTop int) int64 {
	// Chrome-Manager Clean: size + title Jaccard + relative offset to parent.
	return chromeManagerPopupMatchScore(
		master.width, master.height,
		candidate.width, candidate.height,
		expectedLeft, expectedTop,
		candidate.left, candidate.top,
		master.title, candidate.title,
	)
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
	// Prefer CM relative offset (popup - main) on the follower; keep edge-anchor
	// as a secondary expected X when the popup is right-aligned in the tile.
	offsetLeft := expectedPopupOffsetLeft(int(ml), int(mml), int(fl))
	edgeLeft := expectedPopupSurfaceLeft(int(ml), int(mr), int(mml), int(mmr), int(fl), int(fr))
	expectedLeft := offsetLeft
	// If edge-anchor is closer to a typical wallet dock, blend by choosing the
	// candidate search expected as CM offset (primary) — enum score also uses it.
	_ = edgeLeft
	master := syncInputSurfaceCandidate{
		hwnd: masterSurface, className: getWindowClassName(masterSurface),
		title: getWindowTitle(masterSurface),
		left:  int(ml), top: int(mt), width: int(mr - ml), height: int(mb - mt),
	}
	search := &syncInputSurfaceSearch{
		pid: windowPID(followerMain), mainHwnd: followerMain, master: master,
		expectedLeft: expectedLeft,
		expectedTop:  expectedPopupOffsetTop(int(mt), int(mmt), int(ft)),
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
	// Chrome-Manager primary path: outer GetWindowRect proportions → client lParam.
	if lparam, ok := mapCoordsOuterWindowRects(screenX, screenY, masterSurface, followerSurface); ok {
		return lparam, true
	}
	// Client-area fallback for unusual DPI/chrome frames.
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

// mapCoordsOuterWindowRects implements Chrome-Manager's outer-rect calibration:
// rel = (screen - masterOuter) / masterOuterSize; client = rel * followerOuterSize.
func mapCoordsOuterWindowRects(screenX, screenY int, masterSurface, followerSurface windows.HWND) (uintptr, bool) {
	ml, mt, mr, mb := getWindowRect(masterSurface)
	fl, ft, fr, fb := getWindowRect(followerSurface)
	fW, fH := int(fr-fl), int(fb-ft)
	cx, cy, ok := chromeManagerMapPoint(screenX, screenY, int(ml), int(mt), int(mr), int(mb), fW, fH)
	if !ok {
		return 0, false
	}
	// PostMessage mouse messages expect client coords of the target HWND.
	// Outer proportions approximate client for similarly-chromed Chrome windows
	// (same technique as Chrome-Manager). Clamp into follower client if available.
	if fw, fh, cok := getClientSize(followerSurface); cok && fw > 0 && fh > 0 {
		if cx > fw {
			cx = fw
		}
		if cy > fh {
			cy = fh
		}
	}
	return MAKELONG(uint16(int16(cx)), uint16(int16(cy))), true
}

func mapScreenPointBetweenInputSurfaces(screenX, screenY int, masterSurface, followerSurface windows.HWND) (int, int, bool) {
	// Outer-rect proportion → follower screen point (for wheel messages).
	ml, mt, mr, mb := getWindowRect(masterSurface)
	fl, ft, fr, fb := getWindowRect(followerSurface)
	fW, fH := int(fr-fl), int(fb-ft)
	cx, cy, ok := chromeManagerMapPoint(screenX, screenY, int(ml), int(mt), int(mr), int(mb), fW, fH)
	if ok {
		return int(fl) + cx, int(ft) + cy, true
	}
	mx, my := screenToClient(masterSurface, screenX, screenY)
	mw, mh, ok := getClientSize(masterSurface)
	if !ok || mw <= 0 || mh <= 0 || mx < 0 || my < 0 || mx > mw || my > mh {
		return 0, 0, false
	}
	fw, fh, ok := getClientSize(followerSurface)
	if !ok || fw <= 0 || fh <= 0 {
		return 0, 0, false
	}
	fx := int(float64(mx) / float64(mw) * float64(fw))
	fy := int(float64(my) / float64(mh) * float64(fh))
	left, top, _, _ := getWindowRect(followerSurface)
	return int(left) + fx, int(top) + fy, true
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

// listChromePopupSurfaces / chromePopupListSearch remain available for diagnostics
// and tests. Continuous relative-align SetWindowPos was removed: the main
// environmentPopupConfiner is the sole geometry writer (position-only).

type chromePopupListSearch struct {
	main      windows.HWND
	pid       uint32
	treeRoots map[int]int
	out       []windows.HWND
}

// chromePopupListEnumCallback is process-global (Win32 callback table is finite).
var chromePopupListEnumCallback = windows.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
	defer func() { _ = recover() }()
	st := (*chromePopupListSearch)(unsafe.Pointer(lParam))
	if st == nil || hwnd == st.main || !isWindowVisible(hwnd) {
		return 1
	}
	pid := windowPID(hwnd)
	if pid != st.pid {
		if st.treeRoots == nil {
			return 1
		}
		if root, ok := st.treeRoots[int(pid)]; !ok || root != int(st.pid) {
			return 1
		}
	}
	className := getWindowClassName(hwnd)
	title := getWindowTitle(hwnd)
	if isAuxiliaryIMEWindowTitleOrClass(title, className) {
		return 1
	}
	lower := strings.ToLower(className)
	if !strings.HasPrefix(lower, "chrome_widgetwin_") {
		return 1
	}
	l, t, r, b := getWindowRect(hwnd)
	w, h := int(r-l), int(b-t)
	// CM wallet-size heuristic: compact surfaces, not full browser frames.
	if w < 120 || h < 80 || w > 900 || h > 900 {
		return 1
	}
	ml, mt, mr, mb := getWindowRect(st.main)
	// Near main frame (CM is_near_chrome ±100).
	if int(l) < int(ml)-120 || int(t) < int(mt)-120 || int(r) > int(mr)+120 || int(b) > int(mb)+120 {
		if !looksLikeWalletOrExtensionPopupTitle(title) {
			return 1
		}
	}
	st.out = append(st.out, hwnd)
	return 1
})

// listChromePopupSurfaces enumerates Chrome_WidgetWin_* top-level surfaces that
// belong to the same process tree as mainHwnd and are not the main frame itself
// (Chrome-Manager get_chrome_popups).
func listChromePopupSurfaces(mainHwnd windows.HWND) []windows.HWND {
	if mainHwnd == 0 {
		return nil
	}
	state := &chromePopupListSearch{
		main:      mainHwnd,
		pid:       windowPID(mainHwnd),
		treeRoots: mapProcessTreeRoots([]int{int(windowPID(mainHwnd))}),
	}
	procEnumWindows.Call(chromePopupListEnumCallback, uintptr(unsafe.Pointer(state)))
	runtime.KeepAlive(state)
	return state.out
}

func looksLikeWalletOrExtensionPopupTitle(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return true // empty-title Chrome menus (CM treats as popup candidates)
	}
	for _, kw := range []string{
		"metamask", "rabby", "okx", "wallet", "钱包", "notification",
		"extension", "扩展", "sign", "confirm", "connect", "permission",
	} {
		if strings.Contains(t, kw) {
			return true
		}
	}
	return false
}

func sendChromeUIMouseMessage(hwnd windows.HWND, msg, wparam, lparam uintptr, _ bool) {
	// Never synchronously wait for a Chrome surface from a low-level hook.
	// PostMessage preserves per-window ordering (hover before click) without
	// allowing one hung popup to stall every follower or make Windows remove
	// the hook for exceeding its callback timeout.
	procPostMessageW.Call(uintptr(hwnd), msg, wparam, lparam)
}

// masterActiveInputSurface returns the Chrome surface currently receiving input
// on the master (main frame or wallet/popup), matching Chrome-Manager's use of
// GetForegroundWindow + popup list before replaying events.
func masterActiveInputSurface(masterMain windows.HWND) windows.HWND {
	if masterMain == 0 {
		return 0
	}
	fg, _, _ := procGetForegroundWindow.Call()
	if fg != 0 {
		root := getAncestor(windows.HWND(fg), GA_ROOT)
		if root == 0 {
			root = windows.HWND(fg)
		}
		if windowPID(root) == windowPID(masterMain) {
			className := strings.ToLower(getWindowClassName(root))
			if strings.HasPrefix(className, "chrome_widgetwin_") || strings.EqualFold(getWindowClassName(root), "Chrome_MainWindow") {
				return root
			}
		}
	}
	if x, y, ok := currentCursorPosition(); ok {
		return chromeInputSurfaceAtPoint(masterMain, x, y)
	}
	return masterMain
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

	fW, fH, ok := getClientSize(followerHwnd)
	if !ok || fW <= 0 || fH <= 0 || fW > 10000 || fH > 10000 {
		return 0, false
	}

	var clientX, clientY int
	// Uniform tiles / equal client sizes: 1:1 absolute pixels for scrollbar,
	// IME caret, and in-page hit targets (including extension popups mapped
	// through the main surface).
	if absCalibInt(mW-fW) <= sameSizePixelTolerance && absCalibInt(mH-fH) <= sameSizePixelTolerance {
		clientX, clientY = mClientX, mClientY
	} else {
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
		clientX = int(math.Round(float64(fW) * relX))
		clientY = int(math.Round(float64(fH) * relY))
	}
	if clientX < 0 {
		clientX = 0
	}
	if clientY < 0 {
		clientY = 0
	}
	if clientX > fW {
		clientX = fW
	}
	if clientY > fH {
		clientY = fH
	}
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
	if !ok || mW <= 0 || mH <= 0 {
		return 0, 0, false
	}
	// Scrollbars and trackpad gestures can land a few pixels outside the strict
	// client rect under DPI scaling; clamp instead of dropping the event.
	if mClientX < 0 {
		mClientX = 0
	}
	if mClientY < 0 {
		mClientY = 0
	}
	if mClientX > mW {
		mClientX = mW
	}
	if mClientY > mH {
		mClientY = mH
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

func chromePageSurfaceAtPoint(mainHwnd windows.HWND, screenX, screenY int) (windows.HWND, windows.HWND, bool) {
	surface := chromeInputSurfaceAtPoint(mainHwnd, screenX, screenY)
	render := findChromeRenderChild(surface)
	if render == 0 || !pointInsideWindow(render, screenX, screenY) {
		return surface, 0, false
	}
	return surface, render, true
}

func pointInsideChromeRender(hwnd windows.HWND, screenX, screenY int) bool {
	_, _, inside := chromePageSurfaceAtPoint(hwnd, screenX, screenY)
	return inside
}

func followerRenderForMasterSurface(masterSurface, masterMain, followerMain windows.HWND) (windows.HWND, windows.HWND, bool) {
	followerSurface := followerMain
	if masterSurface != masterMain {
		followerSurface = findMatchingChromeInputSurface(masterSurface, masterMain, followerMain)
		if followerSurface == followerMain {
			return 0, 0, false
		}
	}
	render := findChromeRenderChild(followerSurface)
	return followerSurface, render, render != 0
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

	// Do not require isMasterForeground(): the always-on-top sync panel often
	// keeps focus while the user clicks the master browser, which previously
	// dropped every mouse event and made sync appear "started but dead".
	if nCode < 0 || !s.canDispatch() || atomic.LoadInt32(&s.mouseEnabled) == 0 {
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
		// Focus can move from main tab → wallet popup (or between extension
		// routes). Drop the short-lived master CDP target cache so the next
		// key/mouse event re-resolves the focused document.
		s.invalidateMasterCDPTargetCache()
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
		// Deliver any throttled hover sample first so the click lands where the
		// master pointer actually is, not at the last downsampled position.
		s.flushPendingMouseMove(followers, screenX, screenY)
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
		s.flushPendingMouseMove(followers, screenX, screenY)
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
		// Page content: CDP wheel with modifiers is the reliable path for
		// Ctrl+zoom and Shift+horizontal scroll. Chrome UI chrome (tab strip,
		// bookmarks bar) still uses mapped Win32 wheel messages.
		if pointInsideChromeRender(s.masterHwnd, screenX, screenY) {
			s.dispatchPageWheelViaCDP(msg, screenX, screenY, int16(wheelDelta), keyState)
			return callNextHook(nCode, wParam, lParam)
		}
		masterSurface := chromeInputSurfaceAtPoint(s.masterHwnd, screenX, screenY)
		for _, hwnd := range followers {
			if !isWindow(hwnd) {
				continue
			}
			followerSurface := hwnd
			if masterSurface != s.masterHwnd {
				followerSurface = findMatchingChromeInputSurface(masterSurface, s.masterHwnd, hwnd)
				if followerSurface == hwnd {
					continue
				}
			}
			targetX, targetY, ok := mapScreenPointBetweenInputSurfaces(screenX, screenY, masterSurface, followerSurface)
			if !ok || targetX < -32768 || targetX > 32767 || targetY < -32768 || targetY > 32767 {
				continue
			}
			wheelLParam := MAKELONG(uint16(int16(targetX)), uint16(int16(targetY)))
			s.dispatchWithRandomDelay(followerSurface, func() {
				procPostMessageW.Call(uintptr(followerSurface), uintptr(msg), wheelWParam, wheelLParam)
			})
		}

	case WM_MOUSEMOVE:
		atomic.AddInt32(&s.moveCount, 1)
		// Chrome-Manager: time throttle + pixel threshold so multi-open stays smooth.
		// 跟随窗口越多，鼠标移动同步越容易把整机拖卡。
		// 拖拽/选区/滚动条拖动使用更密的采样。
		buttonDown := uint32(atomic.LoadInt32(&s.activePageMouseButton))
		throttle := syncMouseMoveThrottle(len(followers))
		if buttonDown != 0 {
			throttle = syncMouseDragThrottle(len(followers))
		}
		now := time.Now().UnixNano()
		last := atomic.LoadInt64(&s.lastMoveTime)
		lastX := int(atomic.LoadInt32(&s.lastMoveScreenX))
		lastY := int(atomic.LoadInt32(&s.lastMoveScreenY))
		if now-last < int64(throttle) {
			atomic.StoreInt32(&s.lastMoveScreenX, int32(screenX))
			atomic.StoreInt32(&s.lastMoveScreenY, int32(screenY))
			atomic.StoreInt32(&s.pendingMoveFlush, 1)
			return callNextHook(nCode, wParam, lParam)
		}
		// CM mouse_threshold=2: drop micro-jitter between time samples.
		if buttonDown == 0 && last != 0 &&
			shouldThrottleMouseMoveByDistance(lastX, lastY, screenX, screenY, mouseMovePixelThreshold) {
			return callNextHook(nCode, wParam, lParam)
		}
		atomic.StoreInt64(&s.lastMoveTime, now)
		atomic.StoreInt32(&s.lastMoveScreenX, int32(screenX))
		atomic.StoreInt32(&s.lastMoveScreenY, int32(screenY))
		atomic.StoreInt32(&s.pendingMoveFlush, 0)
		if buttonDown != 0 {
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

	if nCode < 0 || atomic.LoadInt32(&s.active) == 0 {
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

	// Esc is a process-global pause/resume shortcut only while a synchronization
	// session exists. Swallow both edges so it cannot also close a Chrome popup.
	if vk == VK_ESCAPE {
		if msg == WM_KEYDOWN || msg == WM_SYSKEYDOWN {
			if atomic.CompareAndSwapInt32(&s.escapeDown, 0, 1) {
				s.togglePausedFromEscape()
			}
		} else {
			atomic.StoreInt32(&s.escapeDown, 0)
		}
		return 1
	}
	// Keyboard: allow when pointer is inside master, or master/owned chrome is FG.
	// Requiring both FG and pointer broke typing when the sync panel held focus.
	if !s.canDispatch() || atomic.LoadInt32(&s.keyEnabled) == 0 {
		return callNextHook(nCode, wParam, lParam)
	}
	if atomic.LoadInt32(&s.pointerInsideMaster) == 0 && !s.isMasterForeground() {
		return callNextHook(nCode, wParam, lParam)
	}

	// Never replay window/tab close chords to followers. Under multi-open sync
	// a single Ctrl+W / Ctrl+Shift+W / Alt+F4 on master would tear down every
	// follower tab or the whole environment window.
	ctrlPressed := isKeyDown(VK_CONTROL)
	altPressed := isKeyDown(VK_MENU)
	shiftPressed := isKeyDown(VK_SHIFT)
	if isBrowserWindowCloseChord(vk, ctrlPressed, altPressed, shiftPressed) {
		return callNextHook(nCode, wParam, lParam)
	}

	atomic.AddInt32(&s.keyCount, 1)
	// 获取跟随窗口快照
	followers := s.getFollowerSnapshot()

	// 检测修饰键状态
	pageFocused := atomic.LoadInt32(&s.pageKeyboardFocus) == 1
	if ctrlPressed && isBrowserZoomVirtualKey(vk) {
		if msg == WM_KEYDOWN || msg == WM_KEYUP {
			// Page zoom must use CDP key events; Win32 PostMessage often never
			// reaches the focused renderer when the master is zooming a webpage.
			if pageFocused {
				s.dispatchPageKeyViaCDP(msg, hook.VkCode, hook.ScanCode, hook.Flags)
			} else if msg == WM_KEYDOWN {
				shiftPressed := isKeyDown(VK_SHIFT)
				for _, hwnd := range followers {
					if isWindow(hwnd) {
						s.dispatchBrowserZoomShortcut(hwnd, vk, shiftPressed)
					}
				}
			}
		}
		return callNextHook(nCode, wParam, lParam)
	}

	if pageFocused {
		s.dispatchPageKeyViaCDP(msg, hook.VkCode, hook.ScanCode, hook.Flags)
		return callNextHook(nCode, wParam, lParam)
	}

	keyboardLayout := foregroundKeyboardLayout()
	imeActive := keyboardLayoutUsesIME(keyboardLayout)
	// Chrome-Manager: when focus is on a master popup (wallet/menu), keys go to
	// the matched follower popup surface — not the follower main frame.
	masterKeySurface := masterActiveInputSurface(s.masterHwnd)

	for _, hwnd := range followers {
		if !isWindow(hwnd) {
			continue
		}
		targetHwnd := chromeKeyboardTarget(hwnd)
		if masterKeySurface != 0 && masterKeySurface != s.masterHwnd {
			if match := findMatchingChromeInputSurface(masterKeySurface, s.masterHwnd, hwnd); match != 0 && match != hwnd {
				targetHwnd = match
			}
		}
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

func isBrowserZoomVirtualKey(vk uint32) bool {
	switch vk {
	case 0x30, // 0: reset zoom
		0x6B, 0x6D, // numpad add/subtract
		0xBB, 0xBD: // OEM plus/minus
		return true
	default:
		return false
	}
}

// isBrowserWindowCloseChord detects shortcuts that close a tab or the whole
// Chromium window. These must never be mirrored to follower environments.
func isBrowserWindowCloseChord(vk uint32, ctrl, alt, shift bool) bool {
	if alt && !ctrl && vk == VK_F4 { // Alt+F4
		return true
	}
	if !ctrl {
		return false
	}
	switch vk {
	case 0x57: // Ctrl+W / Ctrl+Shift+W — close tab / close window
		return true
	case VK_F4: // Ctrl+F4 — close tab
		return true
	}
	_ = shift
	return false
}

func (s *InputSyncer) dispatchBrowserZoomShortcut(hwnd windows.HWND, vk uint32, shiftPressed bool) {
	s.dispatchWithRandomDelay(hwnd, func() {
		procPostMessageW.Call(uintptr(hwnd), WM_KEYDOWN, VK_CONTROL, makeKeyLParam(VK_CONTROL, true))
		if shiftPressed {
			procPostMessageW.Call(uintptr(hwnd), WM_KEYDOWN, VK_SHIFT, makeKeyLParam(VK_SHIFT, true))
		}
		procPostMessageW.Call(uintptr(hwnd), WM_KEYDOWN, uintptr(vk), makeKeyLParam(vk, true))
		procPostMessageW.Call(uintptr(hwnd), WM_KEYUP, uintptr(vk), makeKeyLParam(vk, false))
		if shiftPressed {
			procPostMessageW.Call(uintptr(hwnd), WM_KEYUP, VK_SHIFT, makeKeyLParam(VK_SHIFT, false))
		}
		procPostMessageW.Call(uintptr(hwnd), WM_KEYUP, VK_CONTROL, makeKeyLParam(VK_CONTROL, false))
	})
}

func (s *InputSyncer) dispatchPageKeyViaCDP(msg uint32, vk, scanCode, flags uint32) {
	s.mu.Lock()
	masterPort := s.masterDebug
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
	imeActive := keyboardLayoutUsesIME(foregroundKeyboardLayout())
	ch := rune(0)
	// insertText is only safe for latin direct input. IME composition must keep
	// the original key sequence; urlSyncLoop mirrors the committed field value.
	if !imeActive {
		ch = toUnicode(uint16(vk), uint16(scanCode), (flags&0x01) != 0)
		// Wallet password/PIN fields are almost always digits and latin letters.
		// If ToUnicode fails (layout/IME edge cases, odd extended flags), still
		// recover top-row and numpad digits so follower displays update.
		if ch == 0 {
			ch = digitRuneFromVirtualKey(vk)
		}
	}
	event := cdpKeyEvent{
		generation: atomic.LoadUint64(&s.dispatchGeneration),
		masterPort: masterPort,
		ports:      ports,
		hwnds:      hwnds,
		down:       down,
		vk:         vk,
		character:  ch,
		modifiers:  modifiers,
		imeActive:  imeActive,
	}
	select {
	case s.cdpKeyQueue <- event:
	default:
		// Prefer keeping navigation/edit keys over dropping under load.
		if isSpecialKey(vk) || ctrl || alt {
			timer := time.NewTimer(6 * time.Millisecond)
			select {
			case s.cdpKeyQueue <- event:
			case <-timer.C:
				atomic.AddInt32(&s.cdpKeyDrops, 1)
			}
			timer.Stop()
		} else {
			atomic.AddInt32(&s.cdpKeyDrops, 1)
		}
	}
}

func (s *InputSyncer) cdpKeyDispatchLoop(stopCh <-chan struct{}, queue <-chan cdpKeyEvent) {
	for {
		select {
		case <-stopCh:
			return
		case event := <-queue:
			if !s.canDispatch() || event.generation != atomic.LoadUint64(&s.dispatchGeneration) {
				continue
			}
			masterTarget, hasMasterTarget := s.focusedMasterCDPTarget(event.masterPort)
			waitPopup := hasMasterTarget && waitForFollowerCDPMatch(masterTarget)
			var wg sync.WaitGroup
			for i, hwnd := range event.hwnds {
				port := 0
				if i < len(event.ports) {
					port = event.ports[i]
				}
				wg.Add(1)
				go func(debugPort int, follower windows.HWND) {
					s.dispatchWithRandomDelayComplete(follower, func() {
						s.withCDPPortLock(debugPort, func() {
							if debugPort <= 0 || !hasMasterTarget {
								s.dispatchPageKeyFallback(follower, event)
								return
							}
							followerTarget, ok := matchingFollowerCDPTarget(masterTarget, debugPort, waitPopup)
							if !ok {
								// Never silently drop password/auth digits when the
								// follower wallet surface is still attaching.
								s.dispatchPageKeyFallback(follower, event)
								return
							}
							// Direct latin input (password digits/letters) uses insertText
							// so React-controlled wallet fields update reliably. IME must
							// not: insertText breaks composition.
							if event.down && !event.imeActive && event.character != 0 && event.modifiers&3 == 0 && !isSpecialKey(event.vk) {
								if _, err := cdpCallTarget(followerTarget, "Input.insertText", map[string]any{"text": string(event.character)}); err != nil {
									s.dispatchPageKeyFallback(follower, event)
								}
								return
							}
							params := map[string]any{
								"type":                  map[bool]string{true: "keyDown", false: "keyUp"}[event.down],
								"windowsVirtualKeyCode": int(event.vk), "nativeVirtualKeyCode": int(event.vk),
								"modifiers": event.modifiers, "key": cdpKeyName(event.vk),
							}
							if event.character != 0 && !event.imeActive {
								params["text"] = string(event.character)
								params["unmodifiedText"] = string(event.character)
							}
							if _, err := cdpCallTarget(followerTarget, "Input.dispatchKeyEvent", params); err != nil {
								s.dispatchPageKeyFallback(follower, event)
								return
							}
							// Space is classified special but still needs a char event for
							// contenteditable/password-adjacent fields.
							if event.down && !event.imeActive && event.vk == 0x20 {
								_, _ = cdpCallTarget(followerTarget, "Input.dispatchKeyEvent", map[string]any{
									"type": "char", "text": " ", "unmodifiedText": " ",
									"windowsVirtualKeyCode": 0x20, "nativeVirtualKeyCode": 0x20,
									"modifiers": event.modifiers, "key": " ",
								})
							}
						})
					}, wg.Done)
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
	s.enqueuePageInput(pageInputCritical, func() {
		s.dispatchPageMouseViaCDPNow(msg, screenX, screenY)
	})
}

func (s *InputSyncer) enqueuePageInput(kind pageInputKind, action func()) {
	if action == nil || !s.canDispatch() {
		return
	}
	event := pageInputEvent{
		generation: atomic.LoadUint64(&s.dispatchGeneration),
		kind:       kind,
		action:     action,
	}
	select {
	case s.pageInputQueue <- event:
		return
	default:
	}
	// Moves may be coalesced away under load. Clicks and wheels must not:
	// a brief wait absorbs short CDP bursts without blocking the hook thread
	// (this runs on the page-input worker after the hook already returned).
	if kind == pageInputMove {
		atomic.AddInt32(&s.pageInputDrops, 1)
		return
	}
	timer := time.NewTimer(8 * time.Millisecond)
	defer timer.Stop()
	select {
	case s.pageInputQueue <- event:
	case <-timer.C:
		atomic.AddInt32(&s.pageInputDrops, 1)
	}
}

func (s *InputSyncer) pageInputDispatchLoop(stopCh <-chan struct{}, queue <-chan pageInputEvent) {
	for {
		select {
		case <-stopCh:
			return
		case event := <-queue:
			if event.action != nil && s.canDispatch() && event.generation == atomic.LoadUint64(&s.dispatchGeneration) {
				event.action()
			}
		}
	}
}

// waitForFollowerCDPMatch enables popup wait only when the master surface is a
// popup/notification. Normal page clicks must not sleep up to 200ms per
// follower looking for a popup that will never appear.
func waitForFollowerCDPMatch(master cdpTarget) bool {
	return popupLikeCDPTarget(master)
}

func (s *InputSyncer) dispatchPageMouseViaCDPNow(msg uint32, screenX, screenY int) {
	masterSurface, masterRender, insidePage := chromePageSurfaceAtPoint(s.masterHwnd, screenX, screenY)
	if !insidePage {
		return
	}
	ml, mt, mr, mb := getWindowRect(masterRender)
	if mr <= ml || mb <= mt {
		return
	}
	rx := float64(screenX-int(ml)) / float64(mr-ml)
	ry := float64(screenY-int(mt)) / float64(mb-mt)
	s.mu.Lock()
	masterPort := s.masterDebug
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	masterTarget, hasMasterTarget := s.focusedMasterCDPTarget(masterPort)
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
	waitPopup := hasMasterTarget && waitForFollowerCDPMatch(masterTarget)
	var wg sync.WaitGroup
	for i, hwnd := range hwnds {
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		wg.Add(1)
		go func(port int, hwnd windows.HWND) {
			defer wg.Done()
			if port <= 0 || !hasMasterTarget {
				s.dispatchPageMouseFallback(hwnd, msg, screenX, screenY)
				return
			}
			_, render, ok := followerRenderForMasterSurface(masterSurface, s.masterHwnd, hwnd)
			if !ok {
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
				s.withCDPPortLock(port, func() {
					followerTarget, ok := matchingFollowerCDPTarget(masterTarget, port, waitPopup)
					if !ok {
						s.dispatchPageMouseFallback(hwnd, msg, screenX, screenY)
						return
					}
					if _, err := cdpCallTarget(followerTarget, "Input.dispatchMouseEvent", map[string]any{
						"type": eventType, "x": x, "y": y, "button": button, "buttons": buttons, "clickCount": 1,
					}); err != nil {
						s.dispatchPageMouseFallback(hwnd, msg, screenX, screenY)
					}
				})
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
	s.enqueuePageInput(pageInputMove, func() {
		s.dispatchPageMouseMoveViaCDPNow(screenX, screenY, buttonDownMsg)
	})
}

func (s *InputSyncer) dispatchPageMouseMoveViaCDPNow(screenX, screenY int, buttonDownMsg uint32) {
	masterSurface, masterRender, insidePage := chromePageSurfaceAtPoint(s.masterHwnd, screenX, screenY)
	if !insidePage {
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
	masterPort := s.masterDebug
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	masterTarget, hasMasterTarget := s.focusedMasterCDPTarget(masterPort)
	var wg sync.WaitGroup
	for i, hwnd := range hwnds {
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		wg.Add(1)
		go func(port int, hwnd windows.HWND) {
			defer wg.Done()
			if port <= 0 || !hasMasterTarget {
				return
			}
			_, render, ok := followerRenderForMasterSurface(masterSurface, s.masterHwnd, hwnd)
			if !ok {
				return
			}
			fl, ft, fr, fb := getWindowRect(render)
			x, y := rx*float64(fr-fl), ry*float64(fb-ft)
			s.dispatchWithRandomDelay(hwnd, func() {
				s.withCDPPortLock(port, func() {
					followerTarget, ok := matchingFollowerCDPTarget(masterTarget, port, false)
					if !ok {
						return
					}
					_, _ = cdpCallTarget(followerTarget, "Input.dispatchMouseEvent", map[string]any{
						"type": "mouseMoved", "x": x, "y": y, "button": button, "buttons": buttons,
					})
				})
			})
		}(port, hwnd)
	}
	wg.Wait()
}

func (s *InputSyncer) dispatchPageWheelViaCDP(msg uint32, screenX, screenY int, delta int16, keyState uint16) {
	s.enqueuePageInput(pageInputCritical, func() {
		s.dispatchPageWheelViaCDPNow(msg, screenX, screenY, delta, keyState)
	})
}

// flushPendingMouseMove delivers the last throttled hover sample so a click or
// wheel event does not fire from a stale pointer position on followers.
func (s *InputSyncer) flushPendingMouseMove(followers []windows.HWND, fallbackX, fallbackY int) {
	if s == nil || !atomic.CompareAndSwapInt32(&s.pendingMoveFlush, 1, 0) {
		return
	}
	x := int(atomic.LoadInt32(&s.lastMoveScreenX))
	y := int(atomic.LoadInt32(&s.lastMoveScreenY))
	if x == 0 && y == 0 {
		x, y = fallbackX, fallbackY
	}
	atomic.StoreInt64(&s.lastMoveTime, time.Now().UnixNano())
	if buttonDown := uint32(atomic.LoadInt32(&s.activePageMouseButton)); buttonDown != 0 {
		s.dispatchPageMouseMoveViaCDP(x, y, buttonDown)
		return
	}
	for _, hwnd := range followers {
		if !isWindow(hwnd) {
			continue
		}
		targetHwnd, lparam, ok := mapChromeInputTarget(x, y, s.masterHwnd, hwnd)
		if !ok {
			continue
		}
		s.dispatchWithRandomDelay(hwnd, func() {
			sendChromeUIMouseMessage(targetHwnd, WM_MOUSEMOVE, 0, lparam, targetHwnd != hwnd)
		})
	}
}

func (s *InputSyncer) dispatchPageWheelViaCDPNow(msg uint32, screenX, screenY int, delta int16, keyState uint16) {
	masterSurface, masterRender, insidePage := chromePageSurfaceAtPoint(s.masterHwnd, screenX, screenY)
	if !insidePage {
		return
	}
	ml, mt, mr, mb := getWindowRect(masterRender)
	if mr <= ml || mb <= mt {
		return
	}
	rx := float64(screenX-int(ml)) / float64(mr-ml)
	ry := float64(screenY-int(mt)) / float64(mb-mt)
	deltaX, deltaY := pageWheelDeltas(msg, delta, keyState)
	modifiers := 0
	if keyState&MK_CONTROL != 0 {
		modifiers |= 2
	}
	if keyState&MK_SHIFT != 0 {
		modifiers |= 8
	}
	s.mu.Lock()
	masterPort := s.masterDebug
	ports := append([]int(nil), s.followerDebug...)
	hwnds := append([]windows.HWND(nil), s.followerHwnds...)
	s.mu.Unlock()
	masterTarget, hasMasterTarget := s.focusedMasterCDPTarget(masterPort)
	waitPopup := hasMasterTarget && waitForFollowerCDPMatch(masterTarget)
	var wg sync.WaitGroup
	for i, hwnd := range hwnds {
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		wg.Add(1)
		go func(port int, hwnd windows.HWND) {
			defer wg.Done()
			if port <= 0 || !hasMasterTarget {
				s.dispatchPageWheelFallback(hwnd, msg, screenX, screenY, delta, keyState)
				return
			}
			_, render, ok := followerRenderForMasterSurface(masterSurface, s.masterHwnd, hwnd)
			if !ok {
				s.dispatchPageWheelFallback(hwnd, msg, screenX, screenY, delta, keyState)
				return
			}
			fl, ft, fr, fb := getWindowRect(render)
			x, y := rx*float64(fr-fl), ry*float64(fb-ft)
			s.dispatchWithRandomDelay(hwnd, func() {
				s.withCDPPortLock(port, func() {
					followerTarget, ok := matchingFollowerCDPTarget(masterTarget, port, waitPopup)
					if !ok {
						return
					}
					if _, err := cdpCallTarget(followerTarget, "Input.dispatchMouseEvent", map[string]any{
						"type": "mouseWheel", "x": x, "y": y,
						"deltaX": deltaX, "deltaY": deltaY, "modifiers": modifiers,
					}); err != nil {
						s.dispatchPageWheelFallback(hwnd, msg, screenX, screenY, delta, keyState)
					}
				})
			})
		}(port, hwnd)
	}
	wg.Wait()
}

// pageWheelDeltas converts Win32 wheel messages into CDP Input.dispatchMouseEvent
// deltas. Ctrl keeps vertical delta for zoom; Shift+vertical becomes horizontal.
func pageWheelDeltas(msg uint32, delta int16, keyState uint16) (deltaX, deltaY float64) {
	if msg == WM_MOUSEHWHEEL {
		return float64(delta), 0
	}
	if keyState&MK_SHIFT != 0 && keyState&MK_CONTROL == 0 {
		return -float64(delta), 0
	}
	// Win32 positive means wheel-up; CDP positive deltaY scrolls down.
	return 0, -float64(delta)
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

func digitRuneFromVirtualKey(vk uint32) rune {
	if vk >= 0x30 && vk <= 0x39 {
		return rune('0' + (vk - 0x30))
	}
	// Numpad 0-9 (VK_NUMPAD0..9). Only meaningful when NumLock produces digits.
	if vk >= 0x60 && vk <= 0x69 {
		return rune('0' + (vk - 0x60))
	}
	return 0
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
	case 0x20:
		return " "
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
	// Top-row and numpad digits (wallet PIN / password / amount fields).
	if ch := digitRuneFromVirtualKey(vk); ch != 0 {
		return string(ch)
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

func (s *InputSyncer) urlSyncLoop(stopCh <-chan struct{}) {
	// 250ms is enough for omnibox/SPA navigation and keeps CDP load low when
	// 20+ followers each open a fresh WebSocket for location.href.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}

		if atomic.LoadInt32(&s.active) == 0 {
			return
		}
		if s.IsPaused() {
			// Esc pause must not touch follower pages. Do not navigate, reload,
			// or mirror editable fields while input sync is suspended.
			continue
		}

		s.mu.Lock()
		masterDebug := s.masterDebug
		followerDebug := make([]int, len(s.followerDebug))
		copy(followerDebug, s.followerDebug)
		s.mu.Unlock()
		if masterDebug <= 0 || len(followerDebug) == 0 {
			continue
		}

		// Esc resume reseed: record master's current URL/editable as the new
		// baseline without pushing anything to followers. This runs even when
		// the pointer is outside the master so a later pointer-enter cannot
		// suddenly navigate every follower to the pre-pause URL.
		if atomic.LoadInt32(&s.urlSyncReseed) == 1 {
			s.reseedURLSyncBaseline(masterDebug)
			continue
		}

		if atomic.LoadInt32(&s.pointerInsideMaster) == 0 {
			s.lastFocusedEditableState = ""
			continue
		}

		masterTarget, hasMasterTarget := s.focusedMasterCDPTarget(masterDebug)
		if atomic.LoadInt32(&s.pageKeyboardFocus) == 1 && hasMasterTarget {
			if state := s.getMasterFocusedEditableStateOnTarget(masterTarget); state != "" && state != s.lastFocusedEditableState {
				s.lastFocusedEditableState = state
				waitPopup := waitForFollowerCDPMatch(masterTarget)
				var inputWG sync.WaitGroup
				for _, port := range followerDebug {
					if port <= 0 {
						continue
					}
					inputWG.Add(1)
					go func(debugPort int) {
						defer inputWG.Done()
						s.withCDPPortLock(debugPort, func() {
							s.applyFollowerFocusedEditableStateOnTarget(debugPort, masterTarget, state, waitPopup)
						})
					}(port)
				}
				inputWG.Wait()
			}
		} else {
			s.lastFocusedEditableState = ""
		}

		url := ""
		if hasMasterTarget {
			url = strings.TrimSpace(masterTarget.URL)
		}
		if url == "" {
			url = s.getMasterURL(masterDebug)
		}
		if url != "" && url != s.lastSyncURL && !isAboutBlank(url) {
			// Do not force-navigate followers to a wallet popup/notification
			// document: that replaces their main tab with a full-page extension
			// UI and desyncs browsing. Extension password display is mirrored
			// via focused-target insertText + editable-state sync instead.
			if extensionLikeCDPTarget(cdpTarget{URL: url}) {
				s.lastSyncURL = url
			} else {
				s.lastSyncURL = url
				s.invalidateMasterCDPTargetCache()
				var wg sync.WaitGroup
				for _, port := range followerDebug {
					if port > 0 {
						wg.Add(1)
						go func(debugPort int) {
							defer wg.Done()
							s.withCDPPortLock(debugPort, func() {
								s.navigateFollower(debugPort, url)
							})
						}(port)
					}
				}
				wg.Wait()
			}
		}
	}
}

// reseedURLSyncBaseline captures the master's current URL and focused editable
// value as the sync baseline without navigating or writing followers. Used
// after Esc resume so only subsequent user navigations are mirrored.
func (s *InputSyncer) reseedURLSyncBaseline(masterDebug int) {
	if s == nil || masterDebug <= 0 {
		return
	}
	s.invalidateMasterCDPTargetCache()
	masterTarget, hasMasterTarget := s.focusedMasterCDPTarget(masterDebug)
	url := ""
	if hasMasterTarget {
		url = strings.TrimSpace(masterTarget.URL)
	}
	if url == "" {
		url = s.getMasterURL(masterDebug)
	}
	// Keep the reseed pending until CDP answers; otherwise the first successful
	// URL read later would look like a "change" and Page.navigate every follower.
	if url == "" {
		return
	}
	editable := ""
	if hasMasterTarget && atomic.LoadInt32(&s.pageKeyboardFocus) == 1 {
		editable = s.getMasterFocusedEditableStateOnTarget(masterTarget)
	}
	s.mu.Lock()
	if !isAboutBlank(url) {
		s.lastSyncURL = url
	} else {
		// about:blank is intentionally not mirrored; store a stable sentinel so
		// a later real navigation is still detected without a false resume push.
		s.lastSyncURL = "about:blank"
	}
	s.lastFocusedEditableState = editable
	s.mu.Unlock()
	atomic.StoreInt32(&s.urlSyncReseed, 0)
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

const focusedEditableStateExpression = `(() => {
	const e = document.activeElement;
	if (!e) return '';
	if (e.tagName === 'INPUT' || e.tagName === 'TEXTAREA') {
		return JSON.stringify({
			kind: 'input',
			tag: String(e.tagName || '').toLowerCase(),
			type: String(e.type || ''),
			name: String(e.name || ''),
			id: String(e.id || ''),
			testId: String(e.getAttribute('data-testid') || ''),
			placeholder: String(e.getAttribute('placeholder') || ''),
			value: String(e.value ?? ''),
			start: typeof e.selectionStart === 'number' ? e.selectionStart : -1,
			end: typeof e.selectionEnd === 'number' ? e.selectionEnd : -1
		});
	}
	if (e.isContentEditable) {
		return JSON.stringify({
			kind: 'contenteditable',
			value: String(e.innerText ?? e.textContent ?? '')
		});
	}
	return '';
})()`

func (s *InputSyncer) getMasterFocusedEditableStateOnTarget(target cdpTarget) string {
	if strings.TrimSpace(target.WebSocketDebuggerUrl) == "" {
		return ""
	}
	result, err := cdpCallTarget(target, "Runtime.evaluate", map[string]any{
		"expression": focusedEditableStateExpression, "returnByValue": true,
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

func (s *InputSyncer) applyFollowerFocusedEditableStateOnTarget(debugPort int, master cdpTarget, state string, waitPopup bool) {
	if state == "" || debugPort <= 0 {
		return
	}
	followerTarget, ok := matchingFollowerCDPTarget(master, debugPort, waitPopup)
	if !ok {
		return
	}
	// Resolve the matching password/auth field even when the follower click
	// missed focus (common for wallet unlock/confirm UIs).
	expression := `(() => {
		const s = JSON.parse(` + strconv.Quote(state) + `);
		const isEditable = (el) => !!(el && (
			el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' ||
			(typeof el.isContentEditable === 'boolean' && el.isContentEditable)
		));
		const setInputValue = (el, next) => {
			const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
			const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
			if (setter) setter.call(el, next); else el.value = next;
			el.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: null }));
			el.dispatchEvent(new Event('change', { bubbles: true }));
		};
		const resolveInput = () => {
			let e = document.activeElement;
			if (isEditable(e) && (e.tagName === 'INPUT' || e.tagName === 'TEXTAREA')) {
				if (!s.type || !e.type || String(e.type) === String(s.type) || s.type === 'text' || e.type === 'password') {
					return e;
				}
			}
			const selectors = [];
			if (s.testId) selectors.push('[data-testid="' + CSS.escape(String(s.testId)) + '"]');
			if (s.id) selectors.push('#' + CSS.escape(String(s.id)));
			if (s.name) selectors.push((s.tag === 'textarea' ? 'textarea' : 'input') + '[name="' + CSS.escape(String(s.name)) + '"]');
			if (s.type === 'password') selectors.push('input[type="password"]');
			if (s.placeholder) selectors.push((s.tag === 'textarea' ? 'textarea' : 'input') + '[placeholder="' + CSS.escape(String(s.placeholder)) + '"]');
			selectors.push('input[type="password"]', 'input[type="text"]', 'input:not([type])', 'textarea');
			for (const sel of selectors) {
				try {
					const found = document.querySelector(sel);
					if (found && (found.tagName === 'INPUT' || found.tagName === 'TEXTAREA')) {
						return found;
					}
				} catch (_) {}
			}
			return null;
		};
		if (s.kind === 'contenteditable') {
			let e = document.activeElement;
			if (!e || !e.isContentEditable) {
				e = document.querySelector('[contenteditable="true"], [contenteditable=""]');
			}
			if (!e || !e.isContentEditable) return false;
			const next = String(s.value ?? '');
			if (String(e.innerText ?? e.textContent ?? '') !== next) {
				e.focus();
				e.innerText = next;
				e.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: null }));
			}
			return true;
		}
		const e = resolveInput();
		if (!e) return false;
		try { e.focus(); } catch (_) {}
		if (String(e.value ?? '') !== String(s.value ?? '')) {
			setInputValue(e, String(s.value ?? ''));
		}
		if (s.start >= 0 && typeof e.setSelectionRange === 'function') {
			try { e.setSelectionRange(s.start, s.end); } catch (_) {}
		}
		return true;
	})()`
	_, _ = cdpCallTarget(followerTarget, "Runtime.evaluate", map[string]any{
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
