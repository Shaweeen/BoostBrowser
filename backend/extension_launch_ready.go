package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
)

// Per-profile marker written after the first successful post-assignment launch
// has verified that assigned --load-extension packages correspond to profile
// extension data. Subsequent starts skip extension package repair, startup-tab
// CDP sweeps, bookmark re-merge and search-engine re-seed. Browser launch,
// window size/position and multi-environment popup confinement stay active.
const extensionLaunchReadyMarkerName = ".boost_extension_launch_ready"

type extensionLaunchReadyMarker struct {
	AssignmentFingerprint string   `json:"assignmentFingerprint"`
	ExtensionIDs          []string `json:"extensionIds"`
	VerifiedAt            string   `json:"verifiedAt"`
	ProfileID             string   `json:"profileId,omitempty"`
}

func extensionLaunchReadyMarkerPath(userDataDir string) string {
	return filepath.Join(strings.TrimSpace(userDataDir), extensionLaunchReadyMarkerName)
}

// assignmentFingerprintFromLaunchArgs hashes the ordered set of load-extension
// directories and their folder basenames (expected Chrome extension IDs).
func assignmentFingerprintFromLaunchArgs(args []string) (fingerprint string, extensionIDs []string) {
	dirs := activeLoadExtensionDirs(args)
	if len(dirs) == 0 {
		return "", nil
	}
	type item struct {
		id   string
		path string
	}
	items := make([]item, 0, len(dirs))
	for norm, original := range dirs {
		base := strings.ToLower(filepath.Base(original))
		if base == "" || base == "." || base == string(filepath.Separator) {
			base = strings.ToLower(filepath.Base(norm))
		}
		items = append(items, item{id: base, path: strings.ToLower(filepath.Clean(original))})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].id != items[j].id {
			return items[i].id < items[j].id
		}
		return items[i].path < items[j].path
	})
	ids := make([]string, 0, len(items))
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(it.id)
		b.WriteByte('@')
		b.WriteString(it.path)
		ids = append(ids, it.id)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), ids
}

func readExtensionLaunchReadyMarker(userDataDir string) (extensionLaunchReadyMarker, bool) {
	data, err := os.ReadFile(extensionLaunchReadyMarkerPath(userDataDir))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return extensionLaunchReadyMarker{}, false
	}
	var marker extensionLaunchReadyMarker
	if json.Unmarshal(data, &marker) != nil {
		return extensionLaunchReadyMarker{}, false
	}
	if strings.TrimSpace(marker.AssignmentFingerprint) == "" {
		return extensionLaunchReadyMarker{}, false
	}
	return marker, true
}

func isExtensionLaunchPrepReady(userDataDir, assignmentFingerprint string) bool {
	assignmentFingerprint = strings.TrimSpace(assignmentFingerprint)
	// No --load-extension packages: once first-start prep marker exists, skip
	// Preferences rewrites and other slow prep on every launch.
	if assignmentFingerprint == "" {
		return isStartPrepDone(userDataDir)
	}
	marker, ok := readExtensionLaunchReadyMarker(userDataDir)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(marker.AssignmentFingerprint), assignmentFingerprint)
}

// Lightweight one-time start prep marker (session restore sanitization etc.).
// Independent of extension assignment fingerprint.
const startPrepDoneMarkerName = ".boost_start_prep_done"

func isStartPrepDone(userDataDir string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(userDataDir, startPrepDoneMarkerName))
	return err == nil
}

func markStartPrepDone(userDataDir string) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return
	}
	_ = os.MkdirAll(userDataDir, 0755)
	_ = os.WriteFile(filepath.Join(userDataDir, startPrepDoneMarkerName), []byte("1\n"), 0600)
}

func clearExtensionLaunchReadyMarker(userDataDir string) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return
	}
	_ = os.Remove(extensionLaunchReadyMarkerPath(userDataDir))
}

func writeExtensionLaunchReadyMarker(userDataDir, profileID, assignmentFingerprint string, extensionIDs []string) error {
	userDataDir = strings.TrimSpace(userDataDir)
	assignmentFingerprint = strings.TrimSpace(assignmentFingerprint)
	if userDataDir == "" || assignmentFingerprint == "" {
		return nil
	}
	if err := os.MkdirAll(userDataDir, 0755); err != nil {
		return err
	}
	marker := extensionLaunchReadyMarker{
		AssignmentFingerprint: assignmentFingerprint,
		ExtensionIDs:          append([]string{}, extensionIDs...),
		VerifiedAt:            time.Now().UTC().Format(time.RFC3339),
		ProfileID:             strings.TrimSpace(profileID),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(extensionLaunchReadyMarkerPath(userDataDir), data, 0600)
}

// verifyAssignedExtensionsAgainstProfileData checks that every assigned
// extension package is present and safe for launch. Policy:
//   - manifest.json must exist;
//   - Web Store-style folders should have a stable public key (or already have
//     profile vault/settings under that ID — never overwrite those vaults);
//   - missing LES on first open is OK once the package ID is stable, so Chrome
//     can create vaults under the correct path on first use.
// Existing vault/account files are never modified here.
func verifyAssignedExtensionsAgainstProfileData(userDataDir string, launchArgs []string) bool {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return true
	}
	prefIDs := preferenceExtensionIDs(userDataDir)
	for _, original := range dirs {
		extDir := strings.TrimSpace(original)
		if extDir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(extDir, "manifest.json")); err != nil {
			return false
		}
		id := strings.ToLower(filepath.Base(extDir))
		if !isWebStoreExtensionID(id) {
			// Custom folder names: live package is enough.
			continue
		}
		// Vault/settings already present under this ID — package alignment OK.
		if profileHasExtensionData(userDataDir, id, prefIDs) {
			continue
		}
		// First open before Chrome writes LES: require stable package ID so the
		// first vault write uses the correct path. Do not fail forever.
		if !extensionManifestHasStableKey(extDir, id) {
			return false
		}
	}
	return true
}

// completeAssignedExtensionProfileData fills missing non-secret scaffolding so
// the assigned extension list can align with the environment. Policy:
//   - repair package manifest public keys when missing (program package only);
//   - create empty Local Extension Settings/<id> directories only when absent;
//   - never overwrite, truncate, or delete existing Preferences entries,
//     Local Extension Settings contents, Cookies, IndexedDB, or wallet state;
//   - never write fake vault/account payloads.
// Returns how many packages lacked a stable key before repair, and how many
// empty LES directories were created.
func (a *App) completeAssignedExtensionProfileData(userDataDir string, launchArgs []string) (missingKeysBefore, createdScaffolds int) {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return 0, 0
	}
	for _, original := range dirs {
		extDir := strings.TrimSpace(original)
		if extDir == "" {
			continue
		}
		id := strings.ToLower(filepath.Base(extDir))
		if isWebStoreExtensionID(id) && !extensionManifestHasStableKey(extDir, id) {
			missingKeysBefore++
		}
	}
	// Package metadata only — never reads or writes profile vaults/Cookies.
	if a != nil {
		a.repairLoadExtensionStableIDs(launchArgs)
	}
	for _, original := range dirs {
		extDir := strings.TrimSpace(original)
		if extDir == "" {
			continue
		}
		id := strings.ToLower(filepath.Base(extDir))
		if !isWebStoreExtensionID(id) {
			continue
		}
		if ensureEmptyExtensionSettingsScaffold(userDataDir, id) {
			createdScaffolds++
		}
	}
	// Never rewrite Preferences here. This helper may run while Chrome already
	// owns the profile after start; concurrent Preferences RMW can corrupt or
	// discard extension/wallet metadata Chrome just wrote.
	return missingKeysBefore, createdScaffolds
}

// ensureEmptyExtensionSettingsScaffold creates Local Extension Settings/<id>
// only when the directory is completely missing. If any path already exists
// (even empty), it is left untouched so wallets/social state cannot be replaced.
func ensureEmptyExtensionSettingsScaffold(userDataDir, extensionID string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	extensionID = strings.ToLower(strings.TrimSpace(extensionID))
	if userDataDir == "" || !isWebStoreExtensionID(extensionID) {
		return false
	}
	// Prefer the standard Chrome profile layout.
	target := filepath.Join(userDataDir, "Default", "Local Extension Settings", extensionID)
	if st, err := os.Stat(target); err == nil {
		_ = st
		return false // already present — never touch contents
	} else if !os.IsNotExist(err) {
		return false
	}
	// Also skip if a legacy non-Default path already holds state.
	legacy := filepath.Join(userDataDir, "Local Extension Settings", extensionID)
	if _, err := os.Stat(legacy); err == nil {
		return false
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return false
	}
	return true
}

// mergeExtensionSettingsNeverOverwrite ensures Preferences only gains missing
// structural keys (extensions.ui.developer_mode). It never replaces an existing
// extensions.settings[id] entry or any nested account/wallet fields.
func mergeExtensionSettingsNeverOverwrite(userDataDir string, extensionIDs []string) (changed bool) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return false
	}
	// Only ensure developer mode UI flag so unpacked --load-extension works.
	// Do not invent extensions.settings rows — Chrome owns those after first run.
	_ = extensionIDs
	prefPath := filepath.Join(userDataDir, "Default", "Preferences")
	if _, err := os.Stat(prefPath); err != nil {
		// Do not create a brand-new Preferences just to write flags when Chrome
		// has never started; sanitize/ensure paths handle first-run creation.
		return false
	}
	data, err := os.ReadFile(prefPath)
	if err != nil || len(data) == 0 {
		return false
	}
	var prefs map[string]any
	if json.Unmarshal(data, &prefs) != nil {
		return false
	}
	extRoot := ensureJSONMap(prefs, "extensions")
	ui := ensureJSONMap(extRoot, "ui")
	if ui["developer_mode"] == true {
		return false
	}
	// settings map must remain if present
	if settings, ok := extRoot["settings"].(map[string]any); ok && settings != nil {
		// Explicit no-op on existing settings entries (preserve wallets/social).
		_ = settings
	}
	ui["developer_mode"] = true
	out, err := json.MarshalIndent(prefs, "", "   ")
	if err != nil {
		return false
	}
	if err := fsutil.WriteFileAtomic(prefPath, out, 0644); err != nil {
		return false
	}
	return true
}

func preferenceExtensionIDs(userDataDir string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, path := range chromeProfilePreferencePaths(userDataDir) {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		var prefs map[string]any
		if json.Unmarshal(data, &prefs) != nil {
			continue
		}
		extRoot, _ := prefs["extensions"].(map[string]any)
		if extRoot == nil {
			continue
		}
		settings, _ := extRoot["settings"].(map[string]any)
		for id := range settings {
			id = strings.ToLower(strings.TrimSpace(id))
			if id != "" {
				out[id] = struct{}{}
			}
		}
	}
	return out
}

func profileHasExtensionData(userDataDir, extensionID string, prefIDs map[string]struct{}) bool {
	extensionID = strings.ToLower(strings.TrimSpace(extensionID))
	if extensionID == "" {
		return false
	}
	if _, ok := prefIDs[extensionID]; ok {
		return true
	}
	// Chrome writes vault-like state under Local Extension Settings/<id>.
	// Empty scaffold directories created by us do NOT count — require real content
	// so wallets/social accounts are only treated as present when Chrome wrote them.
	candidates := []string{
		filepath.Join(userDataDir, "Default", "Local Extension Settings", extensionID),
		filepath.Join(userDataDir, "Local Extension Settings", extensionID),
		filepath.Join(userDataDir, "Default", "Extensions", extensionID),
		filepath.Join(userDataDir, "Extensions", extensionID),
	}
	for _, dir := range candidates {
		if directoryHasAnyFile(dir) {
			return true
		}
	}
	return false
}

func directoryHasAnyFile(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true
		}
		// One level of nested Chrome LevelDB/state folders counts as real data.
		if directoryHasAnyFile(filepath.Join(dir, entry.Name())) {
			return true
		}
	}
	return false
}

// clearExtensionLaunchReadyForProfiles takes browserMgr.Mutex.
func (a *App) clearExtensionLaunchReadyForProfiles(profileIDs []string) {
	if a == nil || a.browserMgr == nil || len(profileIDs) == 0 {
		return
	}
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	a.clearExtensionLaunchReadyForProfilesLocked(profileIDs)
}

// clearExtensionLaunchReadyForProfilesLocked requires browserMgr.Mutex held.
func (a *App) clearExtensionLaunchReadyForProfilesLocked(profileIDs []string) {
	if a == nil || a.browserMgr == nil || len(profileIDs) == 0 {
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
		userDataDir := a.browserMgr.ResolveUserDataDir(profile)
		if userDataDir == "" {
			continue
		}
		clearExtensionLaunchReadyMarker(userDataDir)
		logger.New("Extension").Info("扩展分配已变更，下次启动将重新校验环境扩展数据",
			logger.F("profile_id", id),
		)
	}
}

// maybeMarkExtensionLaunchReady runs after a first post-assignment start when
// prep work still ran. It non-destructively completes missing scaffolding,
// verifies packages ↔ profile data, then marks ready so later starts skip prep.
// Existing user vaults, cookies and social session state are never overwritten.
func (a *App) maybeMarkExtensionLaunchReady(profileID, userDataDir, assignmentFingerprint string, launchArgs []string, extensionIDs []string) {
	if strings.TrimSpace(assignmentFingerprint) == "" || strings.TrimSpace(userDataDir) == "" {
		return
	}
	missingKeys, scaffolds := a.completeAssignedExtensionProfileData(userDataDir, launchArgs)
	if missingKeys > 0 || scaffolds > 0 {
		logger.New("Extension").Info("已非破坏性补全扩展清单相关数据",
			logger.F("profile_id", profileID),
			logger.F("packages_missing_key_before", missingKeys),
			logger.F("empty_settings_dirs_created", scaffolds),
		)
	}
	if !verifyAssignedExtensionsAgainstProfileData(userDataDir, launchArgs) {
		logger.New("Extension").Info("扩展数据尚未与分配列表对齐，保持启动前校验（不覆盖已有用户数据）",
			logger.F("profile_id", profileID),
		)
		return
	}
	if err := writeExtensionLaunchReadyMarker(userDataDir, profileID, assignmentFingerprint, extensionIDs); err != nil {
		logger.New("Extension").Warn("写入扩展启动就绪标记失败",
			logger.F("profile_id", profileID),
			logger.F("error", err.Error()),
		)
		return
	}
	logger.New("Extension").Info("扩展分配已与环境数据对齐，后续启动跳过扩展扫描/修复；已有钱包与账号状态保留",
		logger.F("profile_id", profileID),
		logger.F("extension_count", len(extensionIDs)),
	)
}
