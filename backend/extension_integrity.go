package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
)

// Per-environment integrity marker: assignment is fully adapted under user-data.
// When present and fingerprint matches, start skips all install/CLI/verify work.
const extensionIntegrityMarkerName = ".boost_extension_integrity_v1"

type extensionIntegrityMarker struct {
	AssignmentFingerprint string   `json:"assignmentFingerprint"`
	ExtensionIDs          []string `json:"extensionIds"`
	Complete              bool     `json:"complete"`
	VerifiedAt            string   `json:"verifiedAt"`
	ProfileID             string   `json:"profileId,omitempty"`
}

// ExtensionIntegrityScanResult is returned by the one-shot client-open scan.
type ExtensionIntegrityScanResult struct {
	TotalProfiles       int      `json:"totalProfiles"`
	Complete            int      `json:"complete"`
	Incomplete          int      `json:"incomplete"`
	Repaired            int      `json:"repaired"`
	SkippedRunning      int      `json:"skippedRunning"`
	IncompleteIDs       []string `json:"incompleteIds"`
	DismissedIncomplete int      `json:"dismissedIncomplete"`
	Message             string   `json:"message"`
	AlreadyScanned      bool     `json:"alreadyScanned"`
}

var (
	extensionIntegrityScanDone bool
	extensionIntegrityScanMu   sync.Mutex
)

func extensionIntegrityMarkerPath(userDataDir string) string {
	return filepath.Join(strings.TrimSpace(userDataDir), extensionIntegrityMarkerName)
}

func readExtensionIntegrityMarker(userDataDir string) (extensionIntegrityMarker, bool) {
	data, err := os.ReadFile(extensionIntegrityMarkerPath(userDataDir))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return extensionIntegrityMarker{}, false
	}
	var marker extensionIntegrityMarker
	if json.Unmarshal(data, &marker) != nil || !marker.Complete {
		return extensionIntegrityMarker{}, false
	}
	if strings.TrimSpace(marker.AssignmentFingerprint) == "" {
		return extensionIntegrityMarker{}, false
	}
	return marker, true
}

func writeExtensionIntegrityMarker(userDataDir, profileID, fingerprint string, extensionIDs []string) error {
	userDataDir = strings.TrimSpace(userDataDir)
	fingerprint = strings.TrimSpace(fingerprint)
	if userDataDir == "" || fingerprint == "" {
		return nil
	}
	if err := os.MkdirAll(userDataDir, 0755); err != nil {
		return err
	}
	marker := extensionIntegrityMarker{
		AssignmentFingerprint: fingerprint,
		ExtensionIDs:          append([]string{}, extensionIDs...),
		Complete:              true,
		VerifiedAt:            time.Now().UTC().Format(time.RFC3339),
		ProfileID:             strings.TrimSpace(profileID),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(extensionIntegrityMarkerPath(userDataDir), data, 0600)
}

func clearExtensionIntegrityMarker(userDataDir string) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return
	}
	_ = os.Remove(extensionIntegrityMarkerPath(userDataDir))
}

// allAssignedPackagesExistOnDisk ensures program package dirs still resolve.
func allAssignedPackagesExistOnDisk(launchArgs []string) bool {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return true
	}
	for _, dir := range dirs {
		if validateUnpackedExtensionManifest(dir) != nil {
			return false
		}
	}
	return true
}

// isExtensionAssignmentComplete is the single gate for "do nothing on start".
// True when:
//   - no extensions assigned (Chrome-native / store installs only), or
//   - integrity marker matches current assignment (hot start: do not read
//     Preferences, LES, Cookies, or wallet vaults), or
//   - first live check: every assigned package has durable Chrome runtime
//     and we persist the marker so later starts skip entirely.
func isExtensionAssignmentComplete(userDataDir string, launchArgs []string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	fp, ids := assignmentFingerprintFromLaunchArgs(launchArgs)
	if fp == "" {
		return true
	}
	if userDataDir == "" {
		return false
	}
	if !allAssignedPackagesExistOnDisk(launchArgs) {
		return false
	}
	if marker, ok := readExtensionIntegrityMarker(userDataDir); ok {
		if strings.EqualFold(marker.AssignmentFingerprint, fp) {
			// The marker is an optimization hint, not proof that Chrome still
			// owns a loadable copy. A user update, profile repair, or antivirus
			// cleanup can remove Preferences/LES while leaving the marker and
			// package directory behind. Revalidate the durable runtime before
			// stripping --load-extension; otherwise a downloaded extension can
			// silently disappear on a normal restart.
			return everyAssignedHasDurableRuntime(userDataDir, launchArgs)
		}
	}
	if !everyAssignedHasDurableRuntime(userDataDir, launchArgs) {
		return false
	}
	_ = writeExtensionIntegrityMarker(userDataDir, "", fp, ids)
	return true
}

// applyCompleteExtensionLaunchArgs is the start-path owner. A complete
// assignment strips --load-extension and tells the caller to skip recovery,
// Preferences reads, and any other extension I/O for this launch.
func applyCompleteExtensionLaunchArgs(args []string, userDataDir string) (next []string, complete bool) {
	args = normalizeLoadExtensionArgs(args)
	if !isExtensionAssignmentComplete(userDataDir, args) {
		return args, false
	}
	return stripLoadExtensionArgs(args), true
}

func everyAssignedHasDurableRuntime(userDataDir string, launchArgs []string) bool {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return true
	}
	for _, dir := range dirs {
		// Integrity-complete / hard-strip CLI: loadable Preferences AND Chrome
		// has written durable runtime (same gate as canSkipLoadExtensionCLI).
		if !canSkipLoadExtensionCLI(userDataDir, dir) {
			return false
		}
	}
	return true
}

// clearExtensionIntegrityForProfiles clears integrity when assignment changes.
func (a *App) clearExtensionIntegrityForProfilesLocked(profileIDs []string) {
	if a == nil || a.browserMgr == nil {
		return
	}
	for _, id := range profileIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		profile := a.browserMgr.Profiles[id]
		if profile == nil {
			continue
		}
		clearExtensionIntegrityMarker(a.browserMgr.ResolveUserDataDir(profile))
	}
}

// markExtensionIntegrityIfComplete writes the stop-all-verify marker when live
// data proves every assigned package is adapted.
func markExtensionIntegrityIfComplete(userDataDir, profileID string, launchArgs []string) bool {
	fp, ids := assignmentFingerprintFromLaunchArgs(launchArgs)
	if fp == "" {
		return true
	}
	if !everyAssignedHasDurableRuntime(userDataDir, launchArgs) {
		return false
	}
	if !allAssignedPackagesExistOnDisk(launchArgs) {
		return false
	}
	if err := writeExtensionIntegrityMarker(userDataDir, profileID, fp, ids); err != nil {
		logger.New("Extension").Warn("写入扩展完整性标记失败",
			logger.F("profile_id", profileID),
			logger.F("error", err.Error()),
		)
		return false
	}
	logger.New("Extension").Info("扩展完整性已确认：后续启动跳过安装/CLI/巡检",
		logger.F("profile_id", profileID),
		logger.F("extension_count", len(ids)),
	)
	return true
}

// BrowserExtensionIntegrityScanAll runs once per app process (or forced) to
// verify every environment's assigned extensions. Incomplete stopped profiles
// get a lightweight Preferences registration attempt (no Chrome launch, no copy).
// Complete profiles only write/refresh the integrity marker.
func (a *App) BrowserExtensionIntegrityScanAll(force bool) *ExtensionIntegrityScanResult {
	result := &ExtensionIntegrityScanResult{
		IncompleteIDs: []string{},
	}
	if a == nil || a.browserMgr == nil {
		result.Message = "浏览器管理器未初始化"
		return result
	}
	extensionIntegrityScanMu.Lock()
	if extensionIntegrityScanDone && !force {
		extensionIntegrityScanMu.Unlock()
		result.AlreadyScanned = true
		result.Message = "本会话已完成扩展完整性巡检，已跳过"
		return result
	}
	extensionIntegrityScanMu.Unlock()

	log := logger.New("Extension")
	log.Info("开始扩展完整性巡检（客户端打开一次）")

	dismissed := a.dismissedExtensionIncompleteSet()

	profiles := a.browserMgr.List()
	result.TotalProfiles = len(profiles)
	for i := range profiles {
		p := &profiles[i]
		if p.Running {
			result.SkippedRunning++
			// Running: if already complete under user-data, count complete.
			ud := a.browserMgr.ResolveUserDataDir(p)
			if isExtensionAssignmentComplete(ud, p.LaunchArgs) {
				result.Complete++
			} else {
				result.appendIncomplete(p.ProfileId, dismissed)
			}
			continue
		}
		ud := a.browserMgr.ResolveUserDataDir(p)
		fp, _ := assignmentFingerprintFromLaunchArgs(p.LaunchArgs)
		if fp == "" {
			result.Complete++
			continue
		}
		if isExtensionAssignmentComplete(ud, p.LaunchArgs) {
			result.Complete++
			continue
		}
		// READ-ONLY scan: never rewrite Preferences / LES / package files.
		// Only refresh integrity marker when live detection already proves complete.
		beforeComplete := isExtensionAssignmentComplete(ud, p.LaunchArgs)
		if beforeComplete || markExtensionIntegrityIfComplete(ud, p.ProfileId, p.LaunchArgs) {
			result.Complete++
			if !beforeComplete {
				result.Repaired++ // marker refreshed only; no user data touched
			}
			continue
		}
		result.appendIncomplete(p.ProfileId, dismissed)
	}

	extensionIntegrityScanMu.Lock()
	extensionIntegrityScanDone = true
	extensionIntegrityScanMu.Unlock()

	result.Message = formatIntegrityScanMessage(result)
	log.Info("扩展完整性巡检结束",
		logger.F("complete", result.Complete),
		logger.F("incomplete", result.Incomplete),
		logger.F("repaired", result.Repaired),
		logger.F("running_skipped", result.SkippedRunning),
	)
	return result
}

// appendIncomplete records one unfinished environment. Environments the user
// explicitly dismissed stay counted in stats but are excluded from the warning
// list, so the yellow banner stops repeating for acknowledged environments
// while genuinely new ones are still surfaced.
func (r *ExtensionIntegrityScanResult) appendIncomplete(profileID string, dismissed map[string]bool) {
	r.Incomplete++
	if dismissed[profileID] {
		r.DismissedIncomplete++
		return
	}
	r.IncompleteIDs = append(r.IncompleteIDs, profileID)
}

func formatIntegrityScanMessage(r *ExtensionIntegrityScanResult) string {
	if r == nil {
		return ""
	}
	reported := len(r.IncompleteIDs)
	if reported == 0 {
		if r.Incomplete > 0 && r.DismissedIncomplete == r.Incomplete {
			return fmt.Sprintf("有 %d 个环境扩展尚未首次适配（已按你的选择不再提醒）", r.Incomplete)
		}
		return "所有环境扩展数据完整，已停止重复验证"
	}
	if r.DismissedIncomplete > 0 {
		return fmt.Sprintf("部分环境扩展尚未完成首次适配；已忽略 %d 个，剩余 %d 个待打开一次完成适配", r.DismissedIncomplete, reported)
	}
	return "部分环境扩展尚未完成首次适配；打开对应环境一次即可，完整后不再巡检"
}
