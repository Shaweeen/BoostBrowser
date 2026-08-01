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
// extension data (Preferences / Local Extension Settings).
//
// Lifecycle (why Chrome-Manager does not hit this pain):
//  1. User assigns extension → LaunchArgs get --load-extension=… and marker cleared.
//  2. User opens the environment ONCE → Chrome activates packages and writes
//     vault/settings under the profile (adapt pass).
//  3. Marker is written → later starts SKIP --load-extension injection so Chrome
//     reloads from the profile (protects wallet accounts / extension storage).
//  4. Re-assign / remove clears the marker → one more adapt pass is required.
//
// Prep skips (scan/seed) also apply when ready. Post-start tab CDP cleanup was
// removed — do not reintroduce it as a substitute for selective inject.
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

// shouldSkipLoadExtensionInjection is true when NO assigned package still needs
// a first-time --load-extension activation (all have durable profile data).
func shouldSkipLoadExtensionInjection(userDataDir, assignmentFingerprint string, launchArgs []string) bool {
	_ = assignmentFingerprint
	return len(loadExtensionDirsNeedingInject(userDataDir, launchArgs)) == 0 &&
		len(activeLoadExtensionDirs(launchArgs)) > 0
}

// stripLoadExtensionArgs removes all --load-extension flags from a launch argv.
func stripLoadExtensionArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(strings.ToLower(trimmed), "--load-extension=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// resolveExtensionPackageID returns the Chrome extension ID under the profile.
// Prefer the manifest public-key derived Web Store ID; fall back to folder name.
// Using only filepath.Base mis-identifies packages stored as "MetaMask"/"Rabby"
// and forces --load-extension on every start → duplicate toolbar icons and a
// second full-page unlock tab when last session left the first one open.
func resolveExtensionPackageID(extDir string) string {
	extDir = strings.TrimSpace(extDir)
	if extDir == "" {
		return ""
	}
	if key := readManifestPublicKey(extDir); len(key) > 0 {
		if id := extensionIDFromPublicKey(key); isWebStoreExtensionID(id) {
			return id
		}
	}
	return strings.ToLower(filepath.Base(extDir))
}

// loadExtensionDirsNeedingInject returns only packages that do NOT yet have
// durable profile state (Preferences/LES). Packages with wallet/account data
// are never re-injected — including when the user later assigns additional
// extensions (only the new ones appear in this list).
func loadExtensionDirsNeedingInject(userDataDir string, launchArgs []string) []string {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return nil
	}
	prefIDs := preferenceExtensionIDs(userDataDir)
	need := make([]string, 0, len(dirs))
	// Stable order by id for deterministic CLI.
	type item struct{ id, path string }
	list := make([]item, 0, len(dirs))
	for _, original := range dirs {
		id := resolveExtensionPackageID(original)
		list = append(list, item{id: id, path: original})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].id != list[j].id {
			return list[i].id < list[j].id
		}
		return list[i].path < list[j].path
	})
	for _, it := range list {
		// Scheme A: fully materialised in profile Extensions/ + Preferences.
		if isExtensionInstalledInProfile(userDataDir, it.path) {
			continue
		}
		if it.id != "" && profileHasExtensionData(userDataDir, it.id, prefIDs) {
			continue
		}
		// Folder basename may differ from Chrome ID; Preferences.path still
		// points at the package when already adapted.
		if preferenceReferencesExtensionPath(userDataDir, it.path) {
			continue
		}
		need = append(need, it.path)
	}
	return need
}

// applySelectiveLoadExtensionArgs is the legacy name kept for tests. Runtime
// uses applyProfileNativeExtensionLaunchArgs (Scheme A: profile install first).
// Returns injected=cliFallback, skipped=profileInstalled for compatibility.
func applySelectiveLoadExtensionArgs(args []string, userDataDir string) (next []string, injected, skipped int) {
	next, profileInstalled, cliFallback := applyProfileNativeExtensionLaunchArgs(args, userDataDir)
	return next, cliFallback, profileInstalled
}

// profileHasAnyDurableExtensionStorage reports real Chrome-written extension
// storage under the environment data dir (wallet vaults, etc.).
func profileHasAnyDurableExtensionStorage(userDataDir string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return false
	}
	for _, root := range []string{
		filepath.Join(userDataDir, "Default", "Local Extension Settings"),
		filepath.Join(userDataDir, "Local Extension Settings"),
		filepath.Join(userDataDir, "Default", "Extensions"),
		filepath.Join(userDataDir, "Extensions"),
	} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if directoryHasAnyFile(filepath.Join(root, entry.Name())) {
				return true
			}
		}
	}
	// Preferences extensions.settings with at least one entry also counts.
	return len(preferenceExtensionIDs(userDataDir)) > 0
}

// isEnvironmentHotStartSettled means the environment already holds validated
// user extension/wallet data and nothing needs first-time CLI inject. Hot start
// skips heavy prep goroutines. Scheme A: settled when every assigned package is
// already profile-installed (or durable vault data exists and no CLI needed).
func isEnvironmentHotStartSettled(userDataDir string, launchArgs []string) bool {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return isStartPrepDone(userDataDir) || profileHasAnyDurableExtensionStorage(userDataDir)
	}
	for _, packageDir := range dirs {
		if isExtensionInstalledInProfile(userDataDir, packageDir) {
			continue
		}
		// Not profile-installed yet: also accept legacy vault/prefs presence
		// without full Scheme A files (migration path).
		id := resolveExtensionPackageID(packageDir)
		prefIDs := preferenceExtensionIDs(userDataDir)
		if id != "" && profileHasExtensionData(userDataDir, id, prefIDs) {
			continue
		}
		if preferenceReferencesExtensionPath(userDataDir, packageDir) {
			continue
		}
		return false
	}
	return true
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

// preferenceReferencesExtensionPath reports that Chrome already registered this
// unpacked package (extensions.settings[*].path). Used when the package folder
// name is not the Web Store ID but the profile already adapted the wallet.
func preferenceReferencesExtensionPath(userDataDir, extDir string) bool {
	target := normalizeExtensionPath(extDir)
	if target == "" {
		return false
	}
	targetLower := strings.ToLower(target)
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
		for _, raw := range settings {
			entry, _ := raw.(map[string]any)
			if entry == nil {
				continue
			}
			p, _ := entry["path"].(string)
			p = normalizeExtensionPath(p)
			if p == "" {
				continue
			}
			pl := strings.ToLower(p)
			if pl == targetLower || strings.HasPrefix(pl, targetLower+string(os.PathSeparator)) || strings.HasPrefix(targetLower, pl+string(os.PathSeparator)) {
				return true
			}
		}
	}
	return false
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
// waits briefly for Chrome to persist Preferences/LES, verifies packages ↔
// profile data, then marks ready so later starts skip --load-extension inject
// and prep. Existing user vaults, cookies and social session state are never
// overwritten.
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
	// Give Chrome time to flush extension registration + vault paths after the
	// adapt start (CLI --load-extension first activation). Do not mark ready
	// until Preferences/LES show real profile state for assigned web-store IDs.
	aligned := false
	for attempt := 0; attempt < 16; attempt++ {
		if verifyAssignedExtensionsAgainstProfileData(userDataDir, launchArgs) &&
			extensionProfileDataLooksAdapted(userDataDir, launchArgs) {
			aligned = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !aligned {
		logger.New("Extension").Info("扩展数据尚未与分配列表对齐，保持下次启动仍注入 --load-extension（不覆盖已有用户数据）",
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
	logger.New("Extension").Info("扩展已完成首次适配：后续启动将跳过 --load-extension 注入与扫描，保留钱包/账号状态",
		logger.F("profile_id", profileID),
		logger.F("extension_count", len(extensionIDs)),
	)
}

// extensionProfileDataLooksAdapted reports that Chrome (or a prior adapt) left
// durable per-extension state under the profile for assigned web-store IDs.
func extensionProfileDataLooksAdapted(userDataDir string, launchArgs []string) bool {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return true
	}
	prefIDs := preferenceExtensionIDs(userDataDir)
	need := 0
	have := 0
	for _, original := range dirs {
		id := strings.ToLower(filepath.Base(strings.TrimSpace(original)))
		if !isWebStoreExtensionID(id) {
			continue
		}
		need++
		if profileHasExtensionData(userDataDir, id, prefIDs) {
			have++
		}
	}
	if need == 0 {
		// Custom folder names: package + marker verification is enough.
		return true
	}
	return have == need
}
