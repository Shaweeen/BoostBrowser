package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/database"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const legacyDataRecoveryTTL = 30 * time.Minute

type LegacyDataRecoveryRow struct {
	EnvironmentNumber int    `json:"environmentNumber"`
	ProfileID         string `json:"profileId"`
	ProfileName       string `json:"profileName"`
	UserDataDir       string `json:"userDataDir"`
	SourceFolderName  string `json:"sourceFolderName"`
	TargetProfileID   string `json:"targetProfileId"`
	TargetProfileName string `json:"targetProfileName"`
	TargetNumber      int    `json:"targetNumber"`
	Overwrite         bool   `json:"overwrite"`
	DirectoryExists   bool   `json:"directoryExists"`
	Status            string `json:"status"`
	Message           string `json:"message"`
}

type LegacyDataRecoveryPreview struct {
	Cancelled  bool                    `json:"cancelled"`
	SessionID  string                  `json:"sessionId"`
	SourcePath string                  `json:"sourcePath"`
	Total      int                     `json:"total"`
	Restorable int                     `json:"restorable"`
	Conflicts  int                     `json:"conflicts"`
	Missing    int                     `json:"missing"`
	Rows       []LegacyDataRecoveryRow `json:"rows"`
	Message    string                  `json:"message"`
}

type LegacyDataRecoveryResult struct {
	Imported   int                     `json:"imported"`
	Skipped    int                     `json:"skipped"`
	Failed     int                     `json:"failed"`
	BackupPath string                  `json:"backupPath"`
	Rows       []LegacyDataRecoveryRow `json:"rows"`
	Message    string                  `json:"message"`
}

type legacyDataRecoveryCandidate struct {
	Profile           *browser.Profile
	TargetProfile     *browser.Profile
	EnvironmentNumber int
	TargetNumber      int
	SourceFolderName  string
	SourceDir         string
	DestinationDir    string
	RegisteredDir     string
	Status            string
	Message           string
}

type legacyDataRecoverySession struct {
	ID         string
	CreatedAt  time.Time
	SourcePath string
	TempRoot   string
	Candidates []*legacyDataRecoveryCandidate
	Timer      *time.Timer
}

// LegacyDataRecoveryPrepare selects and validates a raw data directory from an
// older installation. The source is never modified. app.db supplies exact
// metadata when available; otherwise Chrome data folders are mapped by name.
func (a *App) LegacyDataRecoveryPrepare() (*LegacyDataRecoveryPreview, error) {
	if a == nil || a.ctx == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("应用尚未初始化")
	}
	selected, err := a.selectLegacyDataDirectory()
	if err != nil {
		return nil, err
	}
	if selected == "" {
		return &LegacyDataRecoveryPreview{Cancelled: true, Message: "已取消旧数据识别"}, nil
	}
	return a.legacyDataRecoveryPreparePath(selected)
}

func (a *App) legacyDataRecoveryPreparePath(selected string) (*LegacyDataRecoveryPreview, error) {
	sourceRoot, dbPath, err := legacyResolveDataRoot(selected)
	if err != nil {
		return nil, err
	}
	activeRoot := a.backupResolveUserDataRoot(a.config)
	if backupSamePath(sourceRoot, activeRoot) || dbPath != "" && backupSamePath(dbPath, a.backupResolveDBPath(a.config)) {
		return nil, fmt.Errorf("所选目录是当前正在使用的 data，请选择旧版备份目录")
	}

	tempRoot, err := os.MkdirTemp("", "browserstudio-legacy-data-")
	if err != nil {
		return nil, fmt.Errorf("创建校验目录失败: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tempRoot)
		}
	}()
	profiles := make([]*browser.Profile, 0)
	if dbPath != "" {
		tempDB := filepath.Join(tempRoot, "app.db")
		if err := legacyCopySQLiteSet(dbPath, tempDB); err != nil {
			return nil, err
		}
		sourceDB, openErr := database.NewDB(tempDB)
		if openErr != nil {
			return nil, fmt.Errorf("旧版 app.db 无法打开: %w", openErr)
		}
		if migrateErr := sourceDB.Migrate(); migrateErr != nil {
			_ = sourceDB.Close()
			return nil, fmt.Errorf("旧版 app.db 结构无法升级识别: %w", migrateErr)
		}
		profiles, err = browser.NewSQLiteProfileDAO(sourceDB.GetConn()).List()
		_ = sourceDB.Close()
		if err != nil {
			return nil, fmt.Errorf("读取旧版环境清单失败: %w", err)
		}
	} else {
		profiles, err = legacyProfilesFromRawFolders(sourceRoot)
		if err != nil {
			return nil, err
		}
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("旧版 app.db 中没有可恢复的浏览器环境")
	}

	sourceMap := make(map[string]*browser.Profile, len(profiles))
	for _, profile := range profiles {
		if profile != nil {
			sourceMap[profile.ProfileId] = profile
		}
	}
	a.browserMgr.Mutex.Lock()
	currentMap := make(map[string]*browser.Profile, len(a.browserMgr.Profiles))
	currentNumbers := make(map[string]int, len(a.browserMgr.Profiles))
	currentByFolder := make(map[string][]*browser.Profile, len(a.browserMgr.Profiles))
	currentDirs := make(map[string]bool, len(a.browserMgr.Profiles))
	for id, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		currentMap[id] = profile
		currentNumbers[id] = resolveBadgeDisplayNumber(id, profile.ProfileName, a.browserMgr.Profiles)
		folderKey := legacyDataFolderKey(profile.UserDataDir, profile.ProfileId)
		currentByFolder[folderKey] = append(currentByFolder[folderKey], profile)
		currentDirs[backupNormalizePath(a.browserMgr.ResolveUserDataDir(profile))] = true
	}
	a.browserMgr.Mutex.Unlock()

	candidates := make([]*legacyDataRecoveryCandidate, 0, len(profiles))
	reservedDestinations := make(map[string]bool, len(profiles))
	reservedTargets := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		if profile == nil || strings.TrimSpace(profile.ProfileId) == "" {
			continue
		}
		number := resolveBadgeDisplayNumber(profile.ProfileId, profile.ProfileName, sourceMap)
		sourceDir := legacyResolveProfileSourceDir(sourceRoot, profile)
		sourceFolderName := filepath.Base(filepath.Clean(sourceDir))
		registeredDir, destinationDir := legacyResolveProfileDestination(activeRoot, profile)
		candidate := &legacyDataRecoveryCandidate{
			Profile: profile, EnvironmentNumber: number, SourceDir: sourceDir,
			SourceFolderName: sourceFolderName, DestinationDir: destinationDir, RegisteredDir: registeredDir, Status: "ready", Message: "将新建恢复环境",
		}
		folderMatches := currentByFolder[legacyDataFolderKey(sourceFolderName, profile.ProfileId)]
		if info, statErr := os.Lstat(sourceDir); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			candidate.Status, candidate.Message = "missing", "备份浏览器数据目录不存在"
		} else if len(folderMatches) == 1 {
			target := folderMatches[0]
			if reservedTargets[target.ProfileId] {
				candidate.Status, candidate.Message = "conflict", "多个备份目录指向同一当前环境，已停止覆盖"
			} else {
				candidate.TargetProfile = target
				candidate.TargetNumber = currentNumbers[target.ProfileId]
				candidate.RegisteredDir = target.UserDataDir
				candidate.DestinationDir = a.browserMgr.ResolveUserDataDir(target)
				candidate.Status = "overwrite"
				candidate.Message = fmt.Sprintf("按文件夹 %s 覆盖当前环境 #%d %s", sourceFolderName, candidate.TargetNumber, target.ProfileName)
				reservedTargets[target.ProfileId] = true
			}
		} else if len(folderMatches) > 1 {
			candidate.Status, candidate.Message = "conflict", "当前多个环境使用同名数据文件夹，无法唯一判断覆盖目标"
		} else if _, exists := currentMap[profile.ProfileId]; exists {
			candidate.Status, candidate.Message = "conflict", "环境 ID 已存在但数据文件夹名不同，请先将当前环境数据目录设为备份文件夹名"
		} else if currentDirs[backupNormalizePath(destinationDir)] || reservedDestinations[backupNormalizePath(destinationDir)] || backupPathExists(destinationDir) {
			candidate.Status, candidate.Message = "conflict", "目标浏览器数据目录已存在，禁止覆盖"
		} else {
			reservedDestinations[backupNormalizePath(destinationDir)] = true
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].EnvironmentNumber < candidates[j].EnvironmentNumber })

	sessionID, err := legacyRandomID()
	if err != nil {
		return nil, err
	}
	session := &legacyDataRecoverySession{ID: sessionID, CreatedAt: time.Now(), SourcePath: sourceRoot, TempRoot: tempRoot, Candidates: candidates}
	session.Timer = time.AfterFunc(legacyDataRecoveryTTL, func() { a.clearLegacyDataRecoverySession(sessionID) })
	a.legacyRecoveryMu.Lock()
	old := a.legacyRecovery
	a.legacyRecovery = session
	a.legacyRecoveryMu.Unlock()
	legacyDisposeRecoverySession(old)
	cleanup = false
	return legacyRecoveryPreview(session), nil
}

func (a *App) LegacyDataRecoveryExecute(sessionID string) (*LegacyDataRecoveryResult, error) {
	if a == nil || a.browserMgr == nil || a.db == nil {
		return nil, fmt.Errorf("应用尚未初始化")
	}
	sessionID = strings.TrimSpace(sessionID)
	a.legacyRecoveryMu.Lock()
	session := a.legacyRecovery
	if session != nil && session.ID == sessionID {
		a.legacyRecovery = nil
	}
	a.legacyRecoveryMu.Unlock()
	if session == nil || session.ID != sessionID || time.Since(session.CreatedAt) > legacyDataRecoveryTTL {
		return nil, fmt.Errorf("恢复预览已过期，请重新识别旧 data")
	}
	defer legacyDisposeRecoverySession(session)

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	a.browserMgr.Mutex.Lock()
	running := len(a.browserMgr.BrowserProcesses)
	a.browserMgr.Mutex.Unlock()
	if running > 0 {
		return nil, fmt.Errorf("检测到 %d 个环境仍在运行；请全部关闭后再恢复，防止 Cookies 或账号数据损坏", running)
	}

	backupPath, err := a.legacyCreateDatabaseRollback()
	if err != nil {
		return nil, err
	}
	result := &LegacyDataRecoveryResult{BackupPath: backupPath, Rows: make([]LegacyDataRecoveryRow, 0, len(session.Candidates))}
	readyTotal := 0
	for _, candidate := range session.Candidates {
		if candidate.Status == "ready" || candidate.Status == "overwrite" {
			readyTotal++
		}
	}
	completed := 0
	for _, candidate := range session.Candidates {
		if candidate.Status != "ready" && candidate.Status != "overwrite" {
			result.Skipped++
			result.Rows = append(result.Rows, legacyRecoveryRow(candidate))
			continue
		}
		completed++
		a.legacyEmitRecoveryProgress(completed-1, readyTotal, fmt.Sprintf("正在复制环境 #%d：%s", candidate.EnvironmentNumber, candidate.Profile.ProfileName))
		rollbackData, restoreErr := legacyRestoreCandidateData(candidate, backupPath)
		if restoreErr != nil {
			candidate.Status, candidate.Message = "failed", fmt.Sprintf("复制失败: %v", restoreErr)
			result.Failed++
			result.Rows = append(result.Rows, legacyRecoveryRow(candidate))
			continue
		}
		profile := *candidate.Profile
		if candidate.TargetProfile != nil {
			// Folder-name matching deliberately preserves the current environment
			// identity and settings. Only its browser data directory is replaced.
			profile = *candidate.TargetProfile
		}
		profile.UserDataDir = candidate.RegisteredDir
		profile.Running, profile.DebugReady, profile.DebugPort, profile.Pid = false, false, 0, 0
		if candidate.TargetProfile == nil && extractBadgeNumberFromName(profile.ProfileName) <= 0 {
			profile.ProfileName = fmt.Sprintf("%s-%d", strings.TrimSpace(profile.ProfileName), candidate.EnvironmentNumber)
		}
		if err := browser.NewSQLiteProfileDAO(a.db.GetConn()).Upsert(&profile); err != nil {
			rollbackData()
			candidate.Status, candidate.Message = "failed", fmt.Sprintf("写入环境清单失败: %v", err)
			result.Failed++
		} else {
			if candidate.TargetProfile != nil {
				candidate.Status, candidate.Message = "success", "当前环境的浏览器数据已按文件夹名覆盖，原数据已备份"
			} else {
				candidate.Status, candidate.Message = "success", "环境、账号与 Cookies 已恢复"
			}
			result.Imported++
		}
		result.Rows = append(result.Rows, legacyRecoveryRow(candidate))
		a.legacyEmitRecoveryProgress(completed, readyTotal, candidate.Message)
	}
	if err := a.backupReloadAfterMutation(); err != nil {
		return nil, fmt.Errorf("数据已写入但刷新客户端失败，可重启客户端继续识别: %w", err)
	}
	result.Message = fmt.Sprintf("旧数据恢复完成：成功 %d，跳过 %d，失败 %d；新版本程序文件未被覆盖", result.Imported, result.Skipped, result.Failed)
	a.legacyEmitRecoveryProgress(readyTotal, readyTotal, result.Message)
	return result, nil
}

func legacyRestoreCandidateData(candidate *legacyDataRecoveryCandidate, backupRoot string) (func(), error) {
	if candidate == nil {
		return func() {}, fmt.Errorf("恢复项为空")
	}
	if backupSamePath(candidate.SourceDir, candidate.DestinationDir) {
		return func() {}, fmt.Errorf("备份目录与当前数据目录相同，已停止覆盖")
	}
	rollback := func() { _ = os.RemoveAll(candidate.DestinationDir) }
	if candidate.TargetProfile != nil && backupPathExists(candidate.DestinationDir) {
		info, err := os.Lstat(candidate.DestinationDir)
		if err != nil {
			return func() {}, fmt.Errorf("检查当前数据目录失败: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return func() {}, fmt.Errorf("当前数据目录不是可安全覆盖的普通文件夹")
		}
		backupDir := filepath.Join(backupRoot, "overwritten-data", legacySafeBackupName(candidate.TargetProfile.ProfileId))
		if err := os.MkdirAll(filepath.Dir(backupDir), 0755); err != nil {
			return func() {}, fmt.Errorf("创建覆盖回滚目录失败: %w", err)
		}
		if backupPathExists(backupDir) {
			return func() {}, fmt.Errorf("覆盖回滚目录已存在，已停止覆盖")
		}
		if err := os.Rename(candidate.DestinationDir, backupDir); err != nil {
			return func() {}, fmt.Errorf("备份当前数据目录失败，未执行覆盖: %w", err)
		}
		rollback = func() {
			_ = os.RemoveAll(candidate.DestinationDir)
			_ = os.Rename(backupDir, candidate.DestinationDir)
		}
	}
	stats := &backupMergeStats{}
	if err := backupSyncDir(candidate.SourceDir, candidate.DestinationDir, false, stats, legacySkipRuntimeLockFile); err != nil {
		rollback()
		return func() {}, err
	}
	return rollback, nil
}

func legacySafeBackupName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "environment"
	}
	var out strings.Builder
	for _, r := range value {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			out.WriteRune(r)
		} else {
			out.WriteRune('_')
		}
	}
	return out.String()
}

func (a *App) LegacyDataRecoveryCancel(sessionID string) {
	a.clearLegacyDataRecoverySession(strings.TrimSpace(sessionID))
}

func legacyResolveDataRoot(selected string) (string, string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(selected))
	if err != nil {
		return "", "", fmt.Errorf("旧数据路径无效: %w", err)
	}
	candidates := []string{abs, filepath.Join(abs, "data")}
	for _, root := range candidates {
		dbPath := filepath.Join(root, "app.db")
		if info, statErr := os.Stat(dbPath); statErr == nil && !info.IsDir() {
			return filepath.Clean(root), dbPath, nil
		}
	}
	for _, root := range candidates {
		if folders, scanErr := legacyRawDataFolders(root); scanErr == nil && len(folders) > 0 {
			return filepath.Clean(root), "", nil
		}
	}
	return "", "", fmt.Errorf("所选目录中没有可识别的 app.db 或浏览器数据文件夹；请选择备份 data 或其上一级目录")
}

func legacyProfilesFromRawFolders(sourceRoot string) ([]*browser.Profile, error) {
	folders, err := legacyRawDataFolders(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("扫描备份 data 失败: %w", err)
	}
	profiles := make([]*browser.Profile, 0, len(folders))
	for _, folder := range folders {
		hash := sha256.Sum256([]byte(strings.ToLower(folder)))
		profiles = append(profiles, &browser.Profile{
			ProfileId:   "recovered-folder-" + hex.EncodeToString(hash[:8]),
			ProfileName: folder,
			UserDataDir: folder,
		})
	}
	return profiles, nil
}

func legacyRawDataFolders(sourceRoot string) ([]string, error) {
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return nil, err
	}
	folders := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.EqualFold(name, "recovery-backups") {
			continue
		}
		defaultDir := filepath.Join(sourceRoot, name, "Default")
		if info, statErr := os.Lstat(defaultDir); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			folders = append(folders, name)
		}
	}
	sort.Slice(folders, func(i, j int) bool { return strings.ToLower(folders[i]) < strings.ToLower(folders[j]) })
	return folders, nil
}

func legacyResolveProfileSourceDir(sourceRoot string, profile *browser.Profile) string {
	configured := strings.TrimSpace(profile.UserDataDir)
	if filepath.IsAbs(configured) && backupPathExists(configured) {
		return filepath.Clean(configured)
	}
	if filepath.IsAbs(configured) {
		for _, candidate := range []string{filepath.Join(sourceRoot, filepath.Base(configured)), filepath.Join(sourceRoot, profile.ProfileId)} {
			if backupPathExists(candidate) {
				return candidate
			}
		}
		return filepath.Join(sourceRoot, profile.ProfileId)
	}
	fallback := profile.ProfileId
	clean := legacySafeRelativeDir(configured, fallback)
	candidates := []string{filepath.Join(sourceRoot, clean)}
	cleanParts := strings.Split(filepath.ToSlash(clean), "/")
	if strings.EqualFold(filepath.Base(sourceRoot), "data") && len(cleanParts) > 1 && strings.EqualFold(cleanParts[0], "data") {
		candidates = append(candidates, filepath.Join(sourceRoot, filepath.FromSlash(strings.Join(cleanParts[1:], "/"))))
	}
	if clean != fallback {
		candidates = append(candidates, filepath.Join(sourceRoot, fallback))
	}
	for _, candidate := range candidates {
		if backupPathExists(candidate) {
			return filepath.Clean(candidate)
		}
	}
	return filepath.Clean(candidates[0])
}

func legacyResolveProfileDestination(activeRoot string, profile *browser.Profile) (string, string) {
	configured := legacySafeRelativeDir(profile.UserDataDir, "profile-"+strings.TrimSpace(profile.ProfileId))
	parts := strings.Split(filepath.ToSlash(configured), "/")
	if strings.EqualFold(filepath.Base(activeRoot), "data") && len(parts) > 1 && strings.EqualFold(parts[0], "data") {
		configured = filepath.FromSlash(strings.Join(parts[1:], "/"))
	}
	return configured, filepath.Join(activeRoot, configured)
}

func legacySafeRelativeDir(value, fallback string) string {
	clean := filepath.Clean(strings.TrimSpace(value))
	if clean == "." || clean == ".." || clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fallback
	}
	return clean
}

func legacyDataFolderKey(value, fallback string) string {
	cleaned := filepath.Clean(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if cleaned == "" || cleaned == "." || cleaned == ".." || cleaned == string(filepath.Separator) {
		cleaned = strings.TrimSpace(fallback)
	}
	return strings.ToLower(strings.TrimSpace(filepath.Base(cleaned)))
}

func legacyCopySQLiteSet(srcDB, dstDB string) error {
	if err := os.MkdirAll(filepath.Dir(dstDB), 0755); err != nil {
		return err
	}
	if err := backupCopyFile(srcDB, dstDB); err != nil {
		return fmt.Errorf("复制旧版 app.db 进行只读校验失败: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if backupPathExists(srcDB + suffix) {
			if err := backupCopyFile(srcDB+suffix, dstDB+suffix); err != nil {
				return fmt.Errorf("复制旧版数据库附属文件失败: %w", err)
			}
		}
	}
	return nil
}

func (a *App) legacyCreateDatabaseRollback() (string, error) {
	dbPath := a.backupResolveDBPath(a.config)
	if _, err := a.db.GetConn().Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		return "", fmt.Errorf("创建恢复前检查点失败: %w", err)
	}
	backupRoot := filepath.Join(a.resolveAppPath("data"), "recovery-backups", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(backupRoot, 0755); err != nil {
		return "", fmt.Errorf("创建恢复前回滚目录失败: %w", err)
	}
	if err := backupCopyFile(dbPath, filepath.Join(backupRoot, "app.db")); err != nil {
		return "", fmt.Errorf("备份当前数据库失败: %w", err)
	}
	return backupRoot, nil
}

func legacySkipRuntimeLockFile(rel string) bool {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(rel)))
	return base == "singletonlock" || base == "singletoncookie" || base == "singletonsocket" || base == "devtoolsactiveport"
}

func legacyRecoveryPreview(session *legacyDataRecoverySession) *LegacyDataRecoveryPreview {
	preview := &LegacyDataRecoveryPreview{SessionID: session.ID, SourcePath: session.SourcePath, Rows: make([]LegacyDataRecoveryRow, 0, len(session.Candidates))}
	for _, candidate := range session.Candidates {
		preview.Total++
		switch candidate.Status {
		case "ready", "overwrite":
			preview.Restorable++
		case "missing":
			preview.Missing++
		default:
			preview.Conflicts++
		}
		preview.Rows = append(preview.Rows, legacyRecoveryRow(candidate))
	}
	preview.Message = fmt.Sprintf("识别到 %d 个备份环境，可恢复或按文件夹名覆盖 %d 个", preview.Total, preview.Restorable)
	return preview
}

func legacyRecoveryRow(candidate *legacyDataRecoveryCandidate) LegacyDataRecoveryRow {
	row := LegacyDataRecoveryRow{
		EnvironmentNumber: candidate.EnvironmentNumber,
		ProfileID:         candidate.Profile.ProfileId,
		ProfileName:       candidate.Profile.ProfileName,
		UserDataDir:       candidate.Profile.UserDataDir,
		SourceFolderName:  candidate.SourceFolderName,
		DirectoryExists:   backupPathExists(candidate.SourceDir),
		Status:            candidate.Status,
		Message:           candidate.Message,
	}
	if candidate.TargetProfile != nil {
		row.TargetProfileID = candidate.TargetProfile.ProfileId
		row.TargetProfileName = candidate.TargetProfile.ProfileName
		row.TargetNumber = candidate.TargetNumber
		row.Overwrite = true
	}
	return row
}

func legacyRandomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成恢复会话失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (a *App) legacyEmitRecoveryProgress(completed, total int, message string) {
	if a.ctx == nil {
		return
	}
	progress := 0
	if total > 0 {
		progress = completed * 100 / total
	}
	wailsruntime.EventsEmit(a.ctx, "legacy-data-recovery:progress", map[string]interface{}{"completed": completed, "total": total, "progress": progress, "message": message})
}

func (a *App) clearLegacyDataRecoverySession(sessionID string) {
	a.legacyRecoveryMu.Lock()
	session := a.legacyRecovery
	if session != nil && session.ID == sessionID {
		a.legacyRecovery = nil
	} else {
		session = nil
	}
	a.legacyRecoveryMu.Unlock()
	legacyDisposeRecoverySession(session)
}

func (a *App) clearLegacyDataRecovery() {
	a.legacyRecoveryMu.Lock()
	session := a.legacyRecovery
	a.legacyRecovery = nil
	a.legacyRecoveryMu.Unlock()
	legacyDisposeRecoverySession(session)
}

func legacyDisposeRecoverySession(session *legacyDataRecoverySession) {
	if session == nil {
		return
	}
	if session.Timer != nil {
		session.Timer.Stop()
	}
	_ = os.RemoveAll(session.TempRoot)
}
