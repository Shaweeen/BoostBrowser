import { Suspense, lazy, useEffect, useState } from 'react'
import type { ComponentType } from 'react'
import { BrowserRouter as Router, Routes, Route, Navigate } from 'react-router-dom'
import { ThemeProvider } from './shared/theme'
import { Layout } from './shared/layout'
import { ToastContainer, Modal, Button, Loading } from './shared/components'
import { AlertCircle } from 'lucide-react'
import { useNotificationStore } from './store/notificationStore'
import { useBackupStore } from './store/backupStore'
import { ForceQuit as ForceQuitApp, GetStartupDataCompatibilityStatus, IsWindowSyncPanelMode, RecordLifecycleEvent, SaveNativeMainWindowBounds } from './wailsjs/go/main/App'
import { Environment, Quit, WindowGetPosition, WindowGetSize, WindowHide, WindowIsMaximised, WindowIsMinimised, WindowMinimise, WindowSetPosition, WindowSetSize } from './wailsjs/runtime/runtime'

function lazyNamed<TModule extends Record<string, ComponentType<any>>>(
  loader: () => Promise<TModule>,
  exportName: keyof TModule,
) {
  return lazy(async () => {
    const module = await loader()
    return {
      default: module[exportName] as ComponentType<any>,
    }
  })
}

const SettingsPage = lazyNamed(() => import('./modules/settings/SettingsPage'), 'SettingsPage')
const BrowserListPage = lazyNamed(() => import('./modules/browser/pages/BrowserListPage'), 'BrowserListPage')
const BrowserDetailPage = lazyNamed(() => import('./modules/browser/pages/BrowserDetailPage'), 'BrowserDetailPage')
const BrowserEditPage = lazyNamed(() => import('./modules/browser/pages/BrowserEditPage'), 'BrowserEditPage')
const BrowserCopyPage = lazyNamed(() => import('./modules/browser/pages/BrowserCopyPage'), 'BrowserCopyPage')
const BatchCreatePage = lazyNamed(() => import('./modules/browser/pages/BatchCreatePage'), 'BatchCreatePage')
const BrowserLogsPage = lazyNamed(() => import('./modules/browser/pages/BrowserLogsPage'), 'BrowserLogsPage')
const ProxyPoolPage = lazyNamed(() => import('./modules/browser/pages/ProxyPoolPage'), 'ProxyPoolPage')
const CoreManagementPage = lazyNamed(() => import('./modules/browser/pages/CoreManagementPage'), 'CoreManagementPage')
const BookmarkSettingsPage = lazyNamed(() => import('./modules/browser/pages/BookmarkSettingsPage'), 'BookmarkSettingsPage')
const LaunchApiDocsPage = lazyNamed(() => import('./modules/browser/pages/LaunchApiDocsPage'), 'LaunchApiDocsPage')
const TagManagementPage = lazyNamed(() => import('./modules/browser/pages/TagManagementPage'), 'TagManagementPage')

import { UpdateChecker } from './modules/updater/UpdateChecker'
const WindowSyncPage = lazyNamed(() => import('./modules/browser/pages/WindowSyncPage'), 'WindowSyncPage')
const ExtensionManagementPage = lazyNamed(() => import('./modules/browser/pages/ExtensionManagementPage'), 'ExtensionManagementPage')
const AutomationPage = lazyNamed(() => import('./modules/browser/pages/AutomationPage'), 'AutomationPage')
const UsageTutorialPage = lazyNamed(() => import('./modules/browser/pages/UsageTutorialPage'), 'UsageTutorialPage')
const QuickLaunchModal = lazyNamed(() => import('./modules/browser/components/QuickLaunchModal'), 'QuickLaunchModal')

const MAIN_WINDOW_BOUNDS_STORAGE_KEY = 'boost:main-window-bounds:v1'

type SavedMainWindowBounds = {
  x: number
  y: number
  width: number
  height: number
}

function readSavedMainWindowBounds(): SavedMainWindowBounds | null {
  try {
    const raw = localStorage.getItem(MAIN_WINDOW_BOUNDS_STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<SavedMainWindowBounds>
    if (
      typeof parsed?.x !== 'number' ||
      typeof parsed?.y !== 'number' ||
      typeof parsed?.width !== 'number' ||
      typeof parsed?.height !== 'number'
    ) {
      return null
    }
    if (parsed.width < 1200 || parsed.height < 700) {
      return null
    }
    return {
      x: Math.round(parsed.x),
      y: Math.round(parsed.y),
      width: Math.round(parsed.width),
      height: Math.round(parsed.height),
    }
  } catch {
    return null
  }
}

function writeSavedMainWindowBounds(bounds: SavedMainWindowBounds) {
  try {
    localStorage.setItem(MAIN_WINDOW_BOUNDS_STORAGE_KEY, JSON.stringify(bounds))
  } catch {
    // ignore write failures
  }
}

async function saveMainWindowBoundsSnapshot() {
  const [isMaximised, isMinimised] = await Promise.all([
    WindowIsMaximised(),
    WindowIsMinimised(),
  ])
  if (isMaximised || isMinimised) return false

  const [position, size] = await Promise.all([
    WindowGetPosition(),
    WindowGetSize(),
  ])
  if (!position || !size) return false
  if (size.w < 1200 || size.h < 700) return false

  const bounds = {
    x: Math.round(position.x),
    y: Math.round(position.y),
    width: Math.round(size.w),
    height: Math.round(size.h),
  }
  writeSavedMainWindowBounds(bounds)
  try {
    await SaveNativeMainWindowBounds(bounds)
  } catch {
    // ignore native snapshot failures
  }
  return true
}

function useExtensionIntegrityScanOnOpen() {
  useEffect(() => {
    let cancelled = false
    const run = async () => {
      try {
        const {
          scanExtensionIntegrityAll,
          dismissExtensionIntegrityNotice,
        } = await import('./modules/browser/api')
        const result = await scanExtensionIntegrityAll(false)
        if (cancelled || result?.alreadyScanned) return
        if (result?.message && (result.incomplete > 0 || result.repaired > 0)) {
          const { toast } = await import('./shared/components')
          if (result.incomplete > 0) {
            const incompleteIds = Array.isArray(result.incompleteIds) ? result.incompleteIds : []
            if (incompleteIds.length > 0) {
              // 用户确认“不再提醒”后持久化，重启/升级后不再重复弹出。
              toast.warningWithAction(result.message, '不再提醒', () => {
                void dismissExtensionIntegrityNotice(incompleteIds).then((ok) => {
                  if (ok) toast.success('已记住，不再提醒这些环境')
                })
              })
            } else {
              toast.warning(result.message, 6000)
            }
          } else {
            toast.success(result.message)
          }
        }
      } catch {
        // non-fatal
      }
    }
    const t = window.setTimeout(run, 1200)
    return () => {
      cancelled = true
      window.clearTimeout(t)
    }
  }, [])
}

function useWailsNotifications() {
  const addNotification = useNotificationStore((s) => s.addNotification)
  useExtensionIntegrityScanOnOpen()

  useEffect(() => {
    const runtime = (window as any).runtime
    if (!runtime?.EventsOn) return

    const offCrashed = runtime.EventsOn(
      'browser:instance:crashed',
      (data: { profileId: string; profileName: string; error: string }) => {
        addNotification({
          type: 'error',
          title: '环境异常退出',
          message: `「${data.profileName || data.profileId}」意外崩溃：${data.error}`,
        })
      }
    )

    const offBridgeFailed = runtime.EventsOn(
      'proxy:bridge:failed',
      (data: { profileId: string; profileName: string; error: string }) => {
        addNotification({
          type: 'error',
          title: '代理连接失败',
          message: `「${data.profileName || data.profileId}」代理桥接启动失败：${data.error}`,
        })
      }
    )

    const offBridgeDied = runtime.EventsOn(
      'proxy:bridge:died',
      (data: { key: string; error: string }) => {
        addNotification({
          type: 'warning',
          title: '连接池节点失效',
          message: `代理节点 ${data.key} 连接中断，相关环境可能无法访问网络`,
        })
      }
    )

    return () => {
      offCrashed?.()
      offBridgeFailed?.()
      offBridgeDied?.()
    }
  }, [addNotification])
}

function CloseConfirmModal() {
  const [open, setOpen] = useState(false)
  const [platform, setPlatform] = useState('windows')
  const [quittingAction, setQuittingAction] = useState<'app-only' | 'app-and-browser' | null>(null)
  const importInProgress = useBackupStore((s) => s.importInProgress)
  const importProgress = useBackupStore((s) => s.importProgress)
  const importMessage = useBackupStore((s) => s.importMessage)
  const supportsTray = platform === 'windows'
  const quitting = quittingAction !== null

  useEffect(() => {
    const runtime = (window as any).runtime
    if (!runtime?.EventsOn) return

    const off = runtime.EventsOn('app:request-close', () => {
      setQuittingAction(null)
      setOpen(true)
    })
    return () => {
      if (typeof off === 'function') off()
    }
  }, [])

  useEffect(() => {
    let cancelled = false

    Environment()
      .then((info) => {
        if (!cancelled && info?.platform) {
          setPlatform(info.platform)
        }
      })
      .catch(() => {})

    return () => {
      cancelled = true
    }
  }, [])

  const closeModal = () => {
    if (quitting) return
    setOpen(false)
  }

  const handleMinimize = async () => {
    if (quitting) return
    RecordLifecycleEvent('frontend-click', ['action=minimize-to-tray']).catch(() => {})
    try {
      await saveMainWindowBoundsSnapshot()
    } catch {}
    setOpen(false)
    if (supportsTray) {
      WindowHide()
      return
    }
    WindowMinimise()
  }

  const handleQuitAppOnly = async () => {
    setQuittingAction('app-only')
    try {
      await RecordLifecycleEvent('frontend-click', ['action=hide-to-tray'])
      try {
        await saveMainWindowBoundsSnapshot()
      } catch {}
      setOpen(false)
      if (supportsTray) {
        WindowHide()
        return
      }
      WindowMinimise()
    } catch (error) {
      console.error('Hide to tray failed', error)
      setQuittingAction(null)
    }
  }

  const handleQuitAppAndBrowsers = async () => {
    setQuittingAction('app-and-browser')
    try {
      await RecordLifecycleEvent('frontend-click', ['action=quit-app-and-browser'])
      try {
        await saveMainWindowBoundsSnapshot()
      } catch {}
      // ForceQuit closes tracked Chromium processes sequentially before the
      // Wails host exits. Never race it with an unconditional early Quit: with
      // many environments that used to terminate the manager first and leave
      // browser windows behind.
      await ForceQuitApp()
      return
    } catch (error) {
      console.error('ForceQuit failed, falling back to runtime.Quit()', error)
    }
    Quit()
  }

  return (
    <Modal
      open={open}
      onClose={closeModal}
      title={importInProgress ? '关闭应用确认' : undefined}
      width={importInProgress ? '360px' : '420px'}
      closable={!quitting}
    >
      <div className="flex flex-col items-center pt-2 pb-6 px-4">
        <div className={`w-12 h-12 rounded-full flex items-center justify-center mb-4 ${
          importInProgress ? 'bg-amber-50 text-amber-500' : 'bg-red-50 text-red-500'
        }`}>
          <AlertCircle className="w-6 h-6" />
        </div>
        {importInProgress && (
          <h3 className="text-lg font-medium text-[var(--color-text-primary)] mb-2">
            正在加载中，是否关闭？
          </h3>
        )}
        {importInProgress ? (
          <p className="text-sm text-[var(--color-text-secondary)] text-center mb-6">
            当前正在加载配置
            {importProgress > 0 ? `（${importProgress}%）` : ''}。
            <br />
            {importMessage || '强制关闭会中断本次加载，是否仍要关闭应用？'}
          </p>
        ) : (
          <p className="mb-6 text-sm text-center text-[var(--color-text-secondary)]">
            可隐藏主窗口到托盘，或连同浏览器一起关闭。
          </p>
        )}

        <div className={`w-full ${importInProgress ? 'flex gap-3' : 'flex flex-col gap-2'}`}>
          {importInProgress ? (
            <>
              <Button variant="secondary" className="flex-1" onClick={closeModal} disabled={quitting}>
                继续加载
              </Button>
              <Button
                variant="danger"
                className="flex-1"
                onClick={handleQuitAppAndBrowsers}
                loading={quittingAction === 'app-and-browser'}
              >
                仍要关闭
              </Button>
            </>
          ) : (
            <>
              <Button
                variant="secondary"
                className="w-full !bg-[#f3f4f6] !border-[#e5e7eb] !text-[var(--color-text-primary)] hover:!bg-[#e5e7eb]"
                onClick={supportsTray ? handleMinimize : closeModal}
                disabled={quitting}
              >
                {supportsTray ? '最小化到托盘' : '取消'}
              </Button>
              <Button
                className="w-full"
                onClick={handleQuitAppOnly}
                loading={quittingAction === 'app-only'}
                disabled={quitting}
              >
                隐藏到托盘（主程序保持运行）
              </Button>
              <Button
                variant="danger"
                className="w-full"
                onClick={handleQuitAppAndBrowsers}
                loading={quittingAction === 'app-and-browser'}
                disabled={quitting}
              >
                退出应用与浏览器
              </Button>
            </>
          )}
        </div>
      </div>
    </Modal>
  )
}

type StartupDataStatus = {
  activeDataPath?: string
  existingData?: boolean
  autoRecovered?: number
  recoveryCount?: number
  recoveryPath?: string
  message?: string
}

type LegacyDataAutoFolder = {
  folderKey: string
  folderName: string
  profileName: string
  sizeBytes: number
}

type LegacyDataAutoPreview = {
  folders: LegacyDataAutoFolder[]
  dismissed: number
  message: string
}

// LegacyDataAutoNotice: client self-identification of leftover Chrome data
// folders inside the active data root. Surfaced once at startup; the user can
// import a folder as an environment or dismiss it (dismissal is remembered in
// backend data/.boost_notice_dismissed.json and never repeats). No data is
// deleted — dismissed folders stay on disk untouched.
function LegacyDataAutoNotice() {
  const [preview, setPreview] = useState<LegacyDataAutoPreview | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    const run = async () => {
      try {
        const { scanLegacyDataAuto } = await import('./modules/browser/api')
        const result = await scanLegacyDataAuto()
        if (!cancelled && Array.isArray(result?.folders) && result.folders.length > 0) {
          setPreview(result)
          setSelected(new Set(result.folders.map((f) => f.folderKey)))
        }
      } catch {
        // non-fatal
      }
    }
    const t = window.setTimeout(run, 2500)
    return () => {
      cancelled = true
      window.clearTimeout(t)
    }
  }, [])

  const toggle = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const handleImport = async () => {
    if (busy || selected.size === 0) return
    setBusy(true)
    try {
      const { importLegacyDataFolders } = await import('./modules/browser/api')
      const result = await importLegacyDataFolders(Array.from(selected))
      const { toast } = await import('./shared/components')
      toast.success(result?.message || '旧数据已导入')
      setPreview(null)
    } finally {
      setBusy(false)
    }
  }

  const handleDismissAll = async () => {
    if (busy || !preview) return
    setBusy(true)
    try {
      const { dismissLegacyDataFolders } = await import('./modules/browser/api')
      const keys = preview.folders.map((f) => f.folderKey)
      const ok = await dismissLegacyDataFolders(keys)
      const { toast } = await import('./shared/components')
      if (ok) {
        toast.success('已记录忽略，这些旧数据不再提醒（文件保留在磁盘）')
        setPreview(null)
      }
    } finally {
      setBusy(false)
    }
  }

  if (!preview) return null

  return (
    <Modal
      open
      onClose={() => setPreview(null)}
      title="识别到未关联的浏览器数据"
      width="600px"
      footer={
        <div className="flex items-center gap-2 w-full">
          <Button variant="secondary" className="flex-1" onClick={handleDismissAll} disabled={busy}>
            全部忽略（不再提醒）
          </Button>
          <Button className="flex-1" onClick={handleImport} loading={busy} disabled={selected.size === 0}>
            导入勾选的环境
          </Button>
        </div>
      }
    >
      <div className="space-y-3 text-sm text-[var(--color-text-secondary)]">
        <p>{preview.message}</p>
        <div className="max-h-64 overflow-y-auto rounded-lg border border-[var(--color-border-default)] divide-y divide-[var(--color-border-default)]">
          {preview.folders.map((f) => (
            <label
              key={f.folderKey}
              className="flex items-center gap-3 px-3 py-2.5 cursor-pointer hover:bg-[var(--color-bg-secondary)] transition-colors"
            >
              <input
                type="checkbox"
                checked={selected.has(f.folderKey)}
                onChange={() => toggle(f.folderKey)}
                className="accent-[var(--color-accent)]"
              />
              <div className="flex-1 min-w-0">
                <p className="truncate font-medium text-[var(--color-text-primary)]">{f.folderName}</p>
                {f.profileName && f.profileName !== f.folderName && (
                  <p className="truncate text-xs text-[var(--color-text-muted)]">识别名：{f.profileName}</p>
                )}
              </div>
              <span className="flex-shrink-0 text-xs text-[var(--color-text-muted)]">
                {(f.sizeBytes / (1024 * 1024)).toFixed(1)} MB
              </span>
            </label>
          ))}
        </div>
        <p className="text-xs text-[var(--color-text-muted)]">
          导入为环境是原地挂载，不复制、不覆盖现有环境；勾选后 Cookies、扩展与钱包本地存储保持原样。忽略的文件不会被删除，之后可在设置页重新启用提醒。
        </p>
      </div>
    </Modal>
  )
}

function StartupDataCompatibilityNotice() {
  const [status, setStatus] = useState<StartupDataStatus | null>(null)

  useEffect(() => {
    let cancelled = false
    GetStartupDataCompatibilityStatus()
      .then((value) => {
        if (!cancelled && (Number(value?.autoRecovered || 0) > 0 || Number(value?.recoveryCount || 0) > 0)) setStatus(value)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  return (
    <Modal
      open={status !== null}
      onClose={() => setStatus(null)}
      title="已识别现有 data 数据"
      width="560px"
      footer={<Button onClick={() => setStatus(null)}>我知道了</Button>}
    >
      <div className="space-y-3 text-sm text-[var(--color-text-secondary)]">
        <div className="flex items-start gap-3 rounded-lg border border-[var(--color-border-default)] bg-[var(--color-bg-secondary)] p-3">
          <AlertCircle className="mt-0.5 h-5 w-5 flex-none text-[var(--color-accent)]" />
          <div>
            <p className="font-medium text-[var(--color-text-primary)]">{status?.message}</p>
            <p className="mt-1">自动恢复环境：{status?.autoRecovered || 0} 个</p>
            {(status?.recoveryCount || 0) > 0 && (
              <>
                <p className="mt-1">等待用户确认恢复：{status?.recoveryCount || 0} 个</p>
                <p className="mt-1 break-all text-xs text-[var(--color-text-muted)]">恢复归档：{status?.recoveryPath}</p>
              </>
            )}
            <p className="mt-1 break-all text-xs text-[var(--color-text-muted)]">数据目录：{status?.activeDataPath}</p>
          </div>
        </div>
        <p className="text-xs text-[var(--color-text-muted)]">
          客户端只升级程序和最新数据读取组件，不改写环境中的 Cookies、浏览器文件、扩展与钱包本地存储。钱包扩展升级请保持相同的官方扩展 ID；扩展自身会负责其存储格式迁移。
        </p>
      </div>
    </Modal>
  )
}

function App() {
  useWailsNotifications()
  const [quickLaunchOpen, setQuickLaunchOpen] = useState(false)
  const [syncPanelMode, setSyncPanelMode] = useState(false)
  const [panelModeLoaded, setPanelModeLoaded] = useState(false)
  const routeFallback = (
    <div className="flex min-h-[240px] items-center justify-center py-10">
      <Loading text="页面加载中..." />
    </div>
  )

  useEffect(() => {
    let cancelled = false
    IsWindowSyncPanelMode()
      .then((enabled) => {
        if (!cancelled) {
          setSyncPanelMode(enabled === true)
          setPanelModeLoaded(true)
        }
      })
      .catch(() => {
        if (!cancelled) {
          setPanelModeLoaded(true)
        }
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing) return
      if (!(event.ctrlKey || event.metaKey)) return
      if (event.key.toLowerCase() !== 'k') return
      event.preventDefault()
      setQuickLaunchOpen((prev) => !prev)
    }

    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [])

  useEffect(() => {
    document.body.classList.toggle('sync-panel-mode', syncPanelMode)
    return () => {
      document.body.classList.remove('sync-panel-mode')
    }
  }, [syncPanelMode])

  useEffect(() => {
    if (!panelModeLoaded || syncPanelMode) return

    let cancelled = false

    const restoreBounds = async () => {
      const saved = readSavedMainWindowBounds()
      if (!saved) return
      try {
        await WindowSetSize(saved.width, saved.height)
        await WindowSetPosition(saved.x, saved.y)
        await new Promise((resolve) => window.setTimeout(resolve, 150))
      } catch {
        // ignore restore failures
      }
    }

    const persistBounds = async () => {
      try {
        if (cancelled) return
        await saveMainWindowBoundsSnapshot()
      } catch {
        // ignore snapshot failures
      }
    }

    void restoreBounds().then(() => {
      void persistBounds()
    })

    // Action-driven bounds persistence: no 1.5s polling. Save on hide, unload,
    // and debounced resize — enough for restore without a permanent timer.
    let resizeTimer: ReturnType<typeof window.setTimeout> | null = null
    const handleBeforeUnload = () => {
      void persistBounds()
    }
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        void persistBounds()
      }
    }
    const handleResize = () => {
      if (resizeTimer) window.clearTimeout(resizeTimer)
      resizeTimer = window.setTimeout(() => {
        void persistBounds()
      }, 400)
    }

    window.addEventListener('beforeunload', handleBeforeUnload)
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('resize', handleResize)

    return () => {
      cancelled = true
      if (resizeTimer) window.clearTimeout(resizeTimer)
      window.removeEventListener('beforeunload', handleBeforeUnload)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      window.removeEventListener('resize', handleResize)
    }
  }, [panelModeLoaded, syncPanelMode])

  if (!panelModeLoaded) {
    return (
      <ThemeProvider>
        <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg-base)]">
          <Loading text="同步器窗口加载中..." />
        </div>
      </ThemeProvider>
    )
  }

  return (
    <ThemeProvider>
      <Router>
        <Layout syncPanelMode={syncPanelMode}>
          <Suspense fallback={routeFallback}>
            <Routes>
              {syncPanelMode ? (
                <>
                  <Route path="/" element={<Navigate to="/browser/sync" replace />} />
                  <Route path="/browser/sync" element={<WindowSyncPage />} />
                  <Route path="*" element={<Navigate to="/browser/sync" replace />} />
                </>
              ) : (
                <>
                  <Route path="/" element={<Navigate to="/browser/list" replace />} />
                  {/* Scaffold leftovers: keep deep-link redirects, no primary nav. */}
                  <Route path="/charts" element={<Navigate to="/browser/list" replace />} />
                  <Route path="/profile" element={<Navigate to="/settings" replace />} />
                  <Route path="/settings" element={<SettingsPage />} />

                  <Route path="/browser/list" element={<BrowserListPage />} />
                  <Route path="/browser/detail/:id" element={<BrowserDetailPage />} />
                  <Route path="/browser/edit/:id" element={<BrowserEditPage />} />
                  <Route path="/browser/copy/:id" element={<BrowserCopyPage />} />
                  <Route path="/browser/batch-create" element={<BatchCreatePage />} />
                  <Route path="/browser/monitor" element={<Navigate to="/browser/list" replace />} />
                  <Route path="/browser/logs" element={<BrowserLogsPage />} />
                  <Route path="/browser/proxy-pool" element={<ProxyPoolPage />} />
                  <Route path="/browser/cores" element={<CoreManagementPage />} />
                  <Route path="/browser/bookmarks" element={<BookmarkSettingsPage />} />
                  <Route path="/browser/automation" element={<AutomationPage />} />
                  <Route path="/browser/launch-api" element={<LaunchApiDocsPage />} />
                  <Route path="/browser/tags" element={<TagManagementPage />} />
                  <Route path="/browser/sync" element={<Navigate to="/browser/list" replace />} />
                  <Route path="/browser/extensions" element={<ExtensionManagementPage />} />
                  <Route path="/system/tutorial" element={<UsageTutorialPage />} />
                </>
              )}
            </Routes>
          </Suspense>
        </Layout>
        <ToastContainer />
        {!syncPanelMode && <CloseConfirmModal />}
        {!syncPanelMode && <StartupDataCompatibilityNotice />}
        {!syncPanelMode && <LegacyDataAutoNotice />}
        {!syncPanelMode && <UpdateChecker />}
        {!syncPanelMode && (
          <Suspense fallback={null}>
            {quickLaunchOpen ? (
              <QuickLaunchModal open={quickLaunchOpen} onClose={() => setQuickLaunchOpen(false)} />
            ) : null}
          </Suspense>
        )}
      </Router>
    </ThemeProvider>
  )
}

export default App
