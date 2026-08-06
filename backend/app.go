package backend

import (
	"boost-browser/backend/internal/activation"
	"boost-browser/backend/internal/apppath"
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"boost-browser/backend/internal/database"
	"boost-browser/backend/internal/launchcode"
	"boost-browser/backend/internal/logger"
	"boost-browser/backend/internal/proxy"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type quitMode uint8

const (
	quitModeFull quitMode = iota
	quitModeAppOnly
)

// App 应用结构体
type App struct {
	ctx              context.Context
	panelMode        bool
	config           *config.Config
	db               *database.DB
	interceptor      *logger.MethodInterceptor
	browserMgr       *browser.Manager
	xrayMgr          *proxy.XrayManager
	clashMgr         *proxy.ClashManager
	singboxMgr       *proxy.SingBoxManager
	standardRelayMgr *proxy.StandardRelayManager
	launchCodeSvc    *launchcode.LaunchCodeService
	launchServer     *launchcode.LaunchServer
	speedScheduler   *browser.ProxySpeedScheduler
	appRoot          string
	version          string
	activationStatus activation.Status

	forceQuit          bool       // 强制退出标志，用于跳过 OnBeforeClose 的拦截
	quitMode           quitMode   // 退出模式：全量退出 / 仅退出应用
	maintenanceMu      sync.Mutex // 维护类操作（初始化/导入/导出）互斥锁
	browserCloseMu     sync.Mutex // 环境关闭按 Profile ID/PID 串行确认写盘
	bridgeMu           sync.Mutex
	xrayBridgeRefs     map[string]string
	rabbyImportMu      sync.Mutex
	rabbyImports       map[string]*rabbyWalletImportSession
	rabbyImportActive  map[string]bool
	legacyRecoveryMu   sync.Mutex
	legacyRecovery     *legacyDataRecoverySession
	startupDataMu      sync.RWMutex
	startupDataStatus  StartupDataCompatibilityStatus
	stopServicesOnce   sync.Once
	finalizeOnce       sync.Once
	updateMu           sync.Mutex
	verifiedUpdatePath string

	// syncProfileReloadAt throttles the sync assistant's periodic SQLite
	// profile-table reload (the panel process is a separate process whose
	// in-memory profile map only loads once).
	syncProfileReloadAt time.Time

	// envPopup confines extension/wallet/secondary Chrome windows to each
	// running environment's main window. Main client only; single owner.
	envPopup *environmentPopupConfiner
}

// NewApp 创建新的应用实例
func NewApp(appRoot string, args ...interface{}) *App {
	panelMode := false
	version := ""
	for _, arg := range args {
		switch v := arg.(type) {
		case bool:
			panelMode = v
		case string:
			if version == "" {
				version = strings.TrimSpace(v)
			}
		}
	}
	return &App{
		appRoot:           strings.TrimSpace(appRoot),
		panelMode:         panelMode,
		version:           version,
		xrayBridgeRefs:    make(map[string]string),
		rabbyImports:      make(map[string]*rabbyWalletImportSession),
		rabbyImportActive: make(map[string]bool),
	}
}

func (a *App) appName() string {
	if a.config != nil {
		if name := strings.TrimSpace(a.config.App.Name); name != "" {
			return name
		}
	}
	return "BrowserStudio"
}

func (a *App) appVersion() string {
	version := strings.TrimSpace(a.version)
	if version == "" {
		return "unknown"
	}
	return version
}

// GetActivationStatus exposes provider-neutral activation state to the UI.
// Future signed or online providers can replace the offline provider without
// changing this API contract.
func (a *App) GetActivationStatus() activation.Status {
	return a.activationStatus
}

func (a *App) ensureLaunchServerAPIKey(cfg *config.Config) {
	if cfg == nil || !cfg.LaunchServer.Auth.Enabled || strings.TrimSpace(cfg.LaunchServer.Auth.APIKey) != "" {
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	cfg.LaunchServer.Auth.APIKey = hex.EncodeToString(buf)
	_ = cfg.Save(a.resolveAppPath("config.yaml"))
}

// startup 应用启动时调用
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.activationStatus = (activation.OfflineInstallerProvider{}).Verify(a.appRoot)
	a.lifecycleLog("startup", "version="+a.appVersion())
	// 写入 Chrome 企业策略到 HKCU，抑制 --no-sandbox 等 unsupported flag
	// 引发的黄色安全警告 infobar。无需管理员权限，不会被识别为 bot 信号。
	applyChromeEnterprisePolicies()
	if err := apppath.EnsureWritableLayout(a.appRoot); err != nil {
		a.lifecycleLog("startup-failed", "step=EnsureWritableLayout", "error="+err.Error())
		runtime.LogFatal(ctx, fmt.Sprintf("初始化 Linux 用户数据目录失败: %v", err))
		return
	}
	cfg, err := LoadConfig(a.resolveAppPath("config.yaml"))
	if err != nil {
		cfg = config.DefaultConfig()
	}
	a.ensureLaunchServerAPIKey(cfg)
	a.config = cfg
	a.applyRuntimeConfig(cfg.Runtime)

	logConfig := logger.LoggerConfig{
		Level:           cfg.Logging.Level,
		FileEnabled:     cfg.Logging.FileEnabled,
		FilePath:        a.resolveAppPath(cfg.Logging.FilePath),
		Format:          cfg.Logging.Format,
		BufferSize:      cfg.Logging.BufferSize,
		AsyncQueueSize:  cfg.Logging.AsyncQueueSize,
		FlushIntervalMs: cfg.Logging.FlushIntervalMs,
		Rotation: logger.RotationConfig{
			Enabled:      cfg.Logging.Rotation.Enabled,
			MaxSizeMB:    cfg.Logging.Rotation.MaxSizeMB,
			MaxAge:       cfg.Logging.Rotation.MaxAge,
			MaxBackups:   cfg.Logging.Rotation.MaxBackups,
			TimeInterval: cfg.Logging.Rotation.TimeInterval,
		},
	}
	logger.InitWithConfig(ctx, logConfig)

	log := logger.New("App")
	log.Info("应用启动中...",
		logger.F("version", a.appVersion()),
		logger.F("panel_mode", a.panelMode),
		logger.F("max_memory_mb", cfg.Runtime.MaxMemoryMB),
		logger.F("gc_percent", cfg.Runtime.GCPercent),
	)
	if apppath.IsDetached(a.appRoot) {
		log.Info("检测到安装目录需要只读运行，已切换到用户数据目录",
			logger.F("install_root", apppath.InstallRoot(a.appRoot)),
			logger.F("state_root", apppath.StateRoot(a.appRoot)),
		)
	}

	// 安装/升级永远复用已有 data；只有目录不存在时才创建最新版本的空结构。
	activeDataRoot := a.resolveAppPath("data")
	dataExisted := directoryHasEntries(activeDataRoot)
	if err := os.MkdirAll(activeDataRoot, 0755); err != nil {
		log.Error("创建 data 目录失败", logger.F("error", err))
	}

	if !a.panelMode {
		// Self-use clean build: do not deploy the bundled chromium-web-store helper
		// extension by default. Users can still import/install their own extensions
		// manually; no default/search helper extension should appear on startup.
		a.ensureDefaultCores()
	} else {
		log.Info("同步面板子进程启动：跳过主窗口扩展部署与默认内核维护")
	}

	if cfg.Logging.Interceptor.Enabled {
		interceptorConfig := logger.InterceptorConfig{
			Enabled:         cfg.Logging.Interceptor.Enabled,
			LogParameters:   cfg.Logging.Interceptor.LogParameters,
			LogResults:      cfg.Logging.Interceptor.LogResults,
			SensitiveFields: cfg.Logging.Interceptor.SensitiveFields,
		}
		a.interceptor = logger.NewMethodInterceptor(log, interceptorConfig)
	}

	db, err := database.NewDB(a.resolveAppPath(cfg.Database.SQLite.Path))
	if err != nil {
		log.Error("初始化数据库失败", logger.F("error", err))
		runtime.LogFatal(ctx, fmt.Sprintf("初始化数据库失败: %v", err))
		return
	}
	a.db = db
	if err := db.Migrate(); err != nil {
		log.Error("数据库迁移失败", logger.F("error", err))
	}

	a.browserMgr = browser.NewManager(cfg, a.appRoot)
	a.xrayMgr = proxy.NewXrayManager(cfg, a.appRoot)
	a.clashMgr = proxy.NewClashManager(cfg, a.appRoot)
	a.singboxMgr = proxy.NewSingBoxManager(cfg, a.appRoot)
	a.standardRelayMgr = proxy.NewStandardRelayManager()

	// 注入 DAO（必须在 InitData 之前）
	conn := db.GetConn()
	a.browserMgr.ProfileDAO = browser.NewSQLiteProfileDAO(conn)
	a.browserMgr.ProxyDAO = browser.NewSQLiteProxyDAO(conn)
	a.browserMgr.CoreDAO = browser.NewSQLiteCoreDAO(conn)
	a.browserMgr.BookmarkDAO = browser.NewSQLiteBookmarkDAO(conn)
	a.browserMgr.GroupDAO = browser.NewSQLiteGroupDAO(conn)

	// 一次性迁移：若 SQLite 表为空则从旧文件导入
	a.migrateToSQLite()
	a.browserMgr.InitData()
	a.initializeActiveDataCompatibility(activeDataRoot, dataExisted)
	if !a.panelMode {
		// 默认使用随 BrowserStudio 打包/下载到 chrome/ 目录内的独立 Google Chrome 内核；不再引用系统安装的 Chrome。
		a.ensureBundledGoogleChromeCore()
		// 同步内存态，确保后续默认内核解析使用刚注册的内置 Chrome。
		_ = a.browserMgr.ListCores()
		// 路径有效性扫描只写诊断日志，不参与内核选择。延后执行可避免大量
		// 浏览器内核目录在主窗口首次加载的关键路径上同步触盘。
		go func() {
			time.Sleep(500 * time.Millisecond)
			a.autoDetectCores()
		}()
		a.loadProxies()
		a.reconcileProfileProxyBindings()
	} else {
		// 面板模式只需要读取 profile 列表并做运行态探测，不需要再起主窗口那套内核/代理维护链路。
		_ = a.browserMgr.ListCores()
	}

	if !a.panelMode {
		// 初始化 LaunchCode 服务
		launchCodeDAO := launchcode.NewSQLiteLaunchCodeDAO(a.db.GetConn())
		a.launchCodeSvc = launchcode.NewLaunchCodeService(launchCodeDAO)
		if err := a.launchCodeSvc.LoadAll(); err != nil {
			log.Error("LaunchCode 加载失败", logger.F("error", err))
		}
		a.browserMgr.CodeProvider = a.launchCodeSvc

		// 启动 LaunchServer
		port := a.config.LaunchServer.Port
		a.launchServer = launchcode.NewLaunchServer(a.launchCodeSvc, a, a.browserMgr, port)
		a.launchServer.SetAPIAuthConfig(launchcode.APIAuthConfig{
			Enabled: a.config.LaunchServer.Auth.Enabled,
			APIKey:  a.config.LaunchServer.Auth.APIKey,
			Header:  a.config.LaunchServer.Auth.Header,
		})
		if err := a.launchServer.Start(); err != nil {
			log.Error("LaunchServer 启动失败", logger.F("error", err))
		} else {
			log.Info("LaunchServer 监听地址",
				logger.F("url", fmt.Sprintf("http://127.0.0.1:%d", a.launchServer.Port())),
				logger.F("preferred_port", port),
			)
			a.launchServer.SetExtensionInstaller(a)
		}
	} else {
		log.Info("同步面板子进程启动：跳过 LaunchServer / LaunchCode 常驻服务")
	}

	// 连接池失效通知
	a.xrayMgr.OnBridgeDied = func(key string, err error) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "proxy:bridge:died", map[string]interface{}{
				"engine": "xray",
				"key":    shortRuntimeKey(key),
				"error":  err.Error(),
			})
		}
	}
	a.singboxMgr.OnBridgeDied = func(key string, err error) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "proxy:bridge:died", map[string]interface{}{
				"engine": "singbox",
				"key":    shortRuntimeKey(key),
				"error":  err.Error(),
			})
		}
	}

	// 主程序被 watchdog 重启时，浏览器子进程仍然存活；启动后立即按
	// --user-data-dir/--remote-debugging-port 重新接管运行状态，避免实例误显示“已停止”。
	// crashfix probe: 保留首轮同步，但临时停掉常驻 reconciler，验证它是否是后台 exit_code=2 的来源。
	// A clean installation has no profiles to recover. Running the Windows CIM
	// process scan in that state only delays startup and, on affected machines,
	// its five-second timeout can terminate the Wails host with exit code 2.
	// Recovery is useful only after at least one profile has been registered.
	profilesForRecovery := a.browserMgr.List()
	hasRuntimeToRecover := false
	for _, profile := range profilesForRecovery {
		if profile.Running || profile.Pid > 0 || profile.DebugPort > 0 {
			hasRuntimeToRecover = true
			break
		}
	}
	if !a.panelMode && hasRuntimeToRecover {
		a.lifecycleLog("runtime-reconcile", "state=scheduled")
		go func() {
			defer func() {
				if r := recover(); r != nil {
					a.lifecycleLog("runtime-reconcile", "state=panic-recovered", fmt.Sprintf("error=%v", r), fmt.Sprintf("stack=%s", strings.ReplaceAll(string(debug.Stack()), "\n", "\\n")))
				}
			}()
			a.lifecycleLog("runtime-reconcile", "state=started")
			a.reconcileBrowserRuntimeStateOnce()
			a.lifecycleLog("runtime-reconcile", "state=completed")
		}()
	} else if a.panelMode {
		// Panel discovers live envs by process/user-data-dir scan on each refresh
		// (see getSyncProfilesLocal). Snapshot file is optional merge only.
		a.lifecycleLog("runtime-reconcile", "state=skipped", "reason=panel-live-process-scan")
	} else {
		a.lifecycleLog("runtime-reconcile", "state=skipped", "reason=no-live-runtime")
	}
	// Cache maintenance starts well after the Wails/profile/database startup
	// window and only touches stopped environments.
	a.lifecycleLog("cache-auto-clean", "state=scheduled", "initialDelay=2m", "pollInterval=6h")
	a.startCacheAutoCleanScheduler()
	// a.startBrowserRuntimeReconciler()
	// Shared layout-hold flag root for main confiner ↔ panel tile coordination.
	setLayoutHoldRoot(a.appRoot)

	if a.panelMode {
		a.lifecycleLog("sync-engine-owner", "mode=panel-process", "isolation=main-client")
	} else {
		// 全局鼠标/键盘 Hook 不得运行在主 Wails 宿主中。同步面板是独立
		// 进程并直接持有同步引擎，主客户端崩溃或重启时同步仍保持运行。
		a.lifecycleLog("sync-engine-owner", "mode=external-panel", "transport=live-process-scan")
	}

	// v1.6.12: 暂停启动后台代理测速定时器。
	// 线上证据显示 v1.6.10/v1.6.11 主程序按 5~7 分钟周期以 exit_code=2 退出，
	// 与这里“启动后10秒跑首轮 + 每5分钟一轮”的后台测速链路高度吻合。
	// 该测速链路会并发调用第三方代理适配器/网络栈，Go 的 recover 无法拦截 runtime fatal
	// （例如第三方库内部并发 map 读写），所以先从启动路径移除，避免后台任务拖垮主程序。
	// 手动代理测速入口仍保留；后续如需自动测速，应改为独立子进程隔离崩溃。
	a.speedScheduler = nil

	if !a.panelMode {
		// Single owner for wallet/extension/secondary window geometry across every
		// running environment. Sync assistant must not run a parallel SetWindowPos loop.
		a.registerEnvironmentPopupConfiner()
	}

	log.Info("应用启动成功")
	a.lifecycleLog("startup-complete")
}

func shortRuntimeKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return key
	}
	return key[:8]
}

// ReloadConfig 开放给前端重新读取配置，用于应对手动修补后的配置重载
func (a *App) ReloadConfig() error {
	log := logger.New("App")
	cfg, err := LoadConfig(a.resolveAppPath("config.yaml"))
	if err != nil {
		log.Error("重载配置文件失败", logger.F("error", err))
		return fmt.Errorf("重载配置文件失败: %w", err)
	}

	a.ensureLaunchServerAPIKey(cfg)
	a.config = cfg
	a.applyRuntimeConfig(cfg.Runtime)
	// Update browser manager config reference
	if a.browserMgr != nil {
		a.browserMgr.Config = cfg
		a.browserMgr.ListCores()
		a.loadProxies()
		a.reconcileProfileProxyBindings()
	}
	if a.xrayMgr != nil {
		a.xrayMgr.Config = cfg
	}
	if a.clashMgr != nil {
		a.clashMgr.Config = cfg
	}
	if a.singboxMgr != nil {
		a.singboxMgr.Config = cfg
	}
	if a.launchServer != nil {
		a.launchServer.SetAPIAuthConfig(launchcode.APIAuthConfig{
			Enabled: cfg.LaunchServer.Auth.Enabled,
			APIKey:  cfg.LaunchServer.Auth.APIKey,
			Header:  cfg.LaunchServer.Auth.Header,
		})
	}

	log.Info("前端触发配置重载成功")
	return nil
}

func (a *App) applyRuntimeConfig(cfg config.RuntimeConfig) {
	if cfg.GCPercent > 0 {
		debug.SetGCPercent(cfg.GCPercent)
	}
	if cfg.MaxMemoryMB > 0 {
		maxMemoryBytes := int64(cfg.MaxMemoryMB) * 1024 * 1024
		debug.SetMemoryLimit(maxMemoryBytes)
		return
	}
	// 0 表示禁用自定义软限制，避免 ReloadConfig 后残留旧的 GOMEMLIMIT。
	debug.SetMemoryLimit(1 << 60)
}

func (a *App) shutdown(ctx context.Context) {
	log := logger.New("App")
	a.rabbyImportMu.Lock()
	for sessionID := range a.rabbyImports {
		a.clearRabbyImportLocked(sessionID)
	}
	a.rabbyImportActive = make(map[string]bool)
	a.rabbyImportMu.Unlock()
	a.clearLegacyDataRecovery()
	a.lifecycleLog("shutdown", fmt.Sprintf("mode=%d", a.quitMode), fmt.Sprintf("forceQuit=%t", a.forceQuit))
	stopPanelOwnedSync(a)
	if a.shouldStopRuntimeServicesOnShutdown() {
		log.Info("应用正在关闭...")
		a.stopRuntimeServices()
	} else {
		log.Info("应用正在关闭（保留当前已打开的浏览器实例）...")
	}
	a.finalizeShutdown()
}

func (a *App) GetInterceptor() *logger.MethodInterceptor {
	return a.interceptor
}

// ForceQuit 设置强制退出标志并调用 runtime.Quit
func (a *App) ForceQuit() {
	a.lifecycleLog("quit-request", "source=ForceQuit", "mode=app-and-browser")
	a.markIntentionalExit("force-quit")
	a.setQuitMode(quitModeFull)
	a.stopRuntimeServices()
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

// QuitAppOnly 仅退出应用本身，保留当前已打开的浏览器实例。
func (a *App) QuitAppOnly() {
	a.lifecycleLog("quit-request", "source=QuitAppOnly", "mode=app-only")
	a.markIntentionalExit("quit-app-only")
	a.setQuitMode(quitModeAppOnly)
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

func Start(a *App, ctx context.Context) {
	a.startup(ctx)
}

func Stop(a *App, ctx context.Context) {
	a.shutdown(ctx)
}

func platformSupportsTrayCloseFlow() bool {
	return platformSupportsTrayCloseFlowForOS(goruntime.GOOS)
}

func platformSupportsTrayCloseFlowForOS(goos string) bool {
	return strings.EqualFold(strings.TrimSpace(goos), "windows")
}

func (a *App) setQuitMode(mode quitMode) {
	a.forceQuit = true
	a.quitMode = mode
}

func (a *App) shouldStopRuntimeServicesOnShutdown() bool {
	// The sync tool never owns browser runtimes. Its exit must not mark shared
	// live profiles as stopped in the database.
	if a.panelMode {
		return false
	}
	return a.quitMode != quitModeAppOnly
}

func ShouldBlockClose(a *App, ctx context.Context) bool {
	if a.forceQuit {
		a.lifecycleLog("before-close", "action=allow", "reason=forceQuit")
		return false
	}
	if !platformSupportsTrayCloseFlow() {
		a.lifecycleLog("before-close", "action=allow", "reason=no-tray-close-flow")
		return false
	}
	a.lifecycleLog("before-close", "action=block-and-show-confirm")
	runtime.EventsEmit(ctx, "app:request-close")
	return true
}

func (a *App) bindProfileXrayBridge(profileId string, bridgeKey string) {
	profileId = strings.TrimSpace(profileId)
	bridgeKey = strings.TrimSpace(bridgeKey)
	if profileId == "" || bridgeKey == "" {
		return
	}

	a.bridgeMu.Lock()
	a.xrayBridgeRefs[profileId] = bridgeKey
	a.bridgeMu.Unlock()
}

func (a *App) releaseProfileXrayBridge(profileId string) {
	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return
	}

	a.bridgeMu.Lock()
	bridgeKey := a.xrayBridgeRefs[profileId]
	delete(a.xrayBridgeRefs, profileId)
	a.bridgeMu.Unlock()

	if bridgeKey != "" && a.xrayMgr != nil {
		a.xrayMgr.ReleaseBridge(bridgeKey)
	}
}

func (a *App) releaseProfileStandardRelay(profileId string) {
	profileId = strings.TrimSpace(profileId)
	if profileId == "" || a.standardRelayMgr == nil {
		return
	}
	a.standardRelayMgr.Release(profileId)
}

func (a *App) clearProfileXrayBridges() {
	a.bridgeMu.Lock()
	a.xrayBridgeRefs = make(map[string]string)
	a.bridgeMu.Unlock()
}

// ============================================================================
// 仪表盘 API
// ============================================================================

func (a *App) GetDashboardStats() map[string]interface{} {
	profiles := a.browserMgr.List()
	totalInstances := len(profiles)
	runningInstances := 0
	for _, p := range profiles {
		if p.Running {
			runningInstances++
		}
	}
	proxyCount := len(a.config.Browser.Proxies)
	coreCount := len(a.config.Browser.Cores)

	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	memUsedMB := float64(mem.Alloc) / 1024 / 1024

	return map[string]interface{}{
		"totalInstances":   totalInstances,
		"runningInstances": runningInstances,
		"proxyCount":       proxyCount,
		"coreCount":        coreCount,
		"memUsedMB":        int(memUsedMB),
		"appVersion":       a.appVersion(),
	}
}

func (a *App) GetAppConfig() map[string]interface{} {
	return map[string]interface{}{
		"name":    a.appName(),
		"version": a.appVersion(),
	}
}

func (a *App) GetMemoryStats() map[string]interface{} {
	var m goruntime.MemStats
	goruntime.ReadMemStats(&m)
	return map[string]interface{}{
		"alloc_mb":       float64(m.Alloc) / 1024 / 1024,
		"total_alloc_mb": float64(m.TotalAlloc) / 1024 / 1024,
		"sys_mb":         float64(m.Sys) / 1024 / 1024,
		"num_gc":         m.NumGC,
		"limit_mb":       a.config.Runtime.MaxMemoryMB,
		"gc_percent":     a.config.Runtime.GCPercent,
	}
}

func (a *App) TriggerGC()               { goruntime.GC() }
func (a *App) SetLogLevel(level string) { logger.SetGlobalLevelString(level) }
func (a *App) GetLogLevel() string      { return logger.New("App").GetLevel().String() }

// GetAppLogs 获取内存缓冲日志
func (a *App) GetAppLogs() []logger.MemoryLogEntry {
	return logger.GetMemoryWriter().GetEntries()
}

// ClearAppLogs 清空内存缓冲日志
func (a *App) ClearAppLogs() {
	logger.GetMemoryWriter().Clear()
}

// GetRunningInstances 获取运行中实例的详细信息
func (a *App) GetRunningInstances() []BrowserProfile {
	all := a.browserMgr.List()
	result := make([]BrowserProfile, 0)
	for _, p := range all {
		if p.Running {
			result = append(result, p)
		}
	}
	return result
}

// ============================================================================
// 浏览器类型别名 (保持 Wails 绑定兼容)
// ============================================================================

type BrowserProfile = browser.Profile
type BrowserProfileInput = browser.ProfileInput
type BrowserTab = browser.Tab
type BrowserSettings = browser.Settings
type BrowserProxy = browser.Proxy
type BrowserCore = browser.Core
type BrowserCoreInput = browser.CoreInput
type BrowserCoreValidateResult = browser.CoreValidateResult
type BrowserCoreExtendedInfo = browser.CoreExtendedInfo

// ============================================================================
// 浏览器配置 API
// ============================================================================

// BrowserProfileList 获取所有实例列表。
// 运行时状态由启动时的 watchdog 恢复 + 环境 start/stop 事件 + 显式
// RefreshBrowserRuntimeState（见 browser_runtime_recovery_windows.go）维护；
// 这里不再做全系统进程扫描，否则每次列表加载/窗口聚焦/生命周期事件都会
// 触发一次 PowerShell CIM + 窗口枚举，环境越多界面越卡。
func (a *App) BrowserProfileList() []BrowserProfile {
	return a.browserMgr.List()
}

// BrowserProfileListByTag 按标签筛选实例列表
func (a *App) BrowserProfileListByTag(tag string) []BrowserProfile {
	return a.browserMgr.ListByTag(tag)
}

// BrowserGetAllTags 获取所有已使用的标签
func (a *App) BrowserGetAllTags() []string {
	return a.browserMgr.GetAllTags()
}

// BrowserProfileSetKeywords 设置实例关键字
func (a *App) BrowserProfileSetKeywords(profileId string, keywords []string) (*BrowserProfile, error) {
	return a.browserMgr.SetKeywords(profileId, keywords)
}

func (a *App) BrowserProfileCreate(input BrowserProfileInput) (*BrowserProfile, error) {
	return a.browserMgr.Create(input)
}

// BrowserProfileBatchCreate 批量创建实例配置
// 按照 namePrefix + 起始序号 ~ namePrefix + 结束序号 生成多个实例，共用其他字段
func (a *App) BrowserProfileBatchCreate(prefix string, startIndex int, count int, input BrowserProfileInput) ([]*BrowserProfile, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "实例"
	}
	if count <= 0 {
		return nil, fmt.Errorf("批量创建数量必须大于0")
	}
	if count > 2000 {
		return nil, fmt.Errorf("单次批量创建不能超过2000个")
	}
	if startIndex < 1 {
		startIndex = 1
	}

	// Validate the complete requested range before creating anything. Deleted
	// names are intentionally reusable; only profiles that currently exist
	// block creation. Preflight keeps the batch all-or-nothing for name clashes.
	existingNames := make(map[string]string)
	for _, profile := range a.browserMgr.List() {
		name := strings.TrimSpace(profile.ProfileName)
		if name != "" {
			existingNames[strings.ToLower(name)] = name
		}
	}
	conflicts := make([]string, 0)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s-%d", prefix, startIndex+i)
		if existing, found := existingNames[strings.ToLower(name)]; found {
			conflicts = append(conflicts, existing)
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("环境编号已存在：%s。请修改名称前缀或起始序号后重试", strings.Join(conflicts, "、"))
	}

	// 剥离 input 里所有「基础身份 + 种子」相关字段（前端面板的预设/默认值会注入一份）。
	// 否则所有批量创建出来的实例都会共用同一份身份与种子，指纹看起来一模一样。
	// 让 Create 内部为每个实例独立生成连贯身份 + 唯一随机种子。
	stripPrefixes := []string{
		"--fingerprint=",
		"--fingerprint-brand=",
		"--fingerprint-platform=",
		"--lang=",
		"--timezone=",
		"--window-size=",
		"--fingerprint-color-depth=",
		"--fingerprint-hardware-concurrency=",
		"--fingerprint-device-memory=",
		"--fingerprint-canvas-noise=",
		"--fingerprint-audio-noise=",
		"--fingerprint-touch-points=",
		"--fingerprint-fonts=",
		"--webrtc-ip-handling-policy=",
		"--fingerprint-webgl-vendor=",
		"--fingerprint-webgl-renderer=",
	}
	baseFingerprint := make([]string, 0, len(input.FingerprintArgs))
	for _, a := range input.FingerprintArgs {
		la := strings.ToLower(strings.TrimSpace(a))
		drop := false
		for _, p := range stripPrefixes {
			if strings.HasPrefix(la, p) {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		baseFingerprint = append(baseFingerprint, a)
	}

	var created []*BrowserProfile
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s-%d", prefix, startIndex+i)
		profileInput := input
		profileInput.ProfileName = name
		// 每个实例独立分配种子，由 Create 内部检测「无 --fingerprint=」时随机生成
		profileInput.FingerprintArgs = append([]string{}, baseFingerprint...)
		p, err := a.browserMgr.Create(profileInput)
		if err != nil {
			// 遇到错误停止创建，返回已创建的结果和错误信息
			return created, fmt.Errorf("第 %d 个实例创建失败: %w", i+1, err)
		}
		created = append(created, p)
	}
	return created, nil
}

// BrowserProfileRandomizeFingerprint 为单个实例重新生成指纹随机种子（不影响其它指纹/启动参数）
func (a *App) BrowserProfileRandomizeFingerprint(profileId string) (*BrowserProfile, error) {
	return a.browserMgr.RandomizeFingerprint(profileId)
}

// BrowserProfileSetProxyPaused temporarily disables remote proxy for one environment
// without unbinding the pool proxy. Start uses direct:// while paused.
func (a *App) BrowserProfileSetProxyPaused(profileId string, paused bool) (*BrowserProfile, error) {
	return a.browserMgr.SetProxyPaused(profileId, paused)
}

func (a *App) BrowserProfileUpdate(profileId string, input BrowserProfileInput) (*BrowserProfile, error) {
	for _, profile := range a.browserMgr.List() {
		if profile.ProfileId == profileId {
			input.LaunchArgs = preserveAssignedExtensionArgs(profile.LaunchArgs, input.LaunchArgs)
			break
		}
	}
	return a.browserMgr.Update(profileId, input)
}

func (a *App) BrowserProfileDelete(profileId string) error {
	return a.deleteBrowserProfileAndOwnedData(profileId)
}

// BrowserProfileDeleteWithCache retains the legacy signature for existing
// clients. Browser data is archived for user-confirmed recovery.
func (a *App) BrowserProfileDeleteWithCache(profileId string, _ bool) error {
	return a.deleteBrowserProfileAndOwnedData(profileId)
}

func (a *App) deleteBrowserProfileAndOwnedData(profileId string) error {
	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return fmt.Errorf("缺少要删除的环境 ID")
	}
	if err := a.browserMgr.DeleteWithCache(profileId, true); err != nil {
		return err
	}

	cleanupErrors := make([]string, 0, 1)
	if err := a.removeDeletedProfileExtensionReferences(profileId); err != nil {
		cleanupErrors = append(cleanupErrors, err.Error())
	}
	// Publish the authoritative main-client environment list after deletion so
	// the sync assistant cannot retain the removed profile in a later refresh.
	a.PrepareWindowSyncRuntimeSnapshot()
	if len(cleanupErrors) > 0 {
		return fmt.Errorf("环境已删除且数据已归档，但附加记录清理失败：%s", strings.Join(cleanupErrors, "；"))
	}
	return nil
}

// BrowserProfileCopy 复制实例配置（除指纹参数外全部复制）
func (a *App) BrowserProfileCopy(profileId string, newName string) (*BrowserProfile, error) {
	return a.browserMgr.Copy(profileId, newName)
}

// ============================================================================
// 浏览器设置 API
// ============================================================================

func (a *App) GetBrowserSettings() BrowserSettings {
	return BrowserSettings{
		UserDataRoot:           a.config.Browser.UserDataRoot,
		DefaultFingerprintArgs: append([]string{}, a.config.Browser.DefaultFingerprintArgs...),
		DefaultLaunchArgs:      append([]string{}, a.config.Browser.DefaultLaunchArgs...),
		DefaultProxy:           a.config.Browser.DefaultProxy,
		ProxyNetworkMode:       proxy.NormalizeProxyNetworkMode(a.config.Browser.ProxyNetworkMode),
		LocalVPNProxy:          a.config.Browser.LocalVPNProxy,
		StartReadyTimeoutMs:    browserStartReadyTimeoutMillis(a.config),
		StartStableWindowMs:    browserStartStableWindowMillis(a.config),
	}
}

func (a *App) SaveBrowserSettings(settings BrowserSettings) error {
	log := logger.New("Browser")
	a.config.Browser.UserDataRoot = strings.TrimSpace(settings.UserDataRoot)
	a.config.Browser.DefaultFingerprintArgs = append([]string{}, settings.DefaultFingerprintArgs...)
	a.config.Browser.DefaultLaunchArgs = append([]string{}, settings.DefaultLaunchArgs...)
	a.config.Browser.DefaultProxy = strings.TrimSpace(settings.DefaultProxy)
	a.config.Browser.ProxyNetworkMode = proxy.NormalizeProxyNetworkMode(settings.ProxyNetworkMode)
	localVPNProxy := strings.TrimSpace(settings.LocalVPNProxy)
	if localVPNProxy != "" {
		normalized, err := proxy.NormalizeLocalGatewayURL(localVPNProxy)
		if err != nil {
			return fmt.Errorf("本地 VPN 网关格式无效，请使用 http://127.0.0.1:端口 或 socks5://127.0.0.1:端口")
		}
		localVPNProxy = normalized
	}
	a.config.Browser.LocalVPNProxy = localVPNProxy
	if settings.StartReadyTimeoutMs > 0 {
		a.config.Browser.StartReadyTimeoutMs = settings.StartReadyTimeoutMs
	} else if a.config.Browser.StartReadyTimeoutMs <= 0 {
		a.config.Browser.StartReadyTimeoutMs = browserStartReadyTimeoutMillis(nil)
	}
	if settings.StartStableWindowMs > 0 {
		a.config.Browser.StartStableWindowMs = settings.StartStableWindowMs
	} else if a.config.Browser.StartStableWindowMs <= 0 {
		a.config.Browser.StartStableWindowMs = browserStartStableWindowMillis(nil)
	}
	if err := a.config.Save(a.resolveAppPath("config.yaml")); err != nil {
		log.Error("浏览器配置保存失败", logger.F("error", err))
		return err
	}
	return nil
}

// ============================================================================
// 内核管理 API
// ============================================================================

func (a *App) BrowserCoreList() []BrowserCore {
	return a.browserMgr.ListCores()
}

func (a *App) BrowserCoreSave(input BrowserCoreInput) error {
	return a.browserMgr.SaveCore(input)
}

func (a *App) BrowserCoreDelete(coreId string) error {
	return a.browserMgr.DeleteCore(coreId)
}

func (a *App) BrowserCoreSetDefault(coreId string) error {
	return a.browserMgr.SetDefaultCore(coreId)
}

func (a *App) BrowserCoreValidate(corePath string) BrowserCoreValidateResult {
	return a.browserMgr.ValidateCorePath(corePath)
}

func (a *App) BrowserCoreExtendedInfo() []BrowserCoreExtendedInfo {
	return a.browserMgr.GetCoresExtendedInfo()
}

// BrowserCoreScan 重新扫描 chrome 目录，自动注册新内核
func (a *App) BrowserCoreScan() []BrowserCore {
	a.autoDetectCores()
	return a.browserMgr.ListCores()
}

// BrowserCoreDownload 在线下载并自动解压配置内核
func (a *App) BrowserCoreDownload(coreName, url, proxyConfig string) error {
	if a.ctx == nil {
		return fmt.Errorf("app context is nil")
	}
	// 异步启动下载流程，以防阻塞前端请求，通过 Wails events 发送进度
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.New("App").Error("DownloadAndExtractCore goroutine panic recovered",
					logger.F("core_name", coreName),
					logger.F("error", r),
				)
			}
		}()
		a.browserMgr.DownloadAndExtractCore(a.ctx, coreName, url, proxyConfig)
	}()
	return nil
}

// ============================================================================
// 代理池 API
// ============================================================================

// ProxyValidationResult 代理验证结果
type ProxyValidationResult struct {
	Supported bool   `json:"supported"`
	ErrorMsg  string `json:"errorMsg"`
}

func (a *App) BrowserProxyList() []BrowserProxy {
	if a.browserMgr.ProxyDAO != nil {
		if list, err := a.browserMgr.ProxyDAO.List(); err == nil {
			return list
		}
	}
	return append([]BrowserProxy{}, a.config.Browser.Proxies...)
}

// BrowserProxyListGroups 获取所有代理分组名称
func (a *App) BrowserProxyListGroups() []string {
	if a.browserMgr.ProxyDAO != nil {
		if groups, err := a.browserMgr.ProxyDAO.ListGroups(); err == nil {
			return groups
		}
	}
	return nil
}

// BrowserProxyListByGroup 按分组名称查询代理
func (a *App) BrowserProxyListByGroup(groupName string) []BrowserProxy {
	if a.browserMgr.ProxyDAO != nil {
		if list, err := a.browserMgr.ProxyDAO.ListByGroup(groupName); err == nil {
			return list
		}
	}
	// 降级：内存过滤
	var result []BrowserProxy
	for _, p := range a.config.Browser.Proxies {
		if p.GroupName == groupName {
			result = append(result, p)
		}
	}
	return result
}

// ValidateProxyConfig 验证代理配置是否支持
func (a *App) ValidateProxyConfig(proxyConfig string, proxyId string) ProxyValidationResult {
	proxies := a.getLatestProxies()
	supported, errorMsg := proxy.ValidateProxyConfig(proxyConfig, proxies, proxyId)
	return ProxyValidationResult{
		Supported: supported,
		ErrorMsg:  errorMsg,
	}
}

// ProxyTestResult 代理测试结果（对齐多账号浏览器「一键检测」可读字段，非照搬）
type ProxyTestResult struct {
	ProxyId        string `json:"proxyId"`
	Ok             bool   `json:"ok"`
	LatencyMs      int64  `json:"latencyMs"`
	Error          string `json:"error"`
	ResolvedConfig string `json:"resolvedConfig"`
	// Protocol is the working scheme after detect (socks5/http/https).
	Protocol string `json:"protocol"`
	// Human-readable summary for UI toasts (zh).
	Message string `json:"message"`
	// Exit meta filled by BrowserProxyFullCheck (optional on speed-only calls).
	ExitIP        string `json:"exitIP,omitempty"`
	Country       string `json:"country,omitempty"`
	City          string `json:"city,omitempty"`
	TimezoneHint  string `json:"timezoneHint,omitempty"`
	IsResidential bool   `json:"isResidential,omitempty"`
	DNSViaProxy   bool   `json:"dnsViaProxy"`
}

// ProxyIPHealthResult 代理出口 IP 健康信息（透传第三方接口结果）
type ProxyIPHealthResult struct {
	ProxyId        string                 `json:"proxyId"`
	Ok             bool                   `json:"ok"`
	Source         string                 `json:"source"`
	Error          string                 `json:"error"`
	IP             string                 `json:"ip"`
	FraudScore     int64                  `json:"fraudScore"`
	IsResidential  bool                   `json:"isResidential"`
	IsBroadcast    bool                   `json:"isBroadcast"`
	Country        string                 `json:"country"`
	Region         string                 `json:"region"`
	City           string                 `json:"city"`
	AsOrganization string                 `json:"asOrganization"`
	RawData        map[string]interface{} `json:"rawData"`
	UpdatedAt      string                 `json:"updatedAt"`
}

// TestProxyConnectivity 测试代理连通性
func (a *App) TestProxyConnectivity(proxyId string, proxyConfig string) ProxyTestResult {
	proxies := a.getLatestProxies()
	r := proxy.TestConnectivity(proxyId, proxyConfig, proxies, nil)
	return proxyTestResultFromInternal(r)
}

// TestProxyRealConnectivity 通过真实 HTTP 请求测试代理连通性（Wails 绑定）
// 参考 Clash URLTest 策略：多 URL fallback + 复用桥接 + TCP ping 降级
func (a *App) TestProxyRealConnectivity(proxyId string) ProxyTestResult {
	proxies := a.getLatestProxies()
	r := proxy.SpeedTest(proxyId, proxies, a.xrayMgr, a.singboxMgr, nil)
	return proxyTestResultFromInternal(r)
}

// TestProxyConfigRealConnectivity validates an unsaved edit through a real
// authenticated HTTP request. It also returns the protocol that actually
// worked, allowing the editor to correct provider lists labelled with the
// wrong HTTP/SOCKS5 scheme before the browser is launched.
func (a *App) TestProxyConfigRealConnectivity(proxyConfig string) ProxyTestResult {
	const previewID = "__proxy_edit_preview__"
	candidate := config.BrowserProxy{ProxyId: previewID, ProxyName: previewID, ProxyConfig: strings.TrimSpace(proxyConfig)}
	r := proxy.SpeedTest(previewID, []config.BrowserProxy{candidate}, a.xrayMgr, a.singboxMgr, nil)
	return proxyTestResultFromInternal(r)
}

func (a *App) persistDetectedStandardProxy(proxyID, currentConfig, resolvedConfig string) {
	proxyID = strings.TrimSpace(proxyID)
	currentConfig = strings.TrimSpace(currentConfig)
	resolvedConfig = strings.TrimSpace(resolvedConfig)
	if proxyID == "" || resolvedConfig == "" ||
		strings.EqualFold(currentConfig, resolvedConfig) ||
		!proxy.IsStandardProxyURL(resolvedConfig) {
		return
	}

	if dao, ok := a.browserMgr.ProxyDAO.(interface {
		UpdateProxyConfigIfCurrent(string, string, string) (bool, error)
	}); ok {
		updated, err := dao.UpdateProxyConfigIfCurrent(proxyID, currentConfig, resolvedConfig)
		if err != nil {
			logger.New("Browser").Warn("代理协议识别结果保存失败",
				logger.F("proxy_id", proxyID),
				logger.F("error", err.Error()),
			)
		} else if updated {
			logger.New("Browser").Info("代理协议已自动修正",
				logger.F("proxy_id", proxyID),
				logger.F("resolved_protocol", strings.SplitN(resolvedConfig, "://", 2)[0]),
			)
		}
		return
	}

	if a.config == nil {
		return
	}
	for i := range a.config.Browser.Proxies {
		item := &a.config.Browser.Proxies[i]
		if item.ProxyId == proxyID && strings.EqualFold(strings.TrimSpace(item.ProxyConfig), currentConfig) {
			item.ProxyConfig = resolvedConfig
			_ = config.SaveProxies(a.resolveAppPath("proxies.yaml"), a.config.Browser.Proxies)
			return
		}
	}
}

func proxyTestResultFromInternal(r proxy.TestResult) ProxyTestResult {
	out := ProxyTestResult{
		ProxyId:        r.ProxyId,
		Ok:             r.Ok,
		LatencyMs:      r.LatencyMs,
		Error:          r.Error,
		ResolvedConfig: r.ResolvedConfig,
		// External standard proxies always go through local relay → DNS via proxy.
		DNSViaProxy: true,
	}
	cfg := strings.TrimSpace(r.ResolvedConfig)
	if cfg == "" {
		cfg = ""
	}
	if scheme := proxy.PreferredSchemeFromProxySource(cfg); scheme != "" {
		out.Protocol = scheme
	} else if idx := strings.Index(cfg, "://"); idx > 0 {
		out.Protocol = strings.ToLower(cfg[:idx])
	}
	if r.Ok {
		if out.LatencyMs > 0 {
			out.Message = fmt.Sprintf("连通正常 · %s · %dms", preferProtocolLabel(out.Protocol), out.LatencyMs)
		} else {
			out.Message = fmt.Sprintf("连通正常 · %s", preferProtocolLabel(out.Protocol))
		}
	} else {
		out.Message = humanizeProxyError(r.Error)
		if out.Message == "" {
			out.Message = "代理不可用"
		}
	}
	return out
}

func preferProtocolLabel(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "socks5":
		return "SOCKS5"
	case "https":
		return "HTTPS"
	case "http":
		return "HTTP"
	case "":
		return "直连/未知"
	default:
		return strings.ToUpper(protocol)
	}
}

func humanizeProxyError(errText string) string {
	errText = strings.TrimSpace(errText)
	if errText == "" {
		return ""
	}
	lower := strings.ToLower(errText)
	switch {
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "407") || strings.Contains(lower, "auth"):
		return "代理认证失败，请检查账号密码"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline"):
		return "代理连接超时，节点可能过载或被墙"
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "connect:"):
		return "无法连接代理端口，请检查地址与端口"
	case strings.Contains(lower, "protocol") || strings.Contains(lower, "socks"):
		return "协议不匹配，已尝试 HTTP/SOCKS 自动识别仍失败"
	case strings.Contains(lower, "无法通过代理访问互联网") || strings.Contains(lower, "internet"):
		return "端口可达但无法经代理上网（协议/认证/出口异常）"
	default:
		return errText
	}
}

// BrowserProxyFullCheck is a one-click check (AdsPower/MoreLogin style summary):
// real latency + working protocol + exit IP/geo when reachable. Does not change
// the necessary start-time connectivity probe path.
func (a *App) BrowserProxyFullCheck(proxyId string) ProxyTestResult {
	result := a.BrowserProxyTestSpeed(proxyId)
	if !result.Ok {
		return result
	}
	// Enrich with exit IP when speed already proves the tunnel works.
	health := a.BrowserProxyCheckIPHealth(proxyId)
	if health.Ok {
		result.ExitIP = health.IP
		result.Country = health.Country
		result.City = health.City
		result.IsResidential = health.IsResidential
		result.TimezoneHint = proxy.TimezoneHintFromCountry(health.Country)
		geo := strings.TrimSpace(strings.Join([]string{health.Country, health.City}, " · "))
		if geo == " · " {
			geo = ""
		}
		parts := []string{result.Message}
		if health.IP != "" {
			parts = append(parts, "出口 "+health.IP)
		}
		if geo != "" && geo != "·" {
			parts = append(parts, geo)
		}
		if result.TimezoneHint != "" {
			parts = append(parts, "建议时区 "+result.TimezoneHint)
		}
		if health.IsResidential {
			parts = append(parts, "住宅属性")
		}
		result.Message = strings.Join(parts, " · ")
	}
	return result
}

// SuggestTimezoneFromProxy returns an IANA timezone hint from the last IP
// health data of a proxy (for fingerprint alignment with exit geo).
func (a *App) SuggestTimezoneFromProxy(proxyId string) string {
	proxyId = strings.TrimSpace(proxyId)
	if proxyId == "" || a.browserMgr == nil || a.browserMgr.ProxyDAO == nil {
		return ""
	}
	// Prefer live health fetch cache via DAO persisted JSON.
	list := a.getLatestProxies()
	for _, item := range list {
		if !strings.EqualFold(item.ProxyId, proxyId) {
			continue
		}
		if strings.TrimSpace(item.LastIPHealthJSON) == "" {
			break
		}
		var health ProxyIPHealthResult
		if json.Unmarshal([]byte(item.LastIPHealthJSON), &health) == nil && health.Ok {
			if tz := proxy.TimezoneHintFromCountry(health.Country); tz != "" {
				return tz
			}
		}
		break
	}
	// Fallback: one health check (network).
	health := a.BrowserProxyCheckIPHealth(proxyId)
	if health.Ok {
		return proxy.TimezoneHintFromCountry(health.Country)
	}
	return ""
}

// BrowserProxyTestSpeed 手动触发单个代理测速并持久化结果
func (a *App) BrowserProxyTestSpeed(proxyId string) ProxyTestResult {
	proxies := a.getLatestProxies()
	r := proxy.SpeedTest(proxyId, proxies, a.xrayMgr, a.singboxMgr, nil)
	for _, item := range proxies {
		if strings.EqualFold(item.ProxyId, proxyId) {
			a.persistDetectedStandardProxy(proxyId, item.ProxyConfig, r.ResolvedConfig)
			break
		}
	}
	if a.browserMgr.ProxyDAO != nil {
		testedAt := time.Now().Format(time.RFC3339)
		_ = a.browserMgr.ProxyDAO.UpdateSpeedResult(proxyId, r.Ok, r.LatencyMs, testedAt)
	}
	return proxyTestResultFromInternal(r)
}

// BrowserProxyBatchTestSpeed 批量并发测速，concurrency 控制并发数（默认 20）
func (a *App) BrowserProxyBatchTestSpeed(proxyIds []string, concurrency int) []ProxyTestResult {
	if len(proxyIds) == 0 {
		return []ProxyTestResult{}
	}
	if concurrency <= 0 {
		concurrency = 8
	}
	if concurrency > 8 {
		concurrency = 8
	}
	if concurrency > len(proxyIds) {
		concurrency = len(proxyIds)
	}
	proxies := a.getLatestProxies()
	results := make([]ProxyTestResult, len(proxyIds))
	type speedJob struct {
		Idx     int
		ProxyId string
	}
	jobs := make(chan speedJob, len(proxyIds))
	var wg sync.WaitGroup

	// 固定大小 worker 池，避免大量代理时创建过多 goroutine
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.New("App").Error("proxy speed test goroutine panic recovered",
						logger.F("error", r),
					)
				}
			}()
			for job := range jobs {
				r := proxy.SpeedTest(job.ProxyId, proxies, a.xrayMgr, a.singboxMgr, nil)
				for _, item := range proxies {
					if strings.EqualFold(item.ProxyId, job.ProxyId) {
						a.persistDetectedStandardProxy(job.ProxyId, item.ProxyConfig, r.ResolvedConfig)
						break
					}
				}
				if a.browserMgr.ProxyDAO != nil {
					testedAt := time.Now().Format(time.RFC3339)
					_ = a.browserMgr.ProxyDAO.UpdateSpeedResult(job.ProxyId, r.Ok, r.LatencyMs, testedAt)
				}
				result := proxyTestResultFromInternal(r)
				results[job.Idx] = result

				// 实时推送单个结果到前端
				if a.ctx != nil {
					runtime.EventsEmit(a.ctx, "proxy:speed:result", result)
				}
			}
		}()
	}

	for i, pid := range proxyIds {
		jobs <- speedJob{Idx: i, ProxyId: pid}
	}
	close(jobs)

	wg.Wait()
	return results
}

// BrowserProxyCheckIPHealth 检测单个代理的出口 IP 健康信息（通过 IPPure 接口）
func (a *App) BrowserProxyCheckIPHealth(proxyId string) ProxyIPHealthResult {
	proxies := a.getLatestProxies()
	data, err := proxy.FetchIPPureInfo(proxyId, proxies, a.xrayMgr, a.singboxMgr)
	result := buildProxyIPHealthResult(proxyId, data, err)
	a.persistProxyIPHealthResult(result)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "proxy:iphealth:result", result)
	}
	return result
}

// BrowserProxyBatchCheckIPHealth 批量并发检测代理出口 IP 健康信息
func (a *App) BrowserProxyBatchCheckIPHealth(proxyIds []string, concurrency int) []ProxyIPHealthResult {
	if len(proxyIds) == 0 {
		return []ProxyIPHealthResult{}
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > 4 {
		concurrency = 4
	}
	if concurrency > len(proxyIds) {
		concurrency = len(proxyIds)
	}

	proxies := a.getLatestProxies()
	results := make([]ProxyIPHealthResult, len(proxyIds))
	type healthJob struct {
		Idx     int
		ProxyId string
	}
	jobs := make(chan healthJob, len(proxyIds))
	var wg sync.WaitGroup

	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.New("App").Error("proxy IP health goroutine panic recovered",
						logger.F("error", r),
					)
				}
			}()
			for job := range jobs {
				data, err := proxy.FetchIPPureInfo(job.ProxyId, proxies, a.xrayMgr, a.singboxMgr)
				result := buildProxyIPHealthResult(job.ProxyId, data, err)
				a.persistProxyIPHealthResult(result)
				results[job.Idx] = result
				if a.ctx != nil {
					runtime.EventsEmit(a.ctx, "proxy:iphealth:result", result)
				}
			}
		}()
	}

	for i, pid := range proxyIds {
		jobs <- healthJob{Idx: i, ProxyId: pid}
	}
	close(jobs)

	wg.Wait()
	return results
}

func buildProxyIPHealthResult(proxyId string, data map[string]interface{}, err error) ProxyIPHealthResult {
	if err != nil {
		return ProxyIPHealthResult{
			ProxyId:   proxyId,
			Ok:        false,
			Source:    "ippure",
			Error:     err.Error(),
			RawData:   map[string]interface{}{},
			UpdatedAt: time.Now().Format(time.RFC3339),
		}
	}

	if data == nil {
		data = map[string]interface{}{}
	}

	return ProxyIPHealthResult{
		ProxyId:        proxyId,
		Ok:             true,
		Source:         "ippure",
		Error:          "",
		IP:             mapString(data, "ip"),
		FraudScore:     mapInt64(data, "fraudScore"),
		IsResidential:  mapBool(data, "isResidential"),
		IsBroadcast:    mapBool(data, "isBroadcast"),
		Country:        mapString(data, "country"),
		Region:         mapString(data, "region"),
		City:           mapString(data, "city"),
		AsOrganization: mapString(data, "asOrganization"),
		RawData:        data,
		UpdatedAt:      time.Now().Format(time.RFC3339),
	}
}

func (a *App) persistProxyIPHealthResult(result ProxyIPHealthResult) {
	if a.browserMgr.ProxyDAO == nil {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = a.browserMgr.ProxyDAO.UpdateIPHealthResult(result.ProxyId, string(payload))
}

func mapString(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprint(v)
	}
}

func mapInt64(m map[string]interface{}, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float32:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		if iv, err := n.Int64(); err == nil {
			return iv
		}
		if fv, err := n.Float64(); err == nil {
			return int64(fv)
		}
	case string:
		if iv, err := strconv.ParseInt(n, 10, 64); err == nil {
			return iv
		}
		if fv, err := strconv.ParseFloat(n, 64); err == nil {
			return int64(fv)
		}
	}
	return 0
}

func mapBool(m map[string]interface{}, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true") || b == "1"
	case int:
		return b != 0
	case int64:
		return b != 0
	case float64:
		return b != 0
	}
	return false
}

// getLatestProxies 获取最新的代理列表，优先从数据库读取
func (a *App) getLatestProxies() []BrowserProxy {
	if a.browserMgr.ProxyDAO != nil {
		if list, err := a.browserMgr.ProxyDAO.List(); err == nil && len(list) > 0 {
			return list
		}
	}
	return a.config.Browser.Proxies
}

func normalizeBrowserProxy(item BrowserProxy, sortOrder int) (BrowserProxy, error) {
	proxyName := strings.TrimSpace(item.ProxyName)
	proxyConfig := strings.TrimSpace(item.ProxyConfig)
	if proxyName == "" || proxyConfig == "" {
		return BrowserProxy{}, fmt.Errorf("代理名称和代理配置不能为空")
	}
	if proxy.LooksLikeStandardProxyConfig(proxyConfig) {
		standardConfig, err := proxy.NormalizeStandardProxyConfig(proxyConfig, "http")
		if err != nil {
			return BrowserProxy{}, fmt.Errorf("代理 %q 格式无效: %w", proxyName, err)
		}
		proxyConfig = standardConfig
	}
	proxyID := strings.TrimSpace(item.ProxyId)
	if proxyID == "" {
		proxyID = generateUUID()
	}
	sourceURL := strings.TrimSpace(item.SourceURL)
	sourceID := strings.TrimSpace(item.SourceID)
	sourceNamePrefix := strings.TrimSpace(item.SourceNamePrefix)
	sourceLastRefreshAt := strings.TrimSpace(item.SourceLastRefreshAt)
	sourceRefreshIntervalM := item.SourceRefreshIntervalM
	if sourceRefreshIntervalM < 0 {
		sourceRefreshIntervalM = 0
	}
	if sourceRefreshIntervalM > 24*60 {
		sourceRefreshIntervalM = 24 * 60
	}
	sourceAutoRefresh := item.SourceAutoRefresh && sourceURL != ""
	if sourceAutoRefresh && sourceRefreshIntervalM <= 0 {
		sourceRefreshIntervalM = 60
	}
	if !sourceAutoRefresh {
		sourceRefreshIntervalM = 0
	}
	if sourceURL == "" {
		sourceID = ""
		sourceNamePrefix = ""
		sourceLastRefreshAt = ""
		sourceAutoRefresh = false
		sourceRefreshIntervalM = 0
	}
	return BrowserProxy{
		ProxyId:                proxyID,
		ProxyName:              proxyName,
		ProxyConfig:            proxyConfig,
		DnsServers:             strings.TrimSpace(item.DnsServers),
		GroupName:              strings.TrimSpace(item.GroupName),
		SourceID:               sourceID,
		SourceURL:              sourceURL,
		SourceNamePrefix:       sourceNamePrefix,
		SourceAutoRefresh:      sourceAutoRefresh,
		SourceRefreshIntervalM: sourceRefreshIntervalM,
		SourceLastRefreshAt:    sourceLastRefreshAt,
		SortOrder:              sortOrder,
	}, nil
}

// UpsertBrowserProxy 只写入发生变化的一条代理。保存不触发网络验证，也不扫描全部实例。
func (a *App) UpsertBrowserProxy(item BrowserProxy) (BrowserProxy, error) {
	latest := a.getLatestProxies()
	sortOrder := len(latest)
	for _, existing := range latest {
		if existing.ProxyId == item.ProxyId {
			sortOrder = existing.SortOrder
			break
		}
	}
	normalized, err := normalizeBrowserProxy(item, sortOrder)
	if err != nil {
		return BrowserProxy{}, err
	}
	if a.browserMgr.ProxyDAO != nil {
		if err := a.browserMgr.ProxyDAO.Upsert(normalized); err != nil {
			return BrowserProxy{}, err
		}
	} else {
		replaced := false
		for i := range latest {
			if latest[i].ProxyId == normalized.ProxyId {
				latest[i] = normalized
				replaced = true
				break
			}
		}
		if !replaced {
			latest = append(latest, normalized)
		}
		if err := config.SaveProxies(a.resolveAppPath("proxies.yaml"), latest); err != nil {
			return BrowserProxy{}, err
		}
	}
	if a.config != nil {
		found := false
		for i := range a.config.Browser.Proxies {
			if a.config.Browser.Proxies[i].ProxyId == normalized.ProxyId {
				a.config.Browser.Proxies[i] = normalized
				found = true
				break
			}
		}
		if !found {
			a.config.Browser.Proxies = append(a.config.Browser.Proxies, normalized)
		}
	}
	return normalized, nil
}

// DeleteBrowserProxies 删除指定代理后立即返回；实例绑定修复在后台完成，
// 避免代理池按钮被数百个环境的扫描与配置落盘阻塞。
func (a *App) DeleteBrowserProxies(proxyIDs []string) error {
	deleteSet := make(map[string]struct{}, len(proxyIDs))
	cleanIDs := make([]string, 0, len(proxyIDs))
	for _, proxyID := range proxyIDs {
		proxyID = strings.TrimSpace(proxyID)
		if proxyID == "" || proxyID == "__direct__" || proxyID == "__local__" {
			continue
		}
		if _, exists := deleteSet[proxyID]; exists {
			continue
		}
		deleteSet[proxyID] = struct{}{}
		cleanIDs = append(cleanIDs, proxyID)
	}
	if len(cleanIDs) == 0 {
		return nil
	}
	if a.browserMgr.ProxyDAO != nil {
		if batchDAO, ok := a.browserMgr.ProxyDAO.(interface{ DeleteMany([]string) error }); ok {
			if err := batchDAO.DeleteMany(cleanIDs); err != nil {
				return err
			}
		} else {
			for _, proxyID := range cleanIDs {
				if err := a.browserMgr.ProxyDAO.Delete(proxyID); err != nil {
					return err
				}
			}
		}
	} else {
		latest := a.getLatestProxies()
		kept := latest[:0]
		for _, item := range latest {
			if _, deleting := deleteSet[item.ProxyId]; !deleting {
				kept = append(kept, item)
			}
		}
		if err := config.SaveProxies(a.resolveAppPath("proxies.yaml"), kept); err != nil {
			return err
		}
	}
	if a.config != nil {
		kept := a.config.Browser.Proxies[:0]
		for _, item := range a.config.Browser.Proxies {
			if _, deleting := deleteSet[item.ProxyId]; !deleting {
				kept = append(kept, item)
			}
		}
		a.config.Browser.Proxies = kept
	}
	go a.reconcileProfileProxyBindings()
	return nil
}

func (a *App) SaveBrowserProxies(proxies []BrowserProxy) error {
	log := logger.New("Browser")
	normalized := make([]BrowserProxy, 0, len(proxies))
	for i, item := range proxies {
		if strings.TrimSpace(item.ProxyName) == "" || strings.TrimSpace(item.ProxyConfig) == "" {
			continue
		}
		item, err := normalizeBrowserProxy(item, i)
		if err != nil {
			return err
		}
		normalized = append(normalized, item)
	}

	// 确保内置代理始终存在（直连 + 本地代理）
	builtins := []BrowserProxy{
		{ProxyId: "__direct__", ProxyName: "直连（不走代理）", ProxyConfig: "direct://"},
		{ProxyId: "__local__", ProxyName: "本地代理", ProxyConfig: "http://127.0.0.1:7890"},
	}
	for _, b := range builtins {
		found := false
		for _, p := range normalized {
			if p.ProxyId == b.ProxyId {
				found = true
				break
			}
		}
		if !found {
			normalized = append([]BrowserProxy{b}, normalized...)
		}
	}

	a.config.Browser.Proxies = normalized

	// 优先写入 SQLite
	if a.browserMgr.ProxyDAO != nil {
		if replaceDAO, ok := a.browserMgr.ProxyDAO.(interface{ ReplaceAll([]browser.Proxy) error }); ok {
			if err := replaceDAO.ReplaceAll(normalized); err != nil {
				log.Error("代理列表事务保存失败", logger.F("error", err))
				return err
			}
		} else {
			if err := a.browserMgr.ProxyDAO.DeleteAll(); err != nil {
				log.Error("清空代理表失败", logger.F("error", err))
				return err
			}
			for _, p := range normalized {
				if err := a.browserMgr.ProxyDAO.Upsert(p); err != nil {
					log.Error("代理保存失败", logger.F("proxy_id", p.ProxyId), logger.F("error", err))
					return err
				}
			}
		}
		log.Info("代理列表已保存到数据库", logger.F("count", len(normalized)))
		a.reconcileProfileProxyBindings()
		return nil
	}

	// 降级：写入 proxies.yaml
	if err := config.SaveProxies(a.resolveAppPath("proxies.yaml"), normalized); err != nil {
		log.Error("代理列表保存失败", logger.F("error", err))
		return err
	}
	a.reconcileProfileProxyBindings()
	return nil
}

// ============================================================================
// 文件系统 API
// ============================================================================

// OpenUserDataDir 在资源管理器中打开用户数据目录
func (a *App) OpenUserDataDir(userDataDir string) error {
	log := logger.New("Browser")

	// 解析完整路径
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return fmt.Errorf("用户数据目录不能为空")
	}

	var fullPath string
	if filepath.IsAbs(userDataDir) {
		fullPath = userDataDir
	} else {
		root := strings.TrimSpace(a.config.Browser.UserDataRoot)
		if root == "" {
			root = "data"
		}
		root = a.resolveAppPath(root)
		fullPath = filepath.Join(root, userDataDir)
	}

	// 检查目录是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		// 目录不存在，尝试创建
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			log.Error("创建用户数据目录失败", logger.F("path", fullPath), logger.F("error", err))
			return fmt.Errorf("创建目录失败: %v", err)
		}
	}

	// 获取绝对路径
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		log.Error("获取绝对路径失败", logger.F("path", fullPath), logger.F("error", err))
		return err
	}

	if err := openPathInFileManager(absPath); err != nil {
		log.Error("打开资源管理器失败", logger.F("path", absPath), logger.F("error", err))
		return err
	}

	log.Info("已打开用户数据目录", logger.F("path", absPath))
	return nil
}

// OpenCorePath 在资源管理器中打开内核路径
func (a *App) OpenCorePath(corePath string) error {
	log := logger.New("Browser")

	corePath = strings.TrimSpace(corePath)
	if corePath == "" {
		return fmt.Errorf("内核路径不能为空")
	}

	var fullPath string
	if filepath.IsAbs(corePath) {
		fullPath = corePath
	} else {
		fullPath = a.resolveAppPath(corePath)
	}

	// 检查目录是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fmt.Errorf("路径不存在: %s", fullPath)
	}

	// 获取绝对路径
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		log.Error("获取绝对路径失败", logger.F("path", fullPath), logger.F("error", err))
		return err
	}

	if err := openPathInFileManager(absPath); err != nil {
		log.Error("打开资源管理器失败", logger.F("path", absPath), logger.F("error", err))
		return err
	}

	log.Info("已打开内核路径", logger.F("path", absPath))
	return nil
}

// openPathInFileManager 调用系统文件管理器打开路径。
// Windows 下不能复用 hideWindow，否则可能导致资源管理器窗口被隐藏。
func openPathInFileManager(absPath string) error {
	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	switch goruntime.GOOS {
	case "windows":
		if info.IsDir() {
			return exec.Command("explorer.exe", absPath).Start()
		}
		return exec.Command("explorer.exe", "/select,", absPath).Start()
	case "darwin":
		if info.IsDir() {
			return exec.Command("open", absPath).Start()
		}
		return exec.Command("open", "-R", absPath).Start()
	default:
		target := absPath
		if !info.IsDir() {
			target = filepath.Dir(absPath)
		}
		return exec.Command("xdg-open", target).Start()
	}
}

// ============================================================================
// 数据迁移
// ============================================================================

// migrateToSQLite 一次性迁移：若 SQLite 表为空则从旧文件导入数据，或初始化默认数据
// 迁移顺序：cores → proxies → profiles → bookmarks
func (a *App) migrateToSQLite() {
	log := logger.New("Migration")

	// 迁移/初始化内核
	if cores, err := a.browserMgr.CoreDAO.List(); err == nil && len(cores) == 0 {
		// 优先从 config.yaml 迁移
		if len(a.config.Browser.Cores) > 0 {
			for _, c := range a.config.Browser.Cores {
				if err := a.browserMgr.CoreDAO.Upsert(c); err != nil {
					log.Error("内核迁移失败", logger.F("core_id", c.CoreId), logger.F("error", err))
				}
			}
			log.Info("内核数据已迁移", logger.F("count", len(a.config.Browser.Cores)))
		} else {
			// 初始化默认内核（自动检测会补充）
			log.Info("内核表为空，将通过自动检测初始化")
		}
	}

	// 迁移/初始化代理
	if proxies, err := a.browserMgr.ProxyDAO.List(); err == nil && len(proxies) == 0 {
		var srcProxies []browser.Proxy
		// 优先 proxies.yaml，其次 config.yaml
		if loaded, err := config.LoadProxies(a.resolveAppPath("proxies.yaml")); err == nil && len(loaded) > 0 {
			srcProxies = loaded
		} else if len(a.config.Browser.Proxies) > 0 {
			srcProxies = a.config.Browser.Proxies
		} else {
			// 初始化默认代理
			srcProxies = []browser.Proxy{
				{ProxyId: "__direct__", ProxyName: "直连（不走代理）", ProxyConfig: "direct://"},
				{ProxyId: "__local__", ProxyName: "本地代理", ProxyConfig: "http://127.0.0.1:7890"},
			}
			log.Info("代理表为空，初始化默认代理")
		}
		for _, p := range srcProxies {
			if err := a.browserMgr.ProxyDAO.Upsert(p); err != nil {
				log.Error("代理迁移失败", logger.F("proxy_id", p.ProxyId), logger.F("error", err))
			}
		}
		if len(srcProxies) > 0 {
			log.Info("代理数据已初始化", logger.F("count", len(srcProxies)))
		}
	}

	// 迁移实例配置（如果为空则自动创建一个默认实例）
	if profiles, err := a.browserMgr.ProfileDAO.List(); err == nil && len(profiles) == 0 {
		if len(a.config.Browser.Profiles) > 0 {
			for _, pc := range a.config.Browser.Profiles {
				coreId := strings.TrimSpace(pc.CoreId)
				if strings.EqualFold(coreId, "default") {
					coreId = ""
				}
				p := &browser.Profile{
					ProfileId:          pc.ProfileId,
					ProfileName:        pc.ProfileName,
					UserDataDir:        pc.UserDataDir,
					CoreId:             coreId,
					FingerprintArgs:    pc.FingerprintArgs,
					ProxyId:            pc.ProxyId,
					ProxyConfig:        pc.ProxyConfig,
					ProxyBindSourceID:  pc.ProxyBindSourceID,
					ProxyBindSourceURL: pc.ProxyBindSourceURL,
					ProxyBindName:      pc.ProxyBindName,
					ProxyBindUpdatedAt: pc.ProxyBindUpdatedAt,
					LaunchArgs:         pc.LaunchArgs,
					Tags:               pc.Tags,
					Keywords:           pc.Keywords,
					CreatedAt:          pc.CreatedAt,
					UpdatedAt:          pc.UpdatedAt,
				}
				if err := a.browserMgr.ProfileDAO.Upsert(p); err != nil {
					log.Error("实例迁移失败", logger.F("profile_id", pc.ProfileId), logger.F("error", err))
				}
			}
			log.Info("实例数据已迁移", logger.F("count", len(a.config.Browser.Profiles)))
		} else {
			log.Info("实例表为空，自动创建默认实例")
			defaultProfile := &browser.Profile{
				ProfileId:       generateUUID(),
				ProfileName:     "默认实例",
				UserDataDir:     "default",
				CoreId:          "",
				FingerprintArgs: a.config.Browser.DefaultFingerprintArgs,
				LaunchArgs:      a.config.Browser.DefaultLaunchArgs,
				Tags:            []string{"默认"},
				ProxyId:         a.config.Browser.DefaultProxy,
				CreatedAt:       time.Now().Format(time.RFC3339),
				UpdatedAt:       time.Now().Format(time.RFC3339),
			}
			if err := a.browserMgr.ProfileDAO.Upsert(defaultProfile); err != nil {
				log.Error("自动创建默认实例失败", logger.F("error", err))
			}
		}
	}

	// 迁移/初始化书签：clean self-use builds do not seed promotional/default bookmarks.
	if bookmarks, err := a.browserMgr.BookmarkDAO.List(); err == nil && len(bookmarks) == 0 {
		if len(a.config.Browser.DefaultBookmarks) > 0 {
			if err := a.browserMgr.BookmarkDAO.ReplaceAll(filterKnownDefaultBookmarks(a.config.Browser.DefaultBookmarks)); err != nil {
				log.Error("书签迁移失败", logger.F("error", err))
			}
		} else {
			log.Info("默认书签为空，跳过书签初始化")
		}
	}
}
