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
)

// noticeDismissMu serializes read-modify-write of the dismiss state file so
// concurrent scan/dismiss/import calls cannot lose an acknowledged entry.
var noticeDismissMu sync.Mutex

// Persistent user dismissal record for one-shot notices. The client never
// repeats a notice the user explicitly acknowledged. This file is the "记录"
// for future upgrades: entries stay until the user changes their mind, and
// cleanup tooling can consult it before archiving abandoned data.
const noticeDismissStateFile = ".boost_notice_dismissed.json"

type noticeDismissState struct {
	// ExtensionIncomplete are environment IDs the user told the integrity scan
	// to stop reporting as "尚未完成首次适配".
	ExtensionIncomplete []string `json:"extensionIncomplete"`
	// LegacyFolders are relative Chrome data folders under the data root the
	// user chose not to import. Dismissal never deletes user data.
	LegacyFolders []string `json:"legacyFolders"`
	// LegacyScanPending is set after the user deletes an environment so the
	// UI may run one orphan-folder scan. Cleared after scan/import/dismiss.
	// Startup must NOT scan when this is false.
	LegacyScanPending bool   `json:"legacyScanPending"`
	UpdatedAt         string `json:"updatedAt"`
}

func (a *App) noticeDismissPath() string {
	return a.resolveAppPath(filepath.Join("data", noticeDismissStateFile))
}

func (a *App) readNoticeDismissal() noticeDismissState {
	state := noticeDismissState{}
	data, err := os.ReadFile(a.noticeDismissPath())
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return state
	}
	_ = json.Unmarshal(data, &state)
	return state
}

func (a *App) writeNoticeDismissal(state noticeDismissState) error {
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(a.noticeDismissPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(a.noticeDismissPath(), data, 0o600)
}

func (a *App) dismissedExtensionIncompleteSet() map[string]bool {
	state := a.readNoticeDismissal()
	out := make(map[string]bool, len(state.ExtensionIncomplete))
	for _, id := range state.ExtensionIncomplete {
		if strings.TrimSpace(id) != "" {
			out[strings.TrimSpace(id)] = true
		}
	}
	return out
}

func (a *App) dismissedLegacyFolderSet() map[string]bool {
	state := a.readNoticeDismissal()
	out := make(map[string]bool, len(state.LegacyFolders))
	for _, key := range state.LegacyFolders {
		if strings.TrimSpace(key) != "" {
			out[strings.TrimSpace(key)] = true
		}
	}
	return out
}

// BrowserExtensionIntegrityDismissNotice records environments whose "首次适配"
// notice the user acknowledged. The one-shot scan will not report them again
// (they are still scanned and repaired silently). New environments created
// later are never pre-dismissed, so genuinely new work is still surfaced.
func (a *App) BrowserExtensionIntegrityDismissNotice(profileIds []string) error {
	if a == nil {
		return fmt.Errorf("应用尚未初始化")
	}
	noticeDismissMu.Lock()
	defer noticeDismissMu.Unlock()
	state := a.readNoticeDismissal()
	kept := make([]string, 0, len(state.ExtensionIncomplete))
	for _, id := range state.ExtensionIncomplete {
		if strings.TrimSpace(id) != "" {
			kept = append(kept, id)
		}
	}
	seen := make(map[string]bool, len(kept)+len(profileIds))
	for _, id := range kept {
		seen[id] = true
	}
	for _, id := range profileIds {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		kept = append(kept, id)
	}
	state.ExtensionIncomplete = kept
	return a.writeNoticeDismissal(state)
}

// BrowserExtensionIntegrityClearDismissed removes all acknowledged
// environments so the notice reappears (e.g. after the user changed their
// mind about which environments matter).
func (a *App) BrowserExtensionIntegrityClearDismissed() error {
	if a == nil {
		return fmt.Errorf("应用尚未初始化")
	}
	noticeDismissMu.Lock()
	defer noticeDismissMu.Unlock()
	state := a.readNoticeDismissal()
	state.ExtensionIncomplete = nil
	return a.writeNoticeDismissal(state)
}

// ============================================================================
// Legacy-data scan: unregistered Chrome folders under the data root (typically
// left after an environment was deleted or app.db was lost).
//
// Policy (product):
//   - Do NOT scan on every client startup.
//   - Scan only after the user explicitly starts it from Settings.
//   - "Ignore / do not import" only records the keys so they do not reappear.
//     It never deletes browser, extension, wallet, cookie, or login data.
// ============================================================================

type LegacyDataAutoFolder struct {
	FolderKey   string `json:"folderKey"`
	FolderName  string `json:"folderName"`
	ProfileName string `json:"profileName"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type LegacyDataAutoPreview struct {
	Folders   []LegacyDataAutoFolder `json:"folders"`
	Dismissed int                    `json:"dismissed"`
	// Pending is true when a post-delete scan is armed (or force=true).
	Pending bool   `json:"pending"`
	Message string `json:"message"`
}

func (a *App) scanLegacyDataAuto() ([]LegacyDataAutoFolder, int) {
	if a == nil || a.browserMgr == nil || a.config == nil {
		return nil, 0
	}
	activeRoot := a.backupResolveUserDataRoot(a.config)
	rels, err := legacyRawDataFolders(activeRoot)
	if err != nil {
		return nil, 0
	}
	dismissed := a.dismissedLegacyFolderSet()

	// Every registered environment owns its resolved data dir; anything under
	// the data root that is not one of those is a candidate for import.
	registered := make(map[string]bool)
	for _, p := range a.browserMgr.List() {
		dir := backupNormalizePath(a.browserMgr.ResolveUserDataDir(&p))
		if dir != "" {
			registered[dir] = true
		}
	}

	folders := make([]LegacyDataAutoFolder, 0, len(rels))
	skipDismissed := 0
	for _, rel := range rels {
		key := filepath.ToSlash(filepath.Clean(rel))
		abs := backupNormalizePath(filepath.Join(activeRoot, filepath.FromSlash(key)))
		if registered[abs] {
			continue
		}
		if dismissed[key] {
			skipDismissed++
			continue
		}
		profile := legacyRawFolderProfile(activeRoot, filepath.FromSlash(key))
		size := legacyFolderSizeBytes(filepath.Join(activeRoot, filepath.FromSlash(key)))
		folders = append(folders, LegacyDataAutoFolder{
			FolderKey:   key,
			FolderName:  filepath.Base(filepath.FromSlash(key)),
			ProfileName: profile.ProfileName,
			SizeBytes:   size,
		})
	}
	return folders, skipDismissed
}

func (a *App) clearLegacyScanPendingLocked() {
	state := a.readNoticeDismissal()
	if !state.LegacyScanPending {
		return
	}
	state.LegacyScanPending = false
	_ = a.writeNoticeDismissal(state)
}

func (a *App) legacyScanPending() bool {
	return a.readNoticeDismissal().LegacyScanPending
}

// BrowserLegacyDataAutoScan returns unregistered Chrome data folders.
// force=false: compatibility no-op retained for older frontend bindings.
// force=true: settings/manual scan, ignores pending gate.
func (a *App) BrowserLegacyDataAutoScan(force bool) (*LegacyDataAutoPreview, error) {
	if a == nil {
		return nil, fmt.Errorf("应用尚未初始化")
	}
	pending := a.legacyScanPending()
	preview := &LegacyDataAutoPreview{Pending: pending || force}
	if !force && !pending {
		preview.Message = "未执行数据扫描；请在设置页手动触发"
		preview.Folders = []LegacyDataAutoFolder{}
		return preview, nil
	}
	folders, dismissed := a.scanLegacyDataAuto()
	preview.Folders = folders
	preview.Dismissed = dismissed
	if len(folders) == 0 {
		// Nothing to offer: clear pending so we do not re-prompt next open.
		noticeDismissMu.Lock()
		a.clearLegacyScanPendingLocked()
		noticeDismissMu.Unlock()
		preview.Pending = false
		preview.Message = "未发现需要导入的残留数据"
		if dismissed > 0 {
			preview.Message = fmt.Sprintf("未发现可导入残留；已忽略记录 %d 个", dismissed)
		}
		return preview, nil
	}
	preview.Message = fmt.Sprintf("识别到 %d 个未关联数据文件夹。可导入为环境，或仅记录忽略（不删除文件）", len(folders))
	return preview, nil
}

// BrowserLegacyDataDismissFolders records ignored orphan folders. It never
// removes data from disk; users may re-enable suggestions later.
func (a *App) BrowserLegacyDataDismissFolders(folderKeys []string) error {
	if a == nil || a.config == nil {
		return fmt.Errorf("应用尚未初始化")
	}
	noticeDismissMu.Lock()
	defer noticeDismissMu.Unlock()

	state := a.readNoticeDismissal()
	seen := make(map[string]bool, len(state.LegacyFolders)+len(folderKeys))
	for _, key := range state.LegacyFolders {
		if strings.TrimSpace(key) != "" {
			seen[key] = true
		}
	}
	for _, key := range folderKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		// Store only safe relative keys. No filesystem mutation is performed.
		rel := filepath.Clean(filepath.FromSlash(key))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "..") {
			continue
		}
		if !seen[key] {
			seen[key] = true
			state.LegacyFolders = append(state.LegacyFolders, filepath.ToSlash(key))
		}
	}
	state.LegacyScanPending = false
	if err := a.writeNoticeDismissal(state); err != nil {
		return err
	}
	return nil
}

// BrowserLegacyDataImportFolders attaches unregistered Chrome data folders as
// new environments in place (no copy, no overwrite). Metadata is rebuilt from
// the folder contents; Cookies/IndexedDB/wallet storage stay byte-for-byte.
func (a *App) BrowserLegacyDataImportFolders(folderKeys []string) (*LegacyDataAutoPreview, error) {
	if a == nil || a.browserMgr == nil || a.db == nil {
		return nil, fmt.Errorf("应用尚未初始化")
	}
	activeRoot := a.backupResolveUserDataRoot(a.config)
	imported := 0
	failed := 0
	seenKeys := make(map[string]bool, len(folderKeys))
	noticeDismissMu.Lock()
	defer noticeDismissMu.Unlock()
	for _, raw := range folderKeys {
		key := strings.TrimSpace(raw)
		if key == "" || seenKeys[key] {
			continue
		}
		seenKeys[key] = true
		rel := filepath.FromSlash(key)
		dir := filepath.Join(activeRoot, rel)
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			failed++
			continue
		}
		profile := legacyRawFolderProfile(activeRoot, rel)
		profile.Running, profile.DebugReady, profile.DebugPort, profile.Pid = false, false, 0, 0
		profile.UserDataDir = filepath.Clean(rel)
		if err := a.browserMgr.ProfileDAO.Upsert(profile); err != nil {
			failed++
			continue
		}
		imported++
		// Imported folders are no longer "leftover": drop them from the
		// dismissed set (if present) so they are never suggested again.
		state := a.readNoticeDismissal()
		kept := make([]string, 0, len(state.LegacyFolders))
		for _, old := range state.LegacyFolders {
			if old != key {
				kept = append(kept, old)
			}
		}
		state.LegacyFolders = kept
		_ = a.writeNoticeDismissal(state)
	}
	if imported > 0 {
		// Reload the in-memory profile map from the DAO so the newly registered
		// environments are immediately visible (InitData alone skips reload when
		// profiles already exist).
		a.browserMgr.ReloadProfilesFromDAO()
	}
	a.clearLegacyScanPendingLocked()
	folders, dismissed := a.scanLegacyDataAuto()
	preview := &LegacyDataAutoPreview{Folders: folders, Dismissed: dismissed, Pending: false}
	preview.Message = fmt.Sprintf("旧数据导入完成：成功 %d，失败 %d", imported, failed)
	return preview, nil
}

// BrowserLegacyDataClearDismissed lets the user re-enable leftover-data
// suggestions that were previously ignored.
func (a *App) BrowserLegacyDataClearDismissed() error {
	if a == nil {
		return fmt.Errorf("应用尚未初始化")
	}
	noticeDismissMu.Lock()
	defer noticeDismissMu.Unlock()
	state := a.readNoticeDismissal()
	state.LegacyFolders = nil
	return a.writeNoticeDismissal(state)
}

func legacyFolderSizeBytes(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
