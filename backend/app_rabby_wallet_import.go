package backend

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"boost-browser/backend/internal/logger"

	"github.com/gorilla/websocket"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	rabbyExtensionID       = "acmacodkjbdgmoleebolmdjonilkdbch"
	jupiterExtensionID     = "iledlaeogohbilgbfhmbgkgmpplbfboh"
	metamaskExtensionID    = "nkbihfbeogaeaoehlefnkodbefgpgknn"
	rabbyImportSessionTTL  = 15 * time.Minute
	rabbyImportMaxFileSize = 4 * 1024 * 1024
	rabbyImportMaxRows     = 2000
	walletImportWorkers    = 4
)

type walletImportSpec struct {
	Type              string
	Name              string
	ExtensionID       string
	AllowedWordCounts map[int]bool
}

var walletImportSpecs = map[string]walletImportSpec{
	"rabby": {
		Type: "rabby", Name: "Rabby", ExtensionID: rabbyExtensionID,
		AllowedWordCounts: map[int]bool{12: true, 15: true, 18: true, 21: true, 24: true},
	},
	"jupiter": {
		Type: "jupiter", Name: "Jupiter", ExtensionID: jupiterExtensionID,
		AllowedWordCounts: map[int]bool{12: true, 24: true},
	},
	"metamask": {
		Type: "metamask", Name: "MetaMask", ExtensionID: metamaskExtensionID,
		AllowedWordCounts: map[int]bool{12: true, 15: true, 18: true, 21: true, 24: true},
	},
}

var rabbyAddressPattern = regexp.MustCompile(`(?i)0x[0-9a-f]{40}`)

type rabbyWalletImportSecretRow struct {
	RowNumber         int
	ProfileID         string
	ProfileName       string
	EnvironmentNumber string
	StorageID         string
	Mnemonic          string
}

type rabbyWalletImportSession struct {
	CreatedAt  time.Time
	FileName   string
	WalletType string
	Rows       []rabbyWalletImportSecretRow
	Timer      *time.Timer
}

type RabbyWalletImportPreviewRow struct {
	RowNumber         int    `json:"rowNumber"`
	EnvironmentNumber int    `json:"environmentNumber"`
	ProfileID         string `json:"profileId"`
	ProfileName       string `json:"profileName"`
	StorageID         string `json:"storageId"`
	WordCount         int    `json:"wordCount"`
	Running           bool   `json:"running"`
	DebugPort         int    `json:"-"`
	DebugReady        bool   `json:"-"`
	UserDataDir       string `json:"-"`
}

type RabbyWalletImportPreview struct {
	Cancelled bool                          `json:"cancelled"`
	SessionID string                        `json:"sessionId"`
	FileName  string                        `json:"fileName"`
	Rows      []RabbyWalletImportPreviewRow `json:"rows"`
	Message   string                        `json:"message"`
}

type RabbyWalletBatchExecuteInput struct {
	SessionID  string `json:"sessionId"`
	WalletType string `json:"walletType"`
	Password   string `json:"password"`
}

type RabbyWalletImportResultRow struct {
	RowNumber   int    `json:"rowNumber"`
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	Status      string `json:"status"`
	Address     string `json:"address"`
	Message     string `json:"message"`
}

type RabbyWalletImportResult struct {
	Total     int                          `json:"total"`
	Succeeded int                          `json:"succeeded"`
	Failed    int                          `json:"failed"`
	Rows      []RabbyWalletImportResultRow `json:"rows"`
	Message   string                       `json:"message"`
}

type RabbyWalletImportProgress struct {
	WalletType  string `json:"walletType"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

// RabbyWalletBatchPrepare selects and validates a CSV/TXT mapping. Mnemonics
// never cross the Wails boundary and are kept only in a short-lived memory
// session until execute/cancel.
func (a *App) RabbyWalletBatchPrepare() (*RabbyWalletImportPreview, error) {
	return a.WalletBatchPrepare("rabby")
}

// WalletBatchPrepare selects and validates a CSV/TXT mapping for one official
// wallet extension. Secret phrases remain in a one-use in-memory session and
// are never returned through the Wails bridge.
func (a *App) WalletBatchPrepare(walletType string) (*RabbyWalletImportPreview, error) {
	if a == nil || a.ctx == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("应用尚未初始化")
	}
	spec, err := resolveWalletImportSpec(walletType)
	if err != nil {
		return nil, err
	}
	if !a.officialWalletExtensionInstalled(spec) {
		return nil, fmt.Errorf("未检测到官方 %s Wallet 扩展，请先在扩展管理中安装并设为全局使用", spec.Name)
	}
	filePath, err := a.selectWalletImportFile(spec)
	if err != nil {
		return nil, fmt.Errorf("打开钱包映射文件失败：%w", err)
	}
	if strings.TrimSpace(filePath) == "" {
		return &RabbyWalletImportPreview{Cancelled: true, Message: "已取消选择"}, nil
	}

	profiles := a.rabbyProfileSnapshot()
	rows, previewRows, err := parseWalletImportFile(filePath, profiles, spec)
	if err != nil {
		return nil, err
	}
	sessionID, err := newRabbyImportSessionID()
	if err != nil {
		clearRabbySecretRows(rows)
		return nil, fmt.Errorf("创建安全导入会话失败：%w", err)
	}

	a.rabbyImportMu.Lock()
	a.cleanupExpiredRabbyImportsLocked(time.Now())
	if a.rabbyImports == nil {
		a.rabbyImports = make(map[string]*rabbyWalletImportSession)
	}
	session := &rabbyWalletImportSession{
		CreatedAt:  time.Now(),
		FileName:   filepath.Base(filePath),
		WalletType: spec.Type,
		Rows:       rows,
	}
	a.rabbyImports[sessionID] = session
	session.Timer = time.AfterFunc(rabbyImportSessionTTL, func() {
		a.rabbyImportMu.Lock()
		defer a.rabbyImportMu.Unlock()
		if a.rabbyImports[sessionID] == session {
			a.clearRabbyImportLocked(sessionID)
		}
	})
	a.rabbyImportMu.Unlock()

	return &RabbyWalletImportPreview{
		SessionID: sessionID,
		FileName:  filepath.Base(filePath),
		Rows:      previewRows,
		Message:   fmt.Sprintf("已安全读取 %d 条 %s 环境映射；助记词未发送到前端", len(rows), spec.Name),
	}, nil
}

// RabbyWalletExportImportTemplate exports environment IDs without secrets so
// the user can fill the mnemonic column offline.
func (a *App) RabbyWalletExportImportTemplate() (map[string]any, error) {
	return a.WalletExportImportTemplate("rabby")
}

func (a *App) WalletExportImportTemplate(walletType string) (map[string]any, error) {
	if a == nil || a.ctx == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("应用尚未初始化")
	}
	spec, err := resolveWalletImportSpec(walletType)
	if err != nil {
		return nil, err
	}
	path, err := a.selectWalletImportTemplatePath(spec)
	if err != nil {
		return nil, fmt.Errorf("打开模板保存对话框失败：%w", err)
	}
	if strings.TrimSpace(path) == "" {
		return map[string]any{"cancelled": true, "message": "已取消保存"}, nil
	}
	if !strings.EqualFold(filepath.Ext(path), ".csv") {
		path += ".csv"
	}

	profiles := a.BrowserProfileList()
	profileSnapshot := a.rabbyProfileSnapshot()
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"environment_number", "profile_id", "profile_name", "storage_id", "mnemonic"})
	for _, profile := range profiles {
		number := 0
		if snapshot, ok := profileSnapshot[profile.ProfileId]; ok {
			number = snapshot.EnvironmentNumber
		}
		storageID := ""
		if snapshot, ok := profileSnapshot[profile.ProfileId]; ok {
			storageID = snapshot.StorageID
		}
		_ = w.Write([]string{strconv.Itoa(number), profile.ProfileId, profile.ProfileName, storageID, ""})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("生成 CSV 模板失败：%w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		return nil, fmt.Errorf("保存 CSV 模板失败：%w", err)
	}
	return map[string]any{
		"cancelled": false,
		"path":      path,
		"count":     len(profiles),
		"message":   fmt.Sprintf("模板已生成，包含 %d 个环境", len(profiles)),
	}, nil
}

func (a *App) RabbyWalletBatchCancel(sessionID string) {
	a.WalletBatchCancel(sessionID)
}

func (a *App) WalletBatchCancel(sessionID string) {
	if a == nil {
		return
	}
	a.rabbyImportMu.Lock()
	defer a.rabbyImportMu.Unlock()
	a.clearRabbyImportLocked(strings.TrimSpace(sessionID))
}

func (a *App) RabbyWalletBatchExecute(input RabbyWalletBatchExecuteInput) (*RabbyWalletImportResult, error) {
	input.WalletType = "rabby"
	return a.WalletBatchExecute(input)
}

func (a *App) WalletBatchExecute(input RabbyWalletBatchExecuteInput) (*RabbyWalletImportResult, error) {
	if a == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("浏览器管理器未初始化")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.WalletType = strings.ToLower(strings.TrimSpace(input.WalletType))
	if input.SessionID == "" {
		return nil, fmt.Errorf("导入会话无效，请重新选择文件")
	}
	if len(input.Password) < 8 {
		return nil, fmt.Errorf("钱包本地解锁密码至少需要 8 个字符")
	}

	a.rabbyImportMu.Lock()
	a.cleanupExpiredRabbyImportsLocked(time.Now())
	session := a.rabbyImports[input.SessionID]
	if session != nil {
		delete(a.rabbyImports, input.SessionID)
		if session.Timer != nil {
			session.Timer.Stop()
			session.Timer = nil
		}
	}
	a.rabbyImportMu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("导入会话已过期，请重新选择文件")
	}
	spec, err := resolveWalletImportSpec(session.WalletType)
	if err != nil {
		clearRabbySecretRows(session.Rows)
		return nil, err
	}
	if input.WalletType != "" && input.WalletType != spec.Type {
		clearRabbySecretRows(session.Rows)
		return nil, fmt.Errorf("钱包类型与导入会话不匹配，请重新选择文件")
	}
	defer clearRabbySecretRows(session.Rows)
	defer func() { input.Password = "" }()

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	a.rabbyImportMu.Lock()
	if a.rabbyImportActive == nil {
		a.rabbyImportActive = make(map[string]bool)
	}
	for _, row := range session.Rows {
		a.rabbyImportActive[row.ProfileID] = true
	}
	a.rabbyImportMu.Unlock()
	defer func() {
		a.rabbyImportMu.Lock()
		for _, row := range session.Rows {
			delete(a.rabbyImportActive, row.ProfileID)
		}
		a.rabbyImportMu.Unlock()
	}()

	if !a.officialWalletExtensionInstalled(spec) {
		return nil, fmt.Errorf("%s Wallet 扩展不存在或已损坏，请重新安装官方全局扩展", spec.Name)
	}
	profiles := a.rabbyProfileSnapshot()
	if err := validateWalletImportProfileMappings(session.Rows, profiles); err != nil {
		return nil, err
	}

	result := &RabbyWalletImportResult{
		Total: len(session.Rows),
		Rows:  make([]RabbyWalletImportResultRow, len(session.Rows)),
	}
	log := logger.New("WalletImport")
	workerCount := walletImportWorkers
	if workerCount > len(session.Rows) {
		workerCount = len(session.Rows)
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	var resultMu sync.Mutex
	completed := 0
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				row := session.Rows[index]
				profileSnapshot := profiles[row.ProfileID]
				resultRow := RabbyWalletImportResultRow{
					RowNumber: row.RowNumber, ProfileID: row.ProfileID, ProfileName: profileSnapshot.ProfileName,
					Status: "running", Message: "正在启动并向环境导入 " + spec.Name,
				}
				resultMu.Lock()
				startedCompleted := completed
				resultMu.Unlock()
				a.emitWalletImportProgress(spec.Type, startedCompleted, len(session.Rows), resultRow)
				debugPort, startErr := a.startWalletImportProfile(spec, row, profileSnapshot)
				var address string
				importErr := startErr
				if importErr == nil {
					address, importErr = importMnemonicIntoFreshWallet(spec.Type, debugPort, row.Mnemonic, input.Password)
				}
				if importErr != nil {
					resultRow.Status = "failed"
					resultRow.Message = safeRabbyImportError(spec.Name+" 导入失败", importErr)
				} else {
					resultRow.Status = "success"
					resultRow.Address = address
					resultRow.Message = spec.Name + " 钱包导入成功；环境保持打开"
				}
				resultMu.Lock()
				result.Rows[index] = resultRow
				if importErr != nil {
					result.Failed++
				} else {
					result.Succeeded++
				}
				completed++
				currentCompleted := completed
				resultMu.Unlock()
				a.emitWalletImportProgress(spec.Type, currentCompleted, len(session.Rows), resultRow)
				log.Info("钱包批量导入单项完成", logger.F("wallet_type", spec.Type), logger.F("profile_id", row.ProfileID), logger.F("status", resultRow.Status))
			}
		}()
	}
	for index := range session.Rows {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	result.Message = fmt.Sprintf("%s 批量导入完成：成功 %d，失败 %d", spec.Name, result.Succeeded, result.Failed)
	return result, nil
}

func validateWalletImportProfileMappings(rows []rabbyWalletImportSecretRow, profiles map[string]RabbyWalletImportPreviewRow) error {
	for _, row := range rows {
		profile, exists := profiles[row.ProfileID]
		if !exists {
			return fmt.Errorf("第 %d 行对应的环境已不存在，请重新下载模板", row.RowNumber)
		}
		expectedNumber, err := strconv.Atoi(strings.TrimSpace(row.EnvironmentNumber))
		if err != nil || expectedNumber != profile.EnvironmentNumber {
			return fmt.Errorf("第 %d 行的环境编号已变更，请重新下载模板", row.RowNumber)
		}
		if row.ProfileName != profile.ProfileName || !strings.EqualFold(strings.TrimSpace(row.StorageID), strings.TrimSpace(profile.StorageID)) {
			return fmt.Errorf("第 %d 行的环境名称或数据文件夹 ID 已变更，请重新下载模板", row.RowNumber)
		}
	}
	return nil
}

func (a *App) startWalletImportProfile(spec walletImportSpec, row rabbyWalletImportSecretRow, snapshot RabbyWalletImportPreviewRow) (int, error) {
	extensionDir := a.globalExtensionDir(spec.ExtensionID)
	started, err := a.browserInstanceStartInternal(
		row.ProfileID,
		[]string{"--disable-extensions-except=" + extensionDir},
		nil,
		true,
		true,
		true,
	)
	if err != nil {
		return 0, fmt.Errorf("环境 #%d %s 自动启动失败：%w", snapshot.EnvironmentNumber, snapshot.ProfileName, err)
	}
	if started == nil || !started.Running || !started.DebugReady || started.DebugPort <= 0 {
		return 0, fmt.Errorf("环境 #%d %s 已启动但调试接口未就绪，请关闭该环境后重试", snapshot.EnvironmentNumber, snapshot.ProfileName)
	}
	if actualStorageID := walletProfileStorageID(started.UserDataDir, started.ProfileId); !strings.EqualFold(actualStorageID, row.StorageID) {
		return 0, fmt.Errorf("环境 #%d %s 的数据文件夹 ID 验证失败，已停止导入", snapshot.EnvironmentNumber, snapshot.ProfileName)
	}
	return started.DebugPort, nil
}

func (a *App) emitRabbyImportProgress(completed, total int, row RabbyWalletImportResultRow) {
	a.emitWalletImportProgress("rabby", completed, total, row)
}

func (a *App) emitWalletImportProgress(walletType string, completed, total int, row RabbyWalletImportResultRow) {
	if a.ctx == nil {
		return
	}
	progress := RabbyWalletImportProgress{
		WalletType:  walletType,
		Completed:   completed,
		Total:       total,
		ProfileID:   row.ProfileID,
		ProfileName: row.ProfileName,
		Status:      row.Status,
		Message:     row.Message,
	}
	wailsruntime.EventsEmit(a.ctx, "wallet-import:progress", progress)
	// Retain the legacy event for clients that still expose only Rabby import.
	if walletType == "rabby" {
		wailsruntime.EventsEmit(a.ctx, "rabby-wallet-import:progress", progress)
	}
}

func (a *App) rabbyProfileSnapshot() map[string]RabbyWalletImportPreviewRow {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	out := make(map[string]RabbyWalletImportPreviewRow, len(a.browserMgr.Profiles))
	for id, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		out[id] = RabbyWalletImportPreviewRow{
			EnvironmentNumber: resolveBadgeDisplayNumber(id, profile.ProfileName, a.browserMgr.Profiles),
			ProfileID:         id,
			ProfileName:       profile.ProfileName,
			StorageID:         walletProfileStorageID(profile.UserDataDir, id),
			Running:           profile.Running,
			DebugPort:         profile.DebugPort,
			DebugReady:        profile.DebugReady,
			UserDataDir:       a.browserMgr.ResolveUserDataDir(profile),
		}
	}
	return out
}

func walletProfileStorageID(userDataDir, profileID string) string {
	cleaned := filepath.Clean(strings.ReplaceAll(strings.TrimSpace(userDataDir), "\\", "/"))
	if cleaned == "" || cleaned == "." || cleaned == string(filepath.Separator) {
		return strings.TrimSpace(profileID)
	}
	storageID := strings.TrimSpace(filepath.Base(cleaned))
	if storageID == "" || storageID == "." || storageID == string(filepath.Separator) {
		return strings.TrimSpace(profileID)
	}
	return storageID
}

func (a *App) cleanupExpiredRabbyImportsLocked(now time.Time) {
	for id, session := range a.rabbyImports {
		if session == nil || now.Sub(session.CreatedAt) > rabbyImportSessionTTL {
			a.clearRabbyImportLocked(id)
		}
	}
}

func (a *App) clearRabbyImportLocked(sessionID string) {
	session := a.rabbyImports[sessionID]
	if session != nil {
		if session.Timer != nil {
			session.Timer.Stop()
			session.Timer = nil
		}
		clearRabbySecretRows(session.Rows)
		delete(a.rabbyImports, sessionID)
	}
}

func clearRabbySecretRows(rows []rabbyWalletImportSecretRow) {
	for i := range rows {
		rows[i].Mnemonic = ""
	}
}

func newRabbyImportSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func resolveWalletImportSpec(walletType string) (walletImportSpec, error) {
	key := strings.ToLower(strings.TrimSpace(walletType))
	spec, ok := walletImportSpecs[key]
	if !ok {
		return walletImportSpec{}, fmt.Errorf("不支持的钱包类型：%s", walletType)
	}
	return spec, nil
}

// officialWalletExtensionInstalled prevents a package downloaded from an
// arbitrary URL containing a 32-character ID from being trusted as a wallet.
// Wallet batch import accepts only a registry entry installed from the exact
// Chrome Web Store origin (or an exact extension ID, which resolves there).
func (a *App) officialWalletExtensionInstalled(spec walletImportSpec) bool {
	if a == nil || !extensionManifestExists(a.globalExtensionDir(spec.ExtensionID)) {
		return false
	}
	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return false
	}
	for _, entry := range registry.Extensions {
		if !strings.EqualFold(strings.TrimSpace(entry.ExtensionID), spec.ExtensionID) {
			continue
		}
		raw := strings.TrimSpace(entry.DownloadAddress)
		if strings.EqualFold(raw, spec.ExtensionID) {
			return true
		}
		parsed, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "chromewebstore.google.com") {
			continue
		}
		if strings.EqualFold(extractExtensionID(raw), spec.ExtensionID) {
			return true
		}
	}
	return false
}

func parseRabbyWalletImportFile(path string, profiles map[string]RabbyWalletImportPreviewRow) ([]rabbyWalletImportSecretRow, []RabbyWalletImportPreviewRow, error) {
	return parseWalletImportFile(path, profiles, walletImportSpecs["rabby"])
}

func parseWalletImportFile(path string, profiles map[string]RabbyWalletImportPreviewRow, spec walletImportSpec) ([]rabbyWalletImportSecretRow, []RabbyWalletImportPreviewRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取钱包映射文件失败：%w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, rabbyImportMaxFileSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("读取钱包映射文件失败：%w", err)
	}
	defer func() {
		for i := range data {
			data[i] = 0
		}
	}()
	if len(data) > rabbyImportMaxFileSize {
		return nil, nil, fmt.Errorf("钱包映射文件不能超过 4MB")
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return nil, nil, fmt.Errorf("钱包映射文件必须使用 UTF-8 编码")
	}

	var parsed []rabbyWalletImportSecretRow
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		parsed, err = parseRabbyCSV(data)
	case ".txt":
		parsed, err = parseRabbyTXT(data)
	default:
		return nil, nil, fmt.Errorf("仅支持 CSV 或 TXT 文件")
	}
	if err != nil {
		return nil, nil, err
	}
	if len(parsed) == 0 {
		return nil, nil, fmt.Errorf("文件中没有可导入的钱包记录")
	}
	if len(parsed) > rabbyImportMaxRows {
		clearRabbySecretRows(parsed)
		return nil, nil, fmt.Errorf("单次最多导入 %d 个钱包", rabbyImportMaxRows)
	}

	seenProfiles := map[string]int{}
	seenMnemonics := map[[32]byte]int{}
	preview := make([]RabbyWalletImportPreviewRow, 0, len(parsed))
	for i := range parsed {
		row := &parsed[i]
		row.Mnemonic = normalizeRabbyMnemonic(row.Mnemonic)
		profile, resolveErr := resolveWalletImportProfile(*row, profiles)
		if resolveErr != nil {
			clearRabbySecretRows(parsed)
			return nil, nil, resolveErr
		}
		row.ProfileID = profile.ProfileID
		row.ProfileName = profile.ProfileName
		row.EnvironmentNumber = strconv.Itoa(profile.EnvironmentNumber)
		row.StorageID = profile.StorageID
		if previous, exists := seenProfiles[row.ProfileID]; exists {
			clearRabbySecretRows(parsed)
			return nil, nil, fmt.Errorf("第 %d 行与第 %d 行重复使用同一环境", row.RowNumber, previous)
		}
		seenProfiles[row.ProfileID] = row.RowNumber
		wordCount := len(strings.Fields(row.Mnemonic))
		if !spec.AllowedWordCounts[wordCount] {
			clearRabbySecretRows(parsed)
			allowed := make([]string, 0, len(spec.AllowedWordCounts))
			for _, count := range []int{12, 15, 18, 21, 24} {
				if spec.AllowedWordCounts[count] {
					allowed = append(allowed, fmt.Sprintf("%d", count))
				}
			}
			return nil, nil, fmt.Errorf("第 %d 行助记词词数为 %d，%s 仅支持 %s 词", row.RowNumber, wordCount, spec.Name, strings.Join(allowed, "/"))
		}
		hash := sha256Bytes(row.Mnemonic)
		if previous, exists := seenMnemonics[hash]; exists {
			clearRabbySecretRows(parsed)
			return nil, nil, fmt.Errorf("第 %d 行与第 %d 行使用了重复助记词，已停止导入", row.RowNumber, previous)
		}
		seenMnemonics[hash] = row.RowNumber
		preview = append(preview, RabbyWalletImportPreviewRow{
			RowNumber:         row.RowNumber,
			EnvironmentNumber: profile.EnvironmentNumber,
			ProfileID:         row.ProfileID,
			ProfileName:       profile.ProfileName,
			StorageID:         profile.StorageID,
			WordCount:         wordCount,
			Running:           profile.Running,
		})
	}
	return parsed, preview, nil
}

func parseRabbyCSV(data []byte) ([]rabbyWalletImportSecretRow, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV 格式错误：%w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV 至少需要表头和一行数据")
	}
	profileColumn, profileNameColumn, environmentNumberColumn, storageIDColumn, mnemonicColumn := -1, -1, -1, -1, -1
	for index, raw := range records[0] {
		header := strings.ToLower(strings.TrimSpace(raw))
		switch header {
		case "profile_id", "profileid", "环境id", "环境_id":
			profileColumn = index
		case "profile_name", "profilename", "环境名称", "环境_名称":
			profileNameColumn = index
		case "environment_number", "environmentnumber", "profile_number", "profilenumber", "环境编号", "编号":
			environmentNumberColumn = index
		case "storage_id", "storageid", "folder_id", "folderid", "存储id", "文件夹id":
			storageIDColumn = index
		case "mnemonic", "seed_phrase", "seedphrase", "助记词":
			mnemonicColumn = index
		}
	}
	if mnemonicColumn < 0 || (profileColumn < 0 && profileNameColumn < 0 && environmentNumberColumn < 0 && storageIDColumn < 0) {
		return nil, fmt.Errorf("CSV 表头必须包含 mnemonic，以及 environment_number、profile_id、profile_name、storage_id 中至少一项")
	}
	rows := make([]rabbyWalletImportSecretRow, 0, len(records)-1)
	for index, record := range records[1:] {
		rowNumber := index + 2
		cell := func(column int) string {
			if column < 0 || column >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[column])
		}
		profileID := cell(profileColumn)
		profileName := cell(profileNameColumn)
		environmentNumber := cell(environmentNumberColumn)
		storageID := cell(storageIDColumn)
		mnemonic := cell(mnemonicColumn)
		// Downloaded templates intentionally contain one row for every
		// environment. A blank mnemonic means "do not import this row" so users
		// can fill only the environments they need without deleting mappings.
		if mnemonic == "" {
			continue
		}
		if profileID == "" && profileName == "" && environmentNumber == "" && storageID == "" {
			return nil, fmt.Errorf("CSV 第 %d 行必须填写 mnemonic，并至少填写环境编号、profile_id、profile_name、storage_id 中一项", rowNumber)
		}
		rows = append(rows, rabbyWalletImportSecretRow{
			RowNumber: rowNumber, ProfileID: profileID, ProfileName: profileName,
			EnvironmentNumber: environmentNumber, StorageID: storageID, Mnemonic: mnemonic,
		})
	}
	return rows, nil
}

func parseRabbyTXT(data []byte) ([]rabbyWalletImportSecretRow, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	rows := make([]rabbyWalletImportSecretRow, 0, len(lines))
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		separator := "\t"
		if !strings.Contains(line, separator) {
			separator = "|"
		}
		if !strings.Contains(line, separator) {
			return nil, fmt.Errorf("TXT 第 %d 行格式错误，应为 环境编号/profile_id/环境名称<Tab>助记词，或使用 | 分隔", index+1)
		}
		parts := strings.SplitN(line, separator, 2)
		profileID, mnemonic := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if profileID == "" || mnemonic == "" {
			return nil, fmt.Errorf("TXT 第 %d 行的 profile_id 或助记词为空", index+1)
		}
		rows = append(rows, rabbyWalletImportSecretRow{RowNumber: index + 1, ProfileID: profileID, Mnemonic: mnemonic})
	}
	return rows, nil
}

func resolveWalletImportProfile(row rabbyWalletImportSecretRow, profiles map[string]RabbyWalletImportPreviewRow) (RabbyWalletImportPreviewRow, error) {
	var selected RabbyWalletImportPreviewRow
	hasSelected := false
	bind := func(label, value string, matches []RabbyWalletImportPreviewRow) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		if len(matches) == 0 {
			return fmt.Errorf("第 %d 行的 %s %q 未匹配到环境", row.RowNumber, label, value)
		}
		if len(matches) > 1 {
			if hasSelected {
				for _, match := range matches {
					if match.ProfileID == selected.ProfileID {
						return nil
					}
				}
				return fmt.Errorf("第 %d 行填写的环境编号、profile_id、profile_name、storage_id 指向不同环境，已停止导入", row.RowNumber)
			}
			return fmt.Errorf("第 %d 行的 %s %q 匹配到多个环境，请改用唯一的 profile_id", row.RowNumber, label, value)
		}
		if hasSelected && selected.ProfileID != matches[0].ProfileID {
			return fmt.Errorf("第 %d 行填写的环境编号、profile_id、profile_name、storage_id 指向不同环境，已停止导入", row.RowNumber)
		}
		selected = matches[0]
		hasSelected = true
		return nil
	}

	matchByID := func(value string) []RabbyWalletImportPreviewRow {
		value = strings.TrimSpace(value)
		if profile, ok := profiles[value]; ok {
			return []RabbyWalletImportPreviewRow{profile}
		}
		for id, profile := range profiles {
			if strings.EqualFold(strings.TrimSpace(id), value) {
				return []RabbyWalletImportPreviewRow{profile}
			}
		}
		return nil
	}
	matchByName := func(value string) []RabbyWalletImportPreviewRow {
		value = strings.TrimSpace(value)
		matches := make([]RabbyWalletImportPreviewRow, 0, 1)
		for _, profile := range profiles {
			if strings.EqualFold(strings.TrimSpace(profile.ProfileName), value) {
				matches = append(matches, profile)
			}
		}
		return matches
	}
	matchByNumber := func(value string) []RabbyWalletImportPreviewRow {
		normalized := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "#"))
		number, err := strconv.Atoi(normalized)
		if err != nil || number <= 0 {
			return nil
		}
		matches := make([]RabbyWalletImportPreviewRow, 0, 1)
		for _, profile := range profiles {
			if profile.EnvironmentNumber == number {
				matches = append(matches, profile)
			}
		}
		return matches
	}
	matchByStorageID := func(value string) []RabbyWalletImportPreviewRow {
		value = strings.TrimSpace(value)
		matches := make([]RabbyWalletImportPreviewRow, 0, 1)
		for _, profile := range profiles {
			if strings.EqualFold(strings.TrimSpace(profile.StorageID), value) {
				matches = append(matches, profile)
			}
		}
		return matches
	}

	profileID := strings.TrimSpace(row.ProfileID)
	if profileID != "" {
		matches := matchByID(profileID)
		// Legacy two-column CSV/TXT files often put the visible number or name
		// in the profile_id position. Accept it only when no explicit selector
		// column is also populated.
		if len(matches) == 0 && strings.TrimSpace(row.EnvironmentNumber) == "" && strings.TrimSpace(row.ProfileName) == "" && strings.TrimSpace(row.StorageID) == "" {
			matches = matchByNumber(profileID)
			if len(matches) == 0 {
				matches = matchByName(profileID)
			}
		}
		if err := bind("profile_id", profileID, matches); err != nil {
			return RabbyWalletImportPreviewRow{}, err
		}
	}
	if err := bind("environment_number", row.EnvironmentNumber, matchByNumber(row.EnvironmentNumber)); err != nil {
		return RabbyWalletImportPreviewRow{}, err
	}
	if err := bind("profile_name", row.ProfileName, matchByName(row.ProfileName)); err != nil {
		return RabbyWalletImportPreviewRow{}, err
	}
	if err := bind("storage_id", row.StorageID, matchByStorageID(row.StorageID)); err != nil {
		return RabbyWalletImportPreviewRow{}, err
	}
	if !hasSelected {
		return RabbyWalletImportPreviewRow{}, fmt.Errorf("第 %d 行未填写可识别的环境编号、profile_id、profile_name 或 storage_id", row.RowNumber)
	}
	return selected, nil
}

func normalizeRabbyMnemonic(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func validRabbyMnemonicWordCount(count int) bool {
	switch count {
	case 12, 15, 18, 21, 24:
		return true
	default:
		return false
	}
}

func sha256Bytes(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

func safeRabbyImportError(prefix string, err error) string {
	if err == nil {
		return prefix
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 300 {
		message = message[:300]
	}
	return prefix + "：" + message
}

type rabbyCDPClient struct {
	conn   *websocket.Conn
	nextID int
}

func newRabbyCDPClient(wsURL string) (*rabbyCDPClient, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	return &rabbyCDPClient{conn: conn, nextID: 1}, nil
}

func (c *rabbyCDPClient) close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *rabbyCDPClient) call(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("CDP 连接不存在")
	}
	id := c.nextID
	c.nextID++
	_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	if err := c.conn.WriteJSON(cdpMessage{Id: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var raw map[string]json.RawMessage
		if err := c.conn.ReadJSON(&raw); err != nil {
			return nil, err
		}
		var responseID int
		if value := raw["id"]; value != nil {
			_ = json.Unmarshal(value, &responseID)
		}
		if responseID != id {
			continue
		}
		if value := raw["error"]; value != nil {
			var cdpErr struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(value, &cdpErr)
			return nil, fmt.Errorf("CDP 错误：%s", cdpErr.Message)
		}
		result := map[string]any{}
		if value := raw["result"]; value != nil {
			_ = json.Unmarshal(value, &result)
		}
		return result, nil
	}
}

func (c *rabbyCDPClient) evaluate(expression string) (any, error) {
	result, err := c.call("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
	}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	if exception := result["exceptionDetails"]; exception != nil {
		return nil, fmt.Errorf("扩展页面脚本执行失败")
	}
	remote, ok := result["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("扩展页面未返回结果")
	}
	return remote["value"], nil
}

func importMnemonicIntoFreshRabby(debugPort int, mnemonic, password string) (string, error) {
	browserWS, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return "", err
	}
	browserClient, err := newRabbyCDPClient(browserWS)
	if err != nil {
		return "", fmt.Errorf("连接浏览器调试接口失败：%w", err)
	}
	defer browserClient.close()

	importURL := "chrome-extension://" + rabbyExtensionID + "/index.html#/new-user/import/seed-phrase"
	created, err := browserClient.call("Target.createTarget", map[string]any{"url": importURL}, 8*time.Second)
	if err != nil {
		return "", fmt.Errorf("打开 Rabby 导入页面失败：%w", err)
	}
	targetID, _ := created["targetId"].(string)
	if targetID == "" {
		return "", fmt.Errorf("Rabby 导入页面未创建")
	}
	defer func() {
		_, _ = browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 3*time.Second)
	}()

	target, err := waitForRabbyTarget(debugPort, targetID, 12*time.Second)
	if err != nil {
		return "", err
	}
	pageClient, err := newRabbyCDPClient(target.WebSocketDebuggerUrl)
	if err != nil {
		return "", fmt.Errorf("连接 Rabby 页面失败：%w", err)
	}
	defer pageClient.close()

	if err := waitRabbyCondition(pageClient, `document.querySelectorAll('.mnemonics-input').length > 0`, 15*time.Second); err != nil {
		return "", fmt.Errorf("Rabby 助记词页面加载失败，请确认扩展版本和安装状态")
	}
	bootedValue, err := pageClient.evaluate(`new Promise((resolve) => chrome.storage.local.get('keyringState', (data) => resolve(Boolean(data && data.keyringState && data.keyringState.booted))))`)
	if err != nil {
		return "", fmt.Errorf("无法确认 Rabby 是否已初始化，已停止以避免覆盖：%w", err)
	}
	if booted, _ := bootedValue.(bool); booted {
		return "", fmt.Errorf("Rabby 已存在钱包，已拒绝覆盖")
	}

	focused, err := pageClient.evaluate(`(() => { const input = document.querySelector('.mnemonics-input'); if (!input) return false; input.focus(); return true; })()`)
	if err != nil || focused != true {
		return "", fmt.Errorf("无法定位 Rabby 助记词输入框")
	}
	if _, err := pageClient.call("Input.insertText", map[string]any{"text": mnemonic}, 8*time.Second); err != nil {
		return "", fmt.Errorf("写入 Rabby 助记词失败")
	}
	time.Sleep(500 * time.Millisecond)
	clicked, err := pageClient.evaluate(`(() => { const button = document.querySelector('button[type="submit"]'); if (!button || button.disabled) return false; button.click(); return true; })()`)
	if err != nil || clicked != true {
		return "", fmt.Errorf("Rabby 未接受助记词，请检查文件内容")
	}
	if err := waitRabbyCondition(pageClient, `location.hash.includes('/set-password')`, 15*time.Second); err != nil {
		return "", fmt.Errorf("助记词未通过 Rabby 校验")
	}
	if err := waitRabbyCondition(pageClient, `document.querySelectorAll('input[type="password"]').length >= 2`, 10*time.Second); err != nil {
		return "", fmt.Errorf("Rabby 密码设置页面加载失败")
	}

	for index := 0; index < 2; index++ {
		expression := fmt.Sprintf(`(() => { const input = document.querySelectorAll('input[type="password"]')[%d]; if (!input) return false; input.focus(); return true; })()`, index)
		focused, focusErr := pageClient.evaluate(expression)
		if focusErr != nil || focused != true {
			return "", fmt.Errorf("无法定位 Rabby 密码输入框")
		}
		if _, err := pageClient.call("Input.insertText", map[string]any{"text": password}, 8*time.Second); err != nil {
			return "", fmt.Errorf("写入 Rabby 本地密码失败")
		}
	}
	time.Sleep(400 * time.Millisecond)
	clicked, err = pageClient.evaluate(`(() => { const button = [...document.querySelectorAll('button.ant-btn-primary')].find((item) => !item.disabled); if (!button) return false; button.click(); return true; })()`)
	if err != nil || clicked != true {
		return "", fmt.Errorf("Rabby 密码确认按钮不可用")
	}
	if err := waitRabbyCondition(pageClient, `location.hash.includes('/new-user/success')`, 30*time.Second); err != nil {
		return "", fmt.Errorf("Rabby 创建保险库超时")
	}

	addressValue, err := waitRabbyValue(pageClient, `(() => { const match = document.body.innerText.match(/0x[0-9a-fA-F]{40}/); return match ? match[0] : ''; })()`, 12*time.Second)
	if err != nil {
		return "", fmt.Errorf("Rabby 已完成导入，但未能读取公开地址")
	}
	address, _ := addressValue.(string)
	if !rabbyAddressPattern.MatchString(address) {
		return "", fmt.Errorf("Rabby 已完成导入，但公开地址校验失败")
	}
	return address, nil
}

func waitForRabbyTarget(debugPort int, targetID string, timeout time.Duration) (cdpTarget, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		targets, err := listCDPTargets(debugPort)
		if err == nil {
			for _, target := range targets {
				if target.ID == targetID && target.WebSocketDebuggerUrl != "" {
					return target, nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return cdpTarget{}, fmt.Errorf("等待 Rabby 扩展页面超时")
}

func waitRabbyCondition(client *rabbyCDPClient, expression string, timeout time.Duration) error {
	_, err := waitRabbyValue(client, expression, timeout)
	return err
}

func waitRabbyValue(client *rabbyCDPClient, expression string, timeout time.Duration) (any, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, err := client.evaluate(expression)
		if err == nil {
			switch typed := value.(type) {
			case bool:
				if typed {
					return value, nil
				}
			case string:
				if strings.TrimSpace(typed) != "" {
					return value, nil
				}
			case float64:
				if typed > 0 {
					return value, nil
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, fmt.Errorf("等待 Rabby 页面状态超时")
}
