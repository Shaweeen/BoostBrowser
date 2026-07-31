//go:build windows

package backend

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"boost-browser/backend/internal/logger"

	"golang.org/x/sys/windows"
)

// environmentPopupApp is set while the main client is live so layout updates and
// confinement share one App without wiring every InputSyncer to a pointer.
var environmentPopupApp atomic.Pointer[App]

// layoutHoldRoot is the app data root used for a cross-process layout-hold flag
// (main confiner must yield while the panel tiles or the main list batch-tiles).
var layoutHoldRoot atomic.Value // string

// environmentPopupConfiner is the single owner of popup / extension / secondary
// Chrome window geometry for every running environment on the main client.
// The sync assistant must NOT run a parallel SetWindowPos loop (relative-align
// thrash). InputSyncer only maps input coordinates to existing surfaces.
//
// Policy:
//   - one geometry writer (this confiner on main);
//   - action/lifecycle driven start/stop from environment start/stop;
//   - natural popup size (position-only nudge; never shrink wallets);
//   - layout hold (local + shared flag) while tiles move.
type environmentPopupConfiner struct {
	mu       sync.Mutex
	stopCh   chan struct{}
	running  bool
	layoutHold int32
	busy     int32
}

// setLayoutHoldRoot records the shared data root for both main and panel.
func setLayoutHoldRoot(appRoot string) {
	appRoot = strings.TrimSpace(appRoot)
	if appRoot == "" {
		return
	}
	layoutHoldRoot.Store(appRoot)
}

func layoutHoldFlagPath() string {
	root, _ := layoutHoldRoot.Load().(string)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "data", "layout-hold.flag")
}

// setSharedLayoutHold publishes a short-lived cross-process hold so the main
// confiner yields while any process rearranges environment windows.
func setSharedLayoutHold(hold bool) {
	path := layoutHoldFlagPath()
	if path == "" {
		return
	}
	if hold {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
		return
	}
	_ = os.Remove(path)
}

func sharedLayoutHoldActive() bool {
	path := layoutHoldFlagPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		// Corrupt flag: treat as active briefly until removed.
		return true
	}
	// Auto-expire so a crashed panel cannot pin the confiner forever.
	return time.Since(t) < 3*time.Second
}

func (a *App) registerEnvironmentPopupConfiner() {
	if a == nil || a.panelMode {
		return
	}
	setLayoutHoldRoot(a.appRoot)
	environmentPopupApp.Store(a)
	a.refreshEnvironmentPopupConfinement()
}

func (a *App) unregisterEnvironmentPopupConfiner() {
	if a == nil {
		return
	}
	environmentPopupApp.CompareAndSwap(a, nil)
	if a.envPopup != nil {
		a.envPopup.stop()
	}
}

func (a *App) holdEnvironmentPopupConfinement(hold bool) {
	if a == nil {
		return
	}
	setLayoutHoldRoot(a.appRoot)
	setSharedLayoutHold(hold)
	if a.panelMode {
		// Panel has no confiner; shared flag is enough for the main process.
		return
	}
	if a.envPopup == nil {
		a.envPopup = &environmentPopupConfiner{}
	}
	if hold {
		atomic.StoreInt32(&a.envPopup.layoutHold, 1)
		return
	}
	atomic.StoreInt32(&a.envPopup.layoutHold, 0)
	// Re-apply immediately after layout settles.
	a.refreshEnvironmentPopupConfinement()
}

// refreshEnvironmentPopupConfinement starts or stops the single confiner from
// the current set of running environments. Safe to call from start/stop paths.
func (a *App) scheduleEnvironmentPopupConfinementRefresh() {
	if a == nil || a.panelMode {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		a.refreshEnvironmentPopupConfinement()
	}()
}

func (a *App) refreshEnvironmentPopupConfinement() {
	if a == nil || a.panelMode {
		return
	}
	if a.envPopup == nil {
		a.envPopup = &environmentPopupConfiner{}
	}
	owners := a.collectRunningEnvironmentPopupOwners()
	// Single-environment popups do not cover sibling environments. Only pay for
	// continuous confinement when multi-open creates cross-window interference.
	if len(owners) < 2 {
		a.envPopup.stop()
		return
	}
	a.envPopup.ensureStarted(func() []syncPopupOwnerWindow {
		return a.collectRunningEnvironmentPopupOwners()
	}, func() bool {
		return atomic.LoadInt32(&a.envPopup.layoutHold) != 0
	})
}

func (a *App) collectRunningEnvironmentPopupOwners() []syncPopupOwnerWindow {
	if a == nil || a.browserMgr == nil {
		return nil
	}
	a.browserMgr.Mutex.Lock()
	rootPIDs := make([]int, 0)
	type runningProfile struct {
		pid int
	}
	running := make([]runningProfile, 0)
	for _, profile := range a.browserMgr.Profiles {
		if profile == nil || !profile.Running || profile.Pid <= 0 {
			continue
		}
		running = append(running, runningProfile{pid: profile.Pid})
		rootPIDs = append(rootPIDs, profile.Pid)
	}
	a.browserMgr.Mutex.Unlock()
	if len(running) == 0 {
		return nil
	}

	resolved := findProcessTreeWindows(rootPIDs)
	owners := make([]syncPopupOwnerWindow, 0, len(running))
	seen := make(map[windows.HWND]struct{}, len(running))
	for _, item := range running {
		hwnd := resolved[item.pid]
		if hwnd == 0 || !isWindow(hwnd) {
			continue
		}
		if _, dup := seen[hwnd]; dup {
			continue
		}
		seen[hwnd] = struct{}{}
		rect, ok := getTopLevelWindowRect(hwnd)
		if !ok || rect.Right <= rect.Left || rect.Bottom <= rect.Top {
			continue
		}
		owners = append(owners, syncPopupOwnerWindow{
			hwnd: hwnd,
			pid:  windowPID(hwnd),
			rect: rect,
		})
	}
	return owners
}

func (c *environmentPopupConfiner) ensureStarted(
	collectOwners func() []syncPopupOwnerWindow,
	layoutHeld func() bool,
) {
	if c == nil || collectOwners == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return
	}
	c.stopCh = make(chan struct{})
	c.running = true
	stopCh := c.stopCh
	go c.loop(stopCh, collectOwners, layoutHeld)
	logger.New("PopupConfiner").Info("环境弹窗约束已启动", logger.F("scope", "all-running-environments"))
}

func (c *environmentPopupConfiner) stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	stopCh := c.stopCh
	c.stopCh = nil
	c.running = false
	c.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	logger.New("PopupConfiner").Info("环境弹窗约束已停止")
}

func (c *environmentPopupConfiner) loop(
	stop <-chan struct{},
	collectOwners func() []syncPopupOwnerWindow,
	layoutHeld func() bool,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.New("PopupConfiner").Error("environment popup confiner panic recovered", logger.F("error", recovered))
		}
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()

	// Run once immediately so a wallet prompt opened right after multi-start is
	// confined without waiting for the first tick.
	c.applyOnce(collectOwners, layoutHeld)

	for {
		owners := collectOwners()
		if len(owners) == 0 {
			return
		}
		interval := syncPopupBoundsIntervalForFollowers(len(owners))
		timer := time.NewTimer(interval)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			c.applyOnce(collectOwners, layoutHeld)
		}
	}
}

func (c *environmentPopupConfiner) applyOnce(
	collectOwners func() []syncPopupOwnerWindow,
	layoutHeld func() bool,
) {
	if c == nil || collectOwners == nil {
		return
	}
	if layoutHeld != nil && layoutHeld() {
		return
	}
	// Cross-process: panel tile / batch tile write layout-hold.flag.
	if sharedLayoutHoldActive() {
		return
	}
	if !atomic.CompareAndSwapInt32(&c.busy, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&c.busy, 0)

	owners := collectOwners()
	if len(owners) == 0 {
		return
	}
	constrainPopupSurfacesToOwners(owners)
}
