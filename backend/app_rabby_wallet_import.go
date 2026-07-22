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
	"strings"
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
	rabbyImportMaxRows     = 500
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
	RowNumber int
	ProfileID string
	Mnemonic  string
}

type rabbyWalletImportSession struct {
	CreatedAt  time.Time
	FileName   string
	WalletType string
	Rows       []rabbyWalletImportSecretRow
	Timer      *time.Timer
}

type RabbyWalletImportPreviewRow struct {
	RowNumber   int    `json:"rowNumber"`
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	WordCount   int    `json:"wordCount"`
	Running     bool   `json:"running"`
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
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "保存 " + spec.Name + " 批量导入模板",
		DefaultFilename: spec.Type + "-wallet-import-template.csv",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "CSV 文件 (*.csv)", Pattern: "*.csv"},
		},
	})
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
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"profile_id", "profile_name", "mnemonic"})
	for _, profile := range profiles {
		_ = w.Write([]string{profile.ProfileId, profile.ProfileName, ""})
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
	for _, row := range session.Rows {
		profile, exists := profiles[row.ProfileID]
		if !exists {
			return nil, fmt.Errorf("第 %d 行对应的环境已不存在，请重新选择文件", row.RowNumber)
		}
		if profile.Running {
			return nil, fmt.Errorf("环境 %s 正在运行，请先关闭文件中全部环境后再执行", profile.ProfileName)
		}
	}

	result := &RabbyWalletImportResult{
		Total: len(session.Rows),
		Rows:  make([]RabbyWalletImportResultRow, 0, len(session.Rows)),
	}
	log := logger.New("WalletImport")
	for index, row := range session.Rows {
		profileSnapshot := profiles[row.ProfileID]
		resultRow := RabbyWalletImportResultRow{
			RowNumber:   row.RowNumber,
			ProfileID:   row.ProfileID,
			ProfileName: profileSnapshot.ProfileName,
			Status:      "running",
			Message:     "正在启动环境并导入 " + spec.Name,
		}
		a.emitWalletImportProgress(spec.Type, index, len(session.Rows), resultRow)

		// During secret entry, load only the selected official wallet extension
		// and suppress ordinary startup pages. This prevents other installed
		// extensions and websites from observing the automated import session.
		extensionDir := a.globalExtensionDir(spec.ExtensionID)
		importLaunchArgs := []string{"--disable-extensions-except=" + extensionDir}
		started, startErr := a.browserInstanceStartInternal(row.ProfileID, importLaunchArgs, nil, true, false, true)
		if startErr != nil || started == nil || started.DebugPort <= 0 {
			resultRow.Status = "failed"
			resultRow.Message = safeRabbyImportError("环境启动失败", startErr)
			result.Failed++
			result.Rows = append(result.Rows, resultRow)
			a.emitWalletImportProgress(spec.Type, index+1, len(session.Rows), resultRow)
			continue
		}

		address, importErr := importMnemonicIntoFreshWallet(spec.Type, started.DebugPort, row.Mnemonic, input.Password)
		_, stopErr := a.BrowserInstanceStop(row.ProfileID)
		if stopErr != nil {
			log.Warn("导入后关闭环境失败", logger.F("profile_id", row.ProfileID), logger.F("error", stopErr.Error()))
		}
		if importErr != nil {
			resultRow.Status = "failed"
			resultRow.Message = safeRabbyImportError(spec.Name+" 导入失败", importErr)
			if stopErr != nil {
				resultRow.Message += "；环境自动关闭失败，请手动关闭"
			}
			result.Failed++
		} else {
			resultRow.Status = "success"
			resultRow.Address = address
			resultRow.Message = spec.Name + " 钱包导入成功"
			if stopErr != nil {
				resultRow.Message += "，但环境自动关闭失败，请手动关闭"
			}
			result.Succeeded++
		}
		result.Rows = append(result.Rows, resultRow)
		a.emitWalletImportProgress(spec.Type, index+1, len(session.Rows), resultRow)
		log.Info("钱包批量导入单项完成", logger.F("wallet_type", spec.Type), logger.F("profile_id", row.ProfileID), logger.F("status", resultRow.Status))
	}
	result.Message = fmt.Sprintf("%s 批量导入完成：成功 %d，失败 %d", spec.Name, result.Succeeded, result.Failed)
	return result, nil
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
			ProfileID:   id,
			ProfileName: profile.ProfileName,
			Running:     profile.Running,
		}
	}
	return out
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
		row.ProfileID = strings.TrimSpace(row.ProfileID)
		row.Mnemonic = normalizeRabbyMnemonic(row.Mnemonic)
		profile, ok := profiles[row.ProfileID]
		if !ok {
			clearRabbySecretRows(parsed)
			return nil, nil, fmt.Errorf("第 %d 行的 profile_id 不存在", row.RowNumber)
		}
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
			RowNumber:   row.RowNumber,
			ProfileID:   row.ProfileID,
			ProfileName: profile.ProfileName,
			WordCount:   wordCount,
			Running:     profile.Running,
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
	profileColumn, mnemonicColumn := -1, -1
	for index, raw := range records[0] {
		header := strings.ToLower(strings.TrimSpace(raw))
		switch header {
		case "profile_id", "profileid", "环境id", "环境_id":
			profileColumn = index
		case "mnemonic", "seed_phrase", "seedphrase", "助记词":
			mnemonicColumn = index
		}
	}
	if profileColumn < 0 || mnemonicColumn < 0 {
		return nil, fmt.Errorf("CSV 表头必须包含 profile_id 和 mnemonic")
	}
	rows := make([]rabbyWalletImportSecretRow, 0, len(records)-1)
	for index, record := range records[1:] {
		rowNumber := index + 2
		if profileColumn >= len(record) || mnemonicColumn >= len(record) {
			return nil, fmt.Errorf("CSV 第 %d 行缺少 profile_id 或 mnemonic", rowNumber)
		}
		profileID := strings.TrimSpace(record[profileColumn])
		mnemonic := strings.TrimSpace(record[mnemonicColumn])
		if profileID == "" && mnemonic == "" {
			continue
		}
		if profileID == "" || mnemonic == "" {
			return nil, fmt.Errorf("CSV 第 %d 行的 profile_id 或 mnemonic 为空", rowNumber)
		}
		rows = append(rows, rabbyWalletImportSecretRow{RowNumber: rowNumber, ProfileID: profileID, Mnemonic: mnemonic})
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
			return nil, fmt.Errorf("TXT 第 %d 行格式错误，应为 profile_id<Tab>助记词 或 profile_id|助记词", index+1)
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
