//go:build windows

package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"boost-browser/backend/internal/logger"
)

type browserRuntimeProcess struct {
	PID            int
	ExecutablePath string
	CommandLine    string
	UserDataDir    string
	DebugPort      int
}

type winProcessSnapshot struct {
	ProcessId      int    `json:"ProcessId"`
	ExecutablePath string `json:"ExecutablePath"`
	CommandLine    string `json:"CommandLine"`
}

var (
	remoteDebugPortRe       = regexp.MustCompile(`(?i)(?:^|\s)"?--remote-debugging-port=(\d+)"?`)
	quotedUserDataArgRe     = regexp.MustCompile(`(?i)(?:^|\s)"--user-data-dir=([^"]+)"`)
	quotedUserDataValueRe   = regexp.MustCompile(`(?i)(?:^|\s)--user-data-dir="([^"]+)"`)
	unquotedUserDataValueRe = regexp.MustCompile(`(?i)(?:^|\s)--user-data-dir=([^"\s]+)`)
)

func (a *App) startBrowserRuntimeReconciler() {
	if a == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.New("Browser").Error("runtime reconciler goroutine panic recovered",
					logger.F("error", r),
				)
			}
		}()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.New("Browser").Error("reconcileBrowserRuntimeStateOnce panic recovered",
							logger.F("error", r),
						)
					}
				}()
				a.reconcileBrowserRuntimeStateOnce()
			}()
		}
	}()
}

func (a *App) reconcileBrowserRuntimeStateOnce() {
	if a == nil || a.browserMgr == nil {
		return
	}

	processes, err := discoverBoostBrowserProcesses(a.appRoot)
	if err != nil {
		return
	}

	// Resolve every currently tracked Chromium root in one process/window pass.
	// The old per-profile lookup repeated Toolhelp + EnumWindows for every open
	// environment, making a manual refresh increasingly slow and more likely to
	// observe an inconsistent window set while users close/reopen instances.
	currentPIDs := make([]int, 0)
	a.browserMgr.Mutex.Lock()
	for _, profile := range a.browserMgr.Profiles {
		if profile != nil && profile.Running && profile.Pid > 0 {
			currentPIDs = append(currentPIDs, profile.Pid)
		}
	}
	a.browserMgr.Mutex.Unlock()
	currentWindows := findProcessTreeWindows(currentPIDs)

	type update struct {
		profile *BrowserProfile
		kind    string
	}
	var updates []update
	var recoveredIDs []string
	var stoppedIDs []string
	log := logger.New("Browser")

	a.browserMgr.Mutex.Lock()
	defer func() {
		if len(updates) > 0 {
			a.persistBrowserRuntimeSnapshotLocked()
		}
		a.browserMgr.Mutex.Unlock()
		for _, item := range updates {
			if item.profile != nil {
				a.emitBrowserInstanceUpdated(item.profile)
			}
		}
	}()

	byUserDataDir := make(map[string][]browserRuntimeProcess)
	for _, proc := range processes {
		key := normalizeRuntimePathKey(proc.UserDataDir)
		if key == "" || proc.DebugPort <= 0 {
			continue
		}
		byUserDataDir[key] = append(byUserDataDir[key], proc)
	}
	for key := range byUserDataDir {
		sort.Slice(byUserDataDir[key], func(i, j int) bool {
			return byUserDataDir[key][i].PID < byUserDataDir[key][j].PID
		})
	}

	for profileId, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}

		userDataKey := normalizeRuntimePathKey(a.browserMgr.ResolveUserDataDir(profile))
		proc, exists := pickRuntimeProcessForSync(byUserDataDir[userDataKey])
		if profile.Running && isBrowserProfileLive(profile, a.browserMgr.BrowserProcesses[profileId]) {
			// Chrome may hand the visible top-level frame to a sibling process that
			// shares the same user-data-dir. A live launcher PID is therefore not
			// enough: keep it only while it still resolves to a real browser frame.
			if profile.Pid > 0 && currentWindows[profile.Pid] != 0 {
				continue
			}
			if !exists {
				continue
			}

			debugReady := canConnectDebugPort(proc.DebugPort, 300*time.Millisecond)
			runtimeWarning := profile.RuntimeWarning
			if !debugReady {
				runtimeWarning = "主程序已重新接管该浏览器进程，但调试接口暂未就绪；实例仍保持运行状态。"
			} else {
				runtimeWarning = ""
			}
			profile.Pid = proc.PID
			profile.DebugPort = proc.DebugPort
			profile.DebugReady = debugReady
			profile.RuntimeWarning = runtimeWarning
			updates = append(updates, update{profile: copyBrowserProfileSnapshot(profile), kind: "recovered"})
			recoveredIDs = append(recoveredIDs, profileId)
			continue
		}

		if !exists {
			if profile.Running {
				a.markProfileStoppedLocked(profileId, profile)
				updates = append(updates, update{profile: copyBrowserProfileSnapshot(profile), kind: "stopped"})
				stoppedIDs = append(stoppedIDs, profileId)
			}
			continue
		}

		debugReady := canConnectDebugPort(proc.DebugPort, 300*time.Millisecond)
		runtimeWarning := ""
		if !debugReady {
			runtimeWarning = "主程序已重新接管该浏览器进程，但调试接口暂未就绪；实例仍保持运行状态。"
		}
		a.markProfileRunningLocked(profileId, profile, nil, proc.PID, proc.DebugPort, debugReady, runtimeWarning)
		updates = append(updates, update{profile: copyBrowserProfileSnapshot(profile), kind: "recovered"})
		recoveredIDs = append(recoveredIDs, profileId)
	}
	for _, profileId := range recoveredIDs {
		log.Info("已重新接管宿主重启前存活的浏览器实例", logger.F("profile_id", profileId))
	}
	for _, profileId := range stoppedIDs {
		log.Info("运行实例已无存活浏览器进程，状态已同步为停止", logger.F("profile_id", profileId))
	}
}

func pickRuntimeProcessForSync(candidates []browserRuntimeProcess) (browserRuntimeProcess, bool) {
	if len(candidates) == 0 {
		return browserRuntimeProcess{}, false
	}
	for _, proc := range candidates {
		if proc.PID <= 0 {
			continue
		}
		if _, err := findProcessTreeWindow(proc.PID); err == nil {
			return proc, true
		}
	}
	return candidates[0], true
}

// discoverBoostBrowserProcessesCached reuses a short-lived process list so
// GetSyncProfiles + StartInputSync in the same second share one CIM scan.
var discoverProcessCache struct {
	mu   sync.Mutex
	at   time.Time
	root string
	list []browserRuntimeProcess
}

func discoverBoostBrowserProcessesCached(appRoot string) ([]browserRuntimeProcess, error) {
	root := filepath.Clean(strings.TrimSpace(appRoot))
	discoverProcessCache.mu.Lock()
	defer discoverProcessCache.mu.Unlock()
	if root != "" && root == discoverProcessCache.root &&
		time.Since(discoverProcessCache.at) < 2*time.Second &&
		discoverProcessCache.list != nil {
		out := make([]browserRuntimeProcess, len(discoverProcessCache.list))
		copy(out, discoverProcessCache.list)
		return out, nil
	}
	list, err := discoverBoostBrowserProcesses(root)
	if err != nil {
		return list, err
	}
	discoverProcessCache.root = root
	discoverProcessCache.at = time.Now()
	discoverProcessCache.list = list
	out := make([]browserRuntimeProcess, len(list))
	copy(out, list)
	return out, nil
}

func discoverBoostBrowserProcesses(appRoot string) ([]browserRuntimeProcess, error) {
	root := strings.TrimSpace(appRoot)
	if root == "" {
		return nil, nil
	}
	root = filepath.Clean(root)
	// Match either:
	//   - chrome binaries under <appRoot>/chrome (bundled kernels), or
	//   - any Chromium whose --user-data-dir sits under <appRoot>/data
	// so the sync panel can discover all multi-open envs without depending on
	// a single published snapshot file.
	script := fmt.Sprintf(`
$root = %s
$chromeRoot = [System.IO.Path]::GetFullPath((Join-Path $root 'chrome'))
$dataRoot = [System.IO.Path]::GetFullPath((Join-Path $root 'data'))
$items = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
  if (-not $_.CommandLine) { return $false }
  $cmd = $_.CommandLine
  if ($cmd -notmatch '--user-data-dir=' -or $cmd -notmatch '--remote-debugging-port=') { return $false }
  $exe = [string]$_.ExecutablePath
  $exeHit = $exe -and $exe.StartsWith($chromeRoot, [System.StringComparison]::OrdinalIgnoreCase)
  $dataHit = $cmd.IndexOf($dataRoot, [System.StringComparison]::OrdinalIgnoreCase) -ge 0
  return ($exeHit -or $dataHit)
} | Select-Object ProcessId, ExecutablePath, CommandLine
@($items) | ConvertTo-Json -Depth 3 -Compress
`, psSingleQuoted(root))

	// Dense multi-open (100+) CIM queries need more than 5s on busy machines.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePowerShellCommand(script))
	hideWindow(cmd)
	out, cmdErr := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("powershell process discovery timed out after 12s")
	}
	if cmdErr != nil {
		return nil, cmdErr
	}
	text := strings.TrimSpace(string(out))
	if text == "" || text == "null" {
		return nil, nil
	}

	var snapshots []winProcessSnapshot
	if strings.HasPrefix(text, "[") {
		if err := json.Unmarshal([]byte(text), &snapshots); err != nil {
			return nil, err
		}
	} else {
		var one winProcessSnapshot
		if err := json.Unmarshal([]byte(text), &one); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, one)
	}

	processes := make([]browserRuntimeProcess, 0, len(snapshots))
	for _, item := range snapshots {
		userDataDir, debugPort := parseChromeRuntimeCommandLine(item.CommandLine)
		if item.ProcessId <= 0 || userDataDir == "" || debugPort <= 0 {
			continue
		}
		processes = append(processes, browserRuntimeProcess{
			PID:            item.ProcessId,
			ExecutablePath: item.ExecutablePath,
			CommandLine:    item.CommandLine,
			UserDataDir:    userDataDir,
			DebugPort:      debugPort,
		})
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes, nil
}

func parseChromeRuntimeCommandLine(commandLine string) (string, int) {
	userDataDir := ""
	for _, re := range []*regexp.Regexp{quotedUserDataArgRe, quotedUserDataValueRe, unquotedUserDataValueRe} {
		if match := re.FindStringSubmatch(commandLine); len(match) >= 2 {
			userDataDir = strings.TrimSpace(match[1])
			break
		}
	}
	debugPort := 0
	if match := remoteDebugPortRe.FindStringSubmatch(commandLine); len(match) >= 2 {
		if port, err := strconv.Atoi(match[1]); err == nil {
			debugPort = port
		}
	}
	return userDataDir, debugPort
}

func normalizeRuntimePathKey(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if path == "" {
		return ""
	}
	return strings.ToLower(filepath.Clean(path))
}

func encodePowerShellCommand(script string) string {
	encoded := utf16.Encode([]rune(script))
	buf := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		buf[i*2] = byte(v)
		buf[i*2+1] = byte(v >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func psSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
