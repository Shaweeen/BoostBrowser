import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  CheckSquare,
  ChevronDown,
  Columns,
  Grip,
  Info,
  LayoutGrid,
  Monitor,
  Minimize2,
  Move,
  RefreshCw,
  Rows,
  Square,
  X,
} from 'lucide-react'
import { Button, Input, Select, toast } from '../../../shared/components'
import { ExitWindowSyncPanel, IsWindowSyncPanelMode } from '../../../wailsjs/go/main/App'
import { EventsOn, ScreenGetAll, WindowCenter, WindowGetPosition, WindowSetAlwaysOnTop, WindowSetMinSize, WindowSetPosition, WindowSetSize, WindowShow, WindowUnminimise } from '../../../wailsjs/runtime/runtime'
import {
  getSyncProfiles,
  getSyncStatus,
  startInputSync,
  stopInputSync,
  syncTileWindows,
  type SyncProfileInfo,
  type SyncStatus,
  type TileLayoutMode,
  updateSyncRandomDelay,
} from '../api_sync'

function compareProfileName(a: SyncProfileInfo, b: SyncProfileInfo) {
  const aName = (a.profileName || a.profileId || '').trim()
  const bName = (b.profileName || b.profileId || '').trim()
  return aName.localeCompare(bName, 'zh-Hans-CN', { numeric: true, sensitivity: 'base' })
}

type FilterMode = 'all' | 'selected' | 'master' | 'followers'
type ToolbarMenu = 'layout' | null

const FILTER_OPTIONS: Array<{ value: FilterMode; label: string }> = [
  { value: 'all', label: '全部实例' },
  { value: 'selected', label: '仅看已选' },
  { value: 'master', label: '仅看主控' },
  { value: 'followers', label: '仅看跟随' },
]

const LAYOUT_OPTIONS: Array<{ value: TileLayoutMode; label: string }> = [
  { value: 'grid', label: '平铺' },
  { value: 'vertical', label: '堆叠' },
  { value: 'horizontal', label: '横向排列' },
]

const PANEL_EXPANDED_SIZE = { width: 720, height: 580, minWidth: 680, minHeight: 520 }
const PANEL_COMPACT_STATUS_SIZE = { width: 400, height: 260, minWidth: 360, minHeight: 80 }
const PANEL_COMPACT_STATUS_COLLAPSED_SIZE = { width: 400, height: 104, minWidth: 360, minHeight: 80 }
const PANEL_COMPACT_FUNCTION_SIZE = { width: 440, height: 108, minWidth: 440, minHeight: 108 }
const PANEL_TOP_MARGIN_PX = 8
const PANEL_COMPACT_EDGE_PADDING_PX = 0
const PANEL_MINI_SIZE = { width: 136, height: 44, minWidth: 136, minHeight: 44 }

export function WindowSyncPage() {
  const compactPanelRef = useRef<HTMLDivElement | null>(null)
  const compactPanelLeaveTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null)
  const syncPanelWindowBootstrappedRef = useRef(false)
  const autoCollapseTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null)
  const panelStartedAtRef = useRef(Date.now())
  const runningSeenRef = useRef(false)
  const emptyRefreshCountRef = useRef(0)
  const bridgeFailureCountRef = useRef(0)
  const [syncPanelMode, setSyncPanelMode] = useState(false)
  const [panelPresentation, setPanelPresentation] = useState<'minimized' | 'compact' | 'full'>('full')
  const [showSyncControls, setShowSyncControls] = useState(false)
  const [profiles, setProfiles] = useState<SyncProfileInfo[]>([])
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set())
  const [masterId, setMasterId] = useState<string | null>(null)
  const [syncStatus, setSyncStatus] = useState<SyncStatus | null>(null)
  const [starting, setStarting] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [tileLayout, setTileLayout] = useState<TileLayoutMode>('grid')
  const [filterMode, setFilterMode] = useState<FilterMode>('all')
  const [filterOpen, setFilterOpen] = useState(false)
  const [toolbarMenu, setToolbarMenu] = useState<ToolbarMenu>(null)
  const [customCols, setCustomCols] = useState('2')
  const [customRows, setCustomRows] = useState('1')
  const [displayLabel, setDisplayLabel] = useState('当前显示器')
  const [, setPanelFocused] = useState(false)
  const [, setPanelHovered] = useState(false)
  const [randomDelayEnabled, setRandomDelayEnabled] = useState(false)
  const [randomDelayMinMs, setRandomDelayMinMs] = useState('50')
  const [randomDelayMaxMs, setRandomDelayMaxMs] = useState('200')

  const refreshTimer = useRef<ReturnType<typeof setInterval>>()
  const loadProfilesSeq = useRef(0)
  const pendingManualRefreshes = useRef(0)
  const loadProfiles = useCallback(async (options?: { silent?: boolean }) => {
    const silent = options?.silent === true
    const seq = ++loadProfilesSeq.current
    if (!silent) {
      pendingManualRefreshes.current += 1
      setRefreshing(true)
    }

    try {
      const [list, status] = await Promise.all([getSyncProfiles(), getSyncStatus()])
      if (seq !== loadProfilesSeq.current) return

      const sorted = [...list].sort(compareProfileName)
      const bridgeError = (status as (SyncStatus & { bridgeError?: string }) | null)?.bridgeError
      if (bridgeError || !status) {
        bridgeFailureCountRef.current += 1
        if (syncPanelMode && bridgeFailureCountRef.current >= 3) {
          void ExitWindowSyncPanel().catch(() => {})
          return
        }
      } else {
        bridgeFailureCountRef.current = 0
        const runningCount = sorted.filter(item => item.status === 'running').length
        if (runningCount > 0) {
          runningSeenRef.current = true
          emptyRefreshCountRef.current = 0
        } else if (syncPanelMode && Date.now() - panelStartedAtRef.current > 7000) {
          emptyRefreshCountRef.current += 1
          if (runningSeenRef.current || emptyRefreshCountRef.current >= 3) {
            void stopInputSync().finally(() => ExitWindowSyncPanel().catch(() => {}))
            return
          }
        }
      }
      setProfiles(sorted)
      setSyncStatus(status)
      if (status) {
        setRandomDelayEnabled(status.randomDelayEnabled === true)
        setRandomDelayMinMs(String(status.randomDelayMinMs || 50))
        setRandomDelayMaxMs(String(status.randomDelayMaxMs || 200))
      }

      if (status?.active) {
        const nextSelected = new Set([status.masterId, ...(status.followerIds || [])].filter(Boolean))
        setSelectedIds(nextSelected)
        setMasterId(status.masterId)
        return
      }

      const runningIds = new Set(sorted.filter(item => item.status === 'running').map(item => item.profileId))
      setSelectedIds(prev => {
        const next = new Set<string>()
        prev.forEach(id => {
          if (runningIds.has(id)) next.add(id)
        })
        return next
      })
      setMasterId(prev => (prev && runningIds.has(prev) ? prev : null))
    } finally {
      if (!silent) {
        pendingManualRefreshes.current = Math.max(0, pendingManualRefreshes.current - 1)
        if (pendingManualRefreshes.current === 0) {
          setRefreshing(false)
        }
      }
    }
  }, [syncPanelMode])

  useEffect(() => {
    let cancelled = false
    IsWindowSyncPanelMode()
      .then((enabled) => {
        if (!cancelled) setSyncPanelMode(enabled === true)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    void loadProfiles({ silent: true })
    refreshTimer.current = setInterval(() => {
      void loadProfiles({ silent: true })
    }, 2000)

    const handleWindowFocus = () => {
      void loadProfiles({ silent: true })
    }
    const handleVisibilityChange = () => {
      if (!document.hidden) {
        void loadProfiles({ silent: true })
      }
    }

    const offStarted = EventsOn('browser:instance:started', () => {
      void loadProfiles({ silent: true })
    })
    const offStopped = EventsOn('browser:instance:stopped', () => {
      void loadProfiles({ silent: true })
    })
    const offUpdated = EventsOn('browser:instance:updated', () => {
      void loadProfiles({ silent: true })
    })

    window.addEventListener('focus', handleWindowFocus)
    document.addEventListener('visibilitychange', handleVisibilityChange)

    return () => {
      if (refreshTimer.current) clearInterval(refreshTimer.current)
      window.removeEventListener('focus', handleWindowFocus)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      offStarted?.()
      offStopped?.()
      offUpdated?.()
    }
  }, [loadProfiles])

  useEffect(() => {
    const screen = window.screen
    if (!screen) return
    const width = screen.availWidth || screen.width
    const height = screen.availHeight || screen.height
    setDisplayLabel(`当前显示器（${width}x${height}）`)
  }, [])

  const displayProfiles = useMemo(() => [...profiles].sort(compareProfileName), [profiles])
  const isSyncing = syncStatus?.active === true
  const activeSyncIds = useMemo(
    () => [syncStatus?.masterId, ...(syncStatus?.followerIds || [])].filter(Boolean) as string[],
    [syncStatus?.followerIds, syncStatus?.masterId],
  )
  const activeSyncCount = activeSyncIds.length
  const followerCount = Math.max(0, activeSyncCount - (syncStatus?.masterId ? 1 : 0))
  const selectedCount = selectedIds.size
  const selectedLabel = selectedCount > 0 ? `已选 ${selectedCount} 项` : '未选择环境'
  const masterProfile = displayProfiles.find(item => item.profileId === (syncStatus?.masterId || masterId)) || null
  const followerProfiles = displayProfiles.filter(item => (syncStatus?.followerIds || []).includes(item.profileId))
  const statusLayoutLabel = tileLayout === 'vertical' ? '堆叠' : tileLayout === 'horizontal' ? '横排' : '平铺'
  const compactRunningMode = isSyncing && !syncPanelMode
  const compactSyncStatusMode = syncPanelMode && isSyncing && panelPresentation === 'compact'
  const compactFunctionPanelMode = syncPanelMode && !isSyncing && panelPresentation === 'compact'
  const minimizedPanelMode = syncPanelMode && panelPresentation === 'minimized'
  const compactPanelInteractive = syncPanelMode && (compactSyncStatusMode || compactFunctionPanelMode)
  const syncControlsVisible = compactSyncStatusMode ? true : showSyncControls

  const handleCompactPanelMouseEnter = () => {
    if (compactPanelLeaveTimerRef.current) {
      window.clearTimeout(compactPanelLeaveTimerRef.current)
      compactPanelLeaveTimerRef.current = null
    }
    setPanelHovered(true)
  }

  const handleCompactPanelMouseLeave = () => {
    if (compactPanelLeaveTimerRef.current) {
      window.clearTimeout(compactPanelLeaveTimerRef.current)
    }
    compactPanelLeaveTimerRef.current = window.setTimeout(() => {
      setPanelHovered(false)
      compactPanelLeaveTimerRef.current = null
    }, 180)
  }

  const visibleProfiles = useMemo(() => {
    switch (filterMode) {
      case 'selected':
        return displayProfiles.filter(item => selectedIds.has(item.profileId))
      case 'master':
        return displayProfiles.filter(item => item.profileId === masterId)
      case 'followers':
        if (isSyncing) {
          return displayProfiles.filter(item => syncStatus?.followerIds?.includes(item.profileId))
        }
        return displayProfiles.filter(item => selectedIds.has(item.profileId) && item.profileId !== masterId)
      default:
	return displayProfiles
    }
  }, [displayProfiles, filterMode, isSyncing, masterId, selectedIds, syncStatus?.followerIds])

  useEffect(() => {
    if (!isSyncing) {
      setShowSyncControls(false)
    }
  }, [isSyncing])

  useEffect(() => {
    if (!compactPanelInteractive) {
      if (compactPanelLeaveTimerRef.current) {
        window.clearTimeout(compactPanelLeaveTimerRef.current)
        compactPanelLeaveTimerRef.current = null
      }
      setPanelFocused(false)
      setPanelHovered(false)
      return
    }

    const handleFocus = () => setPanelFocused(true)
    const handleBlur = () => setPanelFocused(false)

    setPanelFocused(document.hasFocus())
    window.addEventListener('focus', handleFocus)
    window.addEventListener('blur', handleBlur)
    return () => {
      if (compactPanelLeaveTimerRef.current) {
        window.clearTimeout(compactPanelLeaveTimerRef.current)
        compactPanelLeaveTimerRef.current = null
      }
      window.removeEventListener('focus', handleFocus)
      window.removeEventListener('blur', handleBlur)
    }
  }, [compactPanelInteractive])

  useEffect(() => {
    if (!syncPanelMode) return

    const applyWindowMode = async () => {
      const target = compactSyncStatusMode
          ? (syncControlsVisible ? PANEL_COMPACT_STATUS_SIZE : PANEL_COMPACT_STATUS_COLLAPSED_SIZE)
          : minimizedPanelMode
            ? PANEL_MINI_SIZE
          : compactFunctionPanelMode
            ? PANEL_COMPACT_FUNCTION_SIZE
            : PANEL_EXPANDED_SIZE
      // The sync tool is a dedicated control surface. Keep every presentation
      // above the main client and browser windows so expanding it cannot look
      // like the panel disappeared behind another window.
      const shouldPinTop = true
      WindowSetAlwaysOnTop(shouldPinTop)
      WindowSetMinSize(target.minWidth, target.minHeight)
      WindowSetSize(target.width, target.height)
      const isFirstShow = !syncPanelWindowBootstrappedRef.current
      if (isFirstShow) {
        WindowShow()
        WindowUnminimise()
        syncPanelWindowBootstrappedRef.current = true
      }
      // Preserve the dragged position, but clamp the expanded panel into the
      // current display so restoring a logo near an edge never goes off-screen.
      if (shouldPinTop) {
        try {
          const screens = await ScreenGetAll()
          const current = screens.find(screen => screen.isCurrent) || screens.find(screen => screen.isPrimary) || screens[0]
          if (current) {
            const currentX = typeof (current as unknown as { x?: number }).x === 'number' ? (current as unknown as { x: number }).x : 0
            const currentY = typeof (current as unknown as { y?: number }).y === 'number' ? (current as unknown as { y: number }).y : 0
            const position = isFirstShow ? null : await WindowGetPosition()
            const desiredX = position?.x ?? Math.round(currentX + (current.width - target.width) / 2)
            const desiredY = position?.y ?? Math.round(currentY + PANEL_TOP_MARGIN_PX)
            const x = Math.max(currentX, Math.min(desiredX, currentX + current.width - target.width))
            const y = Math.max(currentY, Math.min(desiredY, currentY + current.height - target.height))
            WindowSetPosition(x, y)
            return
          }
        } catch {
        }
      }
      WindowCenter()
    }

    const timer = window.setTimeout(() => {
      void applyWindowMode()
    }, compactSyncStatusMode ? 40 : 0)
    return () => window.clearTimeout(timer)
  }, [compactFunctionPanelMode, compactSyncStatusMode, minimizedPanelMode, syncControlsVisible, syncPanelMode])

  useEffect(() => {
    if (!syncPanelMode || minimizedPanelMode || !isSyncing) {
      if (autoCollapseTimerRef.current) window.clearTimeout(autoCollapseTimerRef.current)
      autoCollapseTimerRef.current = null
      return
    }
    const resetAutoCollapse = () => {
      if (autoCollapseTimerRef.current) window.clearTimeout(autoCollapseTimerRef.current)
      autoCollapseTimerRef.current = window.setTimeout(() => {
        setShowSyncControls(false)
        setToolbarMenu(null)
        setPanelPresentation('minimized')
      }, 3000)
    }
    const events: Array<keyof WindowEventMap> = ['pointerdown', 'pointermove', 'keydown', 'wheel', 'input']
    events.forEach(event => window.addEventListener(event, resetAutoCollapse, { passive: true }))
    resetAutoCollapse()
    return () => {
      if (autoCollapseTimerRef.current) window.clearTimeout(autoCollapseTimerRef.current)
      autoCollapseTimerRef.current = null
      events.forEach(event => window.removeEventListener(event, resetAutoCollapse))
    }
  }, [isSyncing, minimizedPanelMode, syncPanelMode])

  useEffect(() => {
    const compactClass = 'sync-panel-compact'
    if (syncPanelMode && (compactSyncStatusMode || compactFunctionPanelMode || minimizedPanelMode)) {
      document.body.classList.add(compactClass)
      return () => {
        document.body.classList.remove(compactClass)
      }
    }

    document.body.classList.remove(compactClass)
    return () => {
      document.body.classList.remove(compactClass)
    }
  }, [compactFunctionPanelMode, compactSyncStatusMode, minimizedPanelMode, syncPanelMode])

  useEffect(() => {
    if (!syncPanelMode) {
      setPanelPresentation('full')
      return
    }
    setPanelPresentation('compact')
  }, [syncPanelMode])

  useLayoutEffect(() => {
    if (!syncPanelMode || (!compactSyncStatusMode && !compactFunctionPanelMode)) return

    const node = compactPanelRef.current
    if (!node) return

    const sizeFloor = compactSyncStatusMode
      ? PANEL_COMPACT_STATUS_COLLAPSED_SIZE
      : PANEL_COMPACT_FUNCTION_SIZE

    let frame = 0

    const syncWindowToContent = () => {
      if (!compactPanelRef.current) return
      const rect = compactPanelRef.current.getBoundingClientRect()
      const width = Math.max(sizeFloor.minWidth, Math.ceil(rect.width) + PANEL_COMPACT_EDGE_PADDING_PX)
      const height = Math.max(sizeFloor.minHeight, Math.ceil(rect.height) + PANEL_COMPACT_EDGE_PADDING_PX)
      WindowSetMinSize(width, height)
      WindowSetSize(width, height)
    }

    const scheduleSync = () => {
      if (frame) window.cancelAnimationFrame(frame)
      frame = window.requestAnimationFrame(syncWindowToContent)
    }

    scheduleSync()

    const observer = new ResizeObserver(() => {
      scheduleSync()
    })
    observer.observe(node)

    return () => {
      observer.disconnect()
      if (frame) window.cancelAnimationFrame(frame)
    }
  }, [compactFunctionPanelMode, compactSyncStatusMode, syncControlsVisible, syncPanelMode, activeSyncCount, followerCount, statusLayoutLabel, masterProfile?.profileId, masterProfile?.profileName, syncStatus?.mouseEnabled, syncStatus?.keyEnabled, displayProfiles.length, selectedCount, masterId])

  const toggleSelect = (id: string) => {
    if (isSyncing) return
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
        if (masterId === id) setMasterId(null)
      } else {
        next.add(id)
      }
      return next
    })
  }

  const setAsMaster = (id: string) => {
    if (isSyncing) return
    setMasterId(id)
    setSelectedIds(prev => new Set([...prev, id]))
  }

  const handleSelectAllVisible = () => {
    if (isSyncing) return
    const visibleIds = visibleProfiles.filter(item => item.status === 'running').map(item => item.profileId)
    const allSelected = visibleIds.length > 0 && visibleIds.every(id => selectedIds.has(id))
    if (allSelected) {
      const next = new Set(selectedIds)
      visibleIds.forEach(id => next.delete(id))
      if (masterId && !next.has(masterId)) {
        setMasterId(null)
      }
      setSelectedIds(next)
      return
    }
    setSelectedIds(prev => new Set([...prev, ...visibleIds]))
  }

  const handleStartSync = async () => {
    if (!masterId) {
      toast.error('请先指定主控环境')
      return
    }
    const followers = Array.from(selectedIds).filter(id => id !== masterId)
    if (followers.length === 0) {
      toast.error('请至少再选 1 个跟随环境')
      return
    }
    setStarting(true)
    const err = await startInputSync(masterId, followers)
    setStarting(false)
    if (err) {
      toast.error(`启动同步失败：${err}`)
      return
    }
    setPanelPresentation('compact')
    setShowSyncControls(false)
    await loadProfiles()
  }

  const handleStopSync = async () => {
    const err = await stopInputSync()
    if (err) {
      toast.error(`停止同步失败：${err}`)
      return
    }
    setShowSyncControls(false)
    setPanelPresentation('compact')
    setToolbarMenu(null)
    await loadProfiles()
  }

  const handleRandomDelayChange = async (enabled: boolean) => {
    if (!isSyncing) return
    const minMs = Math.max(0, Number(randomDelayMinMs) || 0)
    const maxMs = Math.max(minMs, Number(randomDelayMaxMs) || minMs)
    const err = await updateSyncRandomDelay(enabled, minMs, maxMs)
    if (err) {
      toast.error(`更新随机延时失败：${err}`)
      return
    }
    setRandomDelayEnabled(enabled)
    setSyncStatus(prev => prev ? { ...prev, randomDelayEnabled: enabled, randomDelayMinMs: minMs, randomDelayMaxMs: maxMs } : prev)
  }

  const handleTile = async (layout: TileLayoutMode = tileLayout, _toastLabel?: string) => {
    const ids = isSyncing ? activeSyncIds : Array.from(selectedIds)
    if (ids.length === 0) {
      toast.error('请先选择要排列的环境')
      return
    }
    const result = await syncTileWindows(ids, masterId || undefined, layout)
    if (!result) {
      toast.error('窗口排列失败')
      return
    }
    setTileLayout(result.layout)
  }

  const handleApplyCustomLayout = async () => {
    const cols = Number(customCols)
    const rows = Number(customRows)
    if (!Number.isFinite(cols) || !Number.isFinite(rows) || cols <= 0 || rows <= 0) {
      toast.error('自定义排列的行列数必须大于 0')
      return
    }
    const nextLayout: TileLayoutMode = rows === 1 ? 'horizontal' : cols === 1 ? 'vertical' : 'grid'
    await handleTile(nextLayout, `按 ${cols}×${rows} 自定义排列`)
  }

  const handleClosePanel = () => {
    if (!syncPanelMode) return
    setShowSyncControls(false)
    setPanelPresentation('minimized')
  }

  const handleExitAssistant = async () => {
    if (isSyncing) await stopInputSync()
    await ExitWindowSyncPanel().catch(() => {})
  }

  const handleOpenFullPanel = () => {
    if (!syncPanelMode) return
    setShowSyncControls(true)
    setPanelPresentation('full')
  }

  if (minimizedPanelMode) {
    return (
      <div className="relative flex h-11 w-[136px] items-center overflow-hidden bg-[#f8fafc] px-1.5 shadow-[0_8px_22px_rgba(30,58,110,.18)]">
        <div className="flex h-9 w-9 shrink-0 cursor-move items-center justify-center" title="拖动同步工具" style={{ ['--wails-draggable' as any]: 'drag' }}>
          <span className="relative flex h-8 w-8 items-center justify-center rounded-[9px] bg-[#17263d] text-[14px] font-black text-white">
            B
            <span className={`absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full border-2 border-[#f7f9fc] ${isSyncing ? 'bg-[#22c55e]' : 'bg-[#f59e0b]'}`} />
          </span>
        </div>
        <button
          type="button"
          className="ml-1 flex h-9 min-w-0 flex-1 items-center rounded-[9px] px-1.5 text-left text-[#17263d] transition hover:bg-[#eaf0f8]"
          onClick={() => setPanelPresentation('compact')}
          title="展开窗口同步助手"
          aria-label="展开窗口同步助手"
          style={{ ['--wails-draggable' as any]: 'no-drag' }}
        >
          <span className="min-w-0 flex-1">
            <span className="block text-[11px] font-semibold leading-4">同步工具</span>
            <span className="block truncate text-[8px] leading-3 text-[#738199]">{isSyncing ? `${activeSyncCount} 个同步中` : '点击展开'}</span>
          </span>
        </button>
      </div>
    )
  }

  const visibleSelectableProfiles = visibleProfiles.filter(item => item.status === 'running')
  const compactProfiles = displayProfiles
  const compactSelectableProfiles = compactProfiles.filter(item => item.status === 'running')
  const allVisibleSelected = visibleSelectableProfiles.length > 0 && visibleSelectableProfiles.every(item => selectedIds.has(item.profileId))


  if (compactFunctionPanelMode) {
    return (
      <div className="inline-block overflow-visible bg-transparent px-0 pt-0 text-white">
        <div
          ref={compactPanelRef}
          className="w-[440px] border border-[#dbe5f3] bg-[#eff5ff] px-3 py-3 text-[#111827] shadow-[0_14px_32px_rgba(35,68,135,0.14)] transition-all duration-200"
          onMouseEnter={handleCompactPanelMouseEnter}
          onMouseLeave={handleCompactPanelMouseLeave}
        >
          <div className="flex min-h-[48px] items-center gap-3" style={{ ['--wails-draggable' as any]: 'drag' }}>
            <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-[#dce8ff] text-[#3a6be0] shadow-[inset_0_0_0_1px_rgba(58,107,224,0.08)]">
              <Monitor className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="whitespace-normal break-words text-[16px] font-semibold leading-5 text-[#111827]">同步工具</div>
              <div className="mt-1 text-[12px] text-[#667085]">运行中 {compactSelectableProfiles.length} · 共 {compactProfiles.length} · 已选 {selectedCount} · 主控 {displayProfiles.find(item => item.profileId === masterId)?.profileName || displayProfiles.find(item => item.profileId === masterId)?.profileId || '未设置'}</div>
            </div>
            <button
              type="button"
              className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-full border border-[#c8d0dc] bg-white/80 text-[#344054]"
              onClick={() => setPanelPresentation('minimized')}
              title="最小化为悬浮图标"
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              <Minimize2 className="h-4 w-4" />
            </button>
            <button
              type="button"
              className="inline-flex h-9 items-center justify-center self-center rounded-full border border-[#c8d0dc] bg-white px-3 text-sm font-medium text-[#344054] shadow-[0_8px_18px_rgba(16,24,40,0.08)] transition hover:bg-[#eef2f7] hover:text-[#111827]"
              onClick={() => void loadProfiles()}
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              <RefreshCw className="mr-1.5 h-4 w-4" />刷新
            </button>
            <button
              type="button"
              className="inline-flex h-9 w-9 shrink-0 items-center justify-center self-center rounded-full border border-[#c8d0dc] bg-white text-[#344054] shadow-[0_8px_18px_rgba(16,24,40,0.12)] transition hover:bg-[#eef2f7] hover:text-[#111827]"
              onClick={handleClosePanel}
              title="关闭同步窗口"
              aria-label="关闭同步窗口"
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <div className="mt-3 grid grid-cols-3 gap-2">
            <button
              type="button"
              className="inline-flex h-10 items-center justify-center gap-1.5 rounded-2xl bg-[#1c212b] px-3 text-sm font-medium text-white hover:bg-[#262c37]"
              onClick={() => void handleTile('grid', '平铺')}
              disabled={selectedIds.size === 0}
            >
              <LayoutGrid className="h-4 w-4" />平铺
            </button>
            <button
              type="button"
              className="inline-flex h-10 items-center justify-center gap-1.5 rounded-2xl bg-[#1c212b] px-3 text-sm font-medium text-white hover:bg-[#262c37]"
              onClick={() => void handleTile('vertical', '堆叠')}
              disabled={selectedIds.size === 0}
            >
              <Rows className="h-4 w-4" />堆叠
            </button>
            <button
              type="button"
              className="inline-flex h-10 items-center justify-center gap-1.5 rounded-2xl bg-[#1c212b] px-3 text-sm font-medium text-white hover:bg-[#262c37]"
              onClick={() => void handleTile('horizontal', '横排')}
              disabled={selectedIds.size === 0}
            >
              <Columns className="h-4 w-4" />横排
            </button>
          </div>

          <div className="mt-3 rounded-[20px] border border-white/12 bg-white/92 p-3 shadow-[0_8px_24px_rgba(15,23,42,0.08)]">
            <div className="flex items-center justify-between gap-3">
              <div>
                <div className="text-sm font-semibold text-[#111827]">选择要同步的环境</div>
                <div className="mt-1 text-[12px] text-[#667085]">勾选后可直接设主控，停止同步后会自动回到这里。</div>
              </div>
              <button
                type="button"
                className="inline-flex items-center gap-2 rounded-xl border border-[#d8dee8] bg-[#f8fafc] px-3 py-2 text-sm font-medium text-[#344054] transition hover:bg-[#eef2f7]"
                onClick={handleSelectAllVisible}
                disabled={compactSelectableProfiles.length === 0}
              >
                {compactSelectableProfiles.length > 0 && compactSelectableProfiles.every(item => selectedIds.has(item.profileId))
                  ? <CheckSquare className="h-4 w-4 text-[#3a6be0]" />
                  : <Square className="h-4 w-4 text-[#98a2b3]" />}
                全选
              </button>
            </div>

            <div className="mt-3 max-h-[210px] overflow-auto rounded-xl border border-[#e5e7eb] bg-[#fbfcfe]">
              {compactProfiles.length === 0 ? (
                <div className="px-6 py-12 text-center text-sm text-[#98a2b3]">暂无可同步实例</div>
              ) : (
                compactProfiles.map(profile => {
                  const isSelected = selectedIds.has(profile.profileId)
                  const isMaster = masterId === profile.profileId
                  const isSelectable = profile.status === 'running'
                  return (
                    <div key={profile.profileId} className={`flex items-center gap-2.5 border-b border-[#e5e7eb] px-3 py-2 last:border-b-0 ${isMaster ? 'bg-[#eef4ff]' : isSelected ? 'bg-[#f7faff]' : ''}`}>
                      <button type="button" className="shrink-0" onClick={() => toggleSelect(profile.profileId)} disabled={!isSelectable}>
                        {isSelected || isMaster
                          ? <CheckSquare className="h-4 w-4 text-[#3a6be0]" />
                          : <Square className="h-4 w-4 text-[#98a2b3]" />}
                      </button>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="truncate text-sm font-semibold text-[#111827]">{profile.badgeNumber > 0 ? `#${profile.badgeNumber} · ` : ''}{profile.profileName || profile.profileId}</span>
                          {isMaster && <span className="rounded-full bg-[#d9e7ff] px-2 py-0.5 text-xs font-semibold text-[#3a6be0]">主控</span>}
                          {!isSelectable && <span className="rounded-full bg-[#f2f4f7] px-2 py-0.5 text-xs font-semibold text-[#667085]">无窗口</span>}
                        </div>
                        <div className="mt-1 text-xs text-[#667085]">PID {profile.pid || '-'} · {profile.status === 'running' ? '运行中' : profile.status === 'no_window' ? '无窗口' : '已停止'}</div>
                      </div>
                      <Button variant={isMaster ? 'primary' : 'secondary'} size="sm" onClick={() => setAsMaster(profile.profileId)} disabled={!isSelectable}>
                        {isMaster ? '主控' : '设主控'}
                      </Button>
                    </div>
                  )
                })
              )}
            </div>
          </div>

          <div className="mt-3 grid grid-cols-[1fr_1fr] gap-2">
            <Button className="h-11 text-base" onClick={() => void handleStartSync()} loading={starting} disabled={!masterId || selectedIds.size < 2}>
              开始同步
            </Button>
            <Button variant="secondary" className="h-11" onClick={handleOpenFullPanel}>
              展开完整页
            </Button>
          </div>

          <div className="mt-2 grid grid-cols-[1fr_1fr] gap-2">
            <Button variant="secondary" className="h-9 text-sm" onClick={() => void handleExitAssistant()}>
              退出同步助手
            </Button>
            <div className="inline-flex h-10 items-center justify-center rounded-2xl bg-[#eef2f7] px-3 text-sm text-[#475467]">
              {masterId ? `主控已设置` : '请先设 1 个主控'}
            </div>
          </div>
        </div>
      </div>
    )
  }

  if (compactSyncStatusMode) {
    return (
      <div className="inline-block overflow-visible bg-transparent px-0 pt-0 text-white">
        <div
          ref={compactPanelRef}
          className="w-[400px] border border-[#26324a] bg-[#0f172a] px-3 py-3 shadow-[0_12px_28px_rgba(15,23,42,.24)]"
          onMouseEnter={handleCompactPanelMouseEnter}
          onMouseLeave={handleCompactPanelMouseLeave}
        >
          <div className="flex min-h-[42px] items-center gap-3" style={{ ['--wails-draggable' as any]: 'drag' }}>
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-white/14 text-white shadow-[inset_0_0_0_1px_rgba(255,255,255,0.08)]">
              <Monitor className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="whitespace-normal break-words text-[15px] font-semibold leading-5 text-white">{activeSyncCount} 个环境同步中</div>
              <div className="mt-0.5 text-[11px] text-white/72">主控 {masterProfile?.profileName || masterProfile?.profileId || '-'} · 跟随 {followerCount} · {statusLayoutLabel}</div>
            </div>
            <button
              type="button"
              className="inline-flex h-8 w-8 items-center justify-center rounded-full border border-white/20 bg-white/10 text-white"
              onClick={() => setPanelPresentation('minimized')}
              title="最小化为悬浮图标"
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              <Minimize2 className="h-4 w-4" />
            </button>
            <button
              type="button"
              className="inline-flex h-8 shrink-0 items-center justify-center self-center rounded-full border border-[#e49aa6] bg-[#d74c68] px-3 text-[13px] font-semibold text-white shadow-[0_8px_20px_rgba(215,76,104,0.24)] transition hover:bg-[#e05c76]"
              onClick={() => void handleStopSync()}
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              重新配置
            </button>
          </div>

          {syncControlsVisible && (
          <div className="mt-3 border-t border-white/12 pt-3">
            <div className="grid grid-cols-3 gap-2">
              <button
                type="button"
                className="inline-flex h-9 items-center justify-center gap-1.5 rounded-2xl bg-[#1c212b] px-3 text-[13px] font-medium text-white hover:bg-[#262c37]"
                onClick={() => void handleTile('grid', '平铺')}
              >
                <LayoutGrid className="h-4 w-4" />平铺
              </button>
              <button
                type="button"
                className="inline-flex h-9 items-center justify-center gap-1.5 rounded-2xl bg-[#1c212b] px-3 text-[13px] font-medium text-white hover:bg-[#262c37]"
                onClick={() => void handleTile('vertical', '堆叠')}
              >
                <Rows className="h-4 w-4" />堆叠
              </button>
              <button
                type="button"
                className="inline-flex h-9 items-center justify-center gap-1.5 rounded-2xl bg-[#1c212b] px-3 text-[13px] font-medium text-white hover:bg-[#262c37]"
                onClick={() => void handleTile('horizontal', '横排')}
              >
                <Columns className="h-4 w-4" />横排
              </button>
            </div>

            <div className="mt-2 rounded-2xl border border-white/14 bg-white/8 p-2.5">
              <div className="flex items-center justify-between gap-3">
                <span className="text-[13px] text-white">同步随机延时</span>
                <button
                  type="button"
                  className={`h-7 rounded-full px-3 text-xs ${randomDelayEnabled ? 'bg-[#dff6e5] text-[#173b21]' : 'bg-white/12 text-white/75'}`}
                  onClick={() => void handleRandomDelayChange(!randomDelayEnabled)}
                >
                  {randomDelayEnabled ? '已开启' : '已关闭'}
                </button>
              </div>
              <div className="mt-2 grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2 text-xs text-white/70">
                <input className="h-8 min-w-0 rounded-lg bg-white/12 px-2 text-white outline-none" type="number" min="0" max="5000" value={randomDelayMinMs} onChange={event => setRandomDelayMinMs(event.target.value)} />
                <span>至</span>
                <input className="h-8 min-w-0 rounded-lg bg-white/12 px-2 text-white outline-none" type="number" min="0" max="5000" value={randomDelayMaxMs} onChange={event => setRandomDelayMaxMs(event.target.value)} />
                <span>ms</span>
              </div>
            </div>

            <button
              type="button"
              className="mt-2 inline-flex h-9 w-full items-center justify-center rounded-2xl border border-white/18 bg-white/90 text-[13px] font-medium text-[#344054] shadow-[0_8px_18px_rgba(16,24,40,0.08)] transition hover:bg-[#eef2f7] hover:text-[#111827]"
              onClick={handleOpenFullPanel}
            >
              回到功能页
            </button>
          </div>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className={`relative min-h-full overflow-hidden ${syncPanelMode ? 'bg-[linear-gradient(180deg,#eff5ff_0%,#f6f8fc_45%,#fbfcfe_100%)] px-3 py-3 sm:px-4 sm:py-4 dark:bg-[var(--color-bg-canvas)]' : 'bg-[linear-gradient(180deg,#eff5ff_0%,#f6f8fc_45%,#fbfcfe_100%)] px-6 py-8 dark:bg-[var(--color-bg-canvas)]'}`}>
      {isSyncing && (
        <div className="fixed left-1/2 top-3 z-40 w-[min(820px,calc(100vw-20px))] -translate-x-1/2 rounded-[20px] bg-[#171a22]/95 px-3 py-3 text-white shadow-[0_24px_80px_rgba(0,0,0,0.35)] backdrop-blur">
          <div className="flex flex-wrap items-center gap-2.5">
            <div className="min-w-[160px] pr-2">
              <div className="text-[11px] uppercase tracking-[0.16em] text-white/50">窗口同步中</div>
              <div className="mt-1 text-base font-semibold text-green-400">{activeSyncCount} 个环境同步中</div>
              <div className="mt-1 truncate text-xs text-white/70">
                主控 {masterProfile?.profileName || masterProfile?.profileId || '-'} · 跟随 {followerCount} · {statusLayoutLabel}
              </div>
            </div>

            <div className="relative">
              <button
                type="button"
                className="inline-flex h-9 items-center gap-2 rounded-xl bg-white/10 px-3 text-sm font-medium text-white hover:bg-white/15"
                onClick={() => setToolbarMenu(prev => (prev === 'layout' ? null : 'layout'))}
              >
                <Grip className="h-4 w-4" />
                排列
                <ChevronDown className="h-4 w-4 opacity-70" />
              </button>
              {toolbarMenu === 'layout' && (
                <div className="absolute left-0 top-12 w-[320px] rounded-2xl border border-white/10 bg-white p-4 text-[var(--color-text-primary)] shadow-2xl">
                  <div className="text-sm font-semibold">排列方式</div>
                  <div className="mt-3 grid grid-cols-1 gap-2">
                    <button
                      type="button"
                      className="flex items-center gap-3 rounded-xl px-3 py-2 text-left text-sm hover:bg-[var(--color-bg-muted)]"
                      onClick={() => {
                        setToolbarMenu(null)
                        void handleTile('grid', '平铺')
                      }}
                    >
                      <LayoutGrid className="h-4 w-4 text-[var(--color-accent)]" />平铺
                    </button>
                    <button
                      type="button"
                      className="flex items-center gap-3 rounded-xl px-3 py-2 text-left text-sm hover:bg-[var(--color-bg-muted)]"
                      onClick={() => {
                        setToolbarMenu(null)
                        void handleTile('vertical', '堆叠')
                      }}
                    >
                      <Rows className="h-4 w-4 text-[var(--color-accent)]" />堆叠
                    </button>
                    <button
                      type="button"
                      className="flex items-center gap-3 rounded-xl px-3 py-2 text-left text-sm hover:bg-[var(--color-bg-muted)]"
                      onClick={() => {
                        setToolbarMenu(null)
                        void handleTile('horizontal', '横排')
                      }}
                    >
                      <Columns className="h-4 w-4 text-[var(--color-accent)]" />横排
                    </button>
                  </div>

                  <div className="mt-4 border-t border-[var(--color-border)] pt-4">
                    <div className="text-sm font-semibold">自定义排列</div>
                    <div className="mt-3 grid grid-cols-2 gap-3">
                      <div>
                        <div className="mb-1 text-xs text-[var(--color-text-secondary)]">列数</div>
                        <Input value={customCols} onChange={e => setCustomCols(e.target.value)} />
                      </div>
                      <div>
                        <div className="mb-1 text-xs text-[var(--color-text-secondary)]">行数</div>
                        <Input value={customRows} onChange={e => setCustomRows(e.target.value)} />
                      </div>
                    </div>
                    <div className="mt-3">
                      <div className="mb-1 text-xs text-[var(--color-text-secondary)]">显示器</div>
                      <Select value={displayLabel} onChange={() => undefined} options={[{ value: displayLabel, label: displayLabel }]} />
                    </div>
                    <Button className="mt-3 w-full" onClick={() => {
                      setToolbarMenu(null)
                      void handleApplyCustomLayout()
                    }}>
                      应用自定义排列
                    </Button>
                  </div>
                </div>
              )}
            </div>

            <button
              type="button"
              className="inline-flex h-9 items-center gap-1.5 rounded-xl bg-white/10 px-3 text-sm font-medium text-white hover:bg-white/15"
              onClick={() => void handleTile('grid', '平铺')}
              title="平铺"
            >
              <LayoutGrid className="h-4 w-4" />平铺
            </button>
            <button
              type="button"
              className="inline-flex h-9 items-center gap-1.5 rounded-xl bg-white/10 px-3 text-sm font-medium text-white hover:bg-white/15"
              onClick={() => void handleTile('vertical', '堆叠')}
              title="堆叠"
            >
              <Rows className="h-4 w-4" />堆叠
            </button>
            <button
              type="button"
              className="inline-flex h-9 items-center gap-1.5 rounded-xl bg-white/10 px-3 text-sm font-medium text-white hover:bg-white/15"
              onClick={() => void handleTile('horizontal', '横排')}
              title="横排"
            >
              <Columns className="h-4 w-4" />横排
            </button>

            <div className="ml-auto flex flex-wrap items-center gap-2 text-sm">
              <button type="button" className="inline-flex h-9 items-center rounded-xl bg-[#4b1620] px-3.5 text-sm font-medium text-[#ff9db0] hover:bg-[#5a1b27]" onClick={() => void handleStopSync()}>
                停止同步
              </button>

            </div>
          </div>
        </div>
      )}

      <div className={`mx-auto transition-all ${isSyncing ? 'pt-18' : ''} ${syncPanelMode ? compactRunningMode ? 'max-w-[700px]' : 'max-w-[900px]' : compactRunningMode ? 'max-w-[860px]' : 'max-w-[980px]'}`}>
        <div className={`rounded-[28px] border border-white/70 bg-white/95 shadow-[0_24px_80px_rgba(35,68,135,0.12)] backdrop-blur dark:border-[var(--color-border)] dark:bg-[var(--color-bg-surface)] ${syncPanelMode ? 'p-4 sm:p-5' : 'p-5'}`}>
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="inline-flex items-center gap-2 rounded-2xl bg-[var(--color-bg-muted)] px-4 py-2 text-sm font-semibold text-[var(--color-accent)]">
                <Monitor className="h-4 w-4" />同步工具
              </div>
              <div className="mt-3 text-sm text-[var(--color-text-secondary)]">
                {compactRunningMode
                  ? '运行中已切到紧凑控制视图，方便只盯状态和快捷操作。'
                  : syncPanelMode
                    ? '这是独立弹出的同步器窗口，主程序页面里不再直接展示。'
                    : '同步器建议以独立窗口方式打开。'}
              </div>
            </div>

            {syncPanelMode && (
              <button
                type="button"
                className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-2xl border border-[#d8dee8] bg-white text-[#475467] shadow-sm transition hover:bg-[#f2f4f7] hover:text-[#101828] dark:border-[var(--color-border)] dark:bg-[var(--color-bg-muted)] dark:text-[var(--color-text-secondary)]"
                onClick={handleClosePanel}
                title="关闭同步窗口"
                aria-label="关闭同步窗口"
              >
                <X className="h-4 w-4" />
              </button>
            )}

          </div>

          {!compactRunningMode && (
            <div className="mt-5 rounded-2xl border border-[#cddcff] bg-[#edf3ff] px-4 py-3 text-sm text-[#4c6fb8]">
              <div className="flex items-center gap-2">
                <Info className="h-4 w-4 shrink-0" />
                <span>主控窗口位置已支持自定义输入，请按需设置。同步开始后顶部会显示运行条，可随时停止和调整窗口排列。</span>
              </div>
            </div>
          )}

          {!compactRunningMode && (
            <div className="mt-4 flex items-center justify-between gap-3">
              <button
                type="button"
                className="inline-flex items-center gap-3 text-sm font-medium text-[var(--color-text-primary)]"
                onClick={handleSelectAllVisible}
                disabled={isSyncing || visibleProfiles.length === 0}
              >
                {allVisibleSelected ? <CheckSquare className="h-4 w-4 text-[var(--color-accent)]" /> : <Square className="h-4 w-4 text-[var(--color-text-muted)]" />}
                <span>{selectedLabel}</span>
              </button>

              <div className="flex items-center gap-2">
                <Button variant="secondary" size="sm" onClick={() => void loadProfiles()} loading={refreshing}>
                  <RefreshCw className="h-4 w-4" />刷新
                </Button>

                <div className="relative">
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 rounded-xl px-3 py-2 text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-bg-muted)]"
                    onClick={() => setFilterOpen(prev => !prev)}
                  >
                    筛选
                    <ChevronDown className="h-4 w-4" />
                  </button>
                  {filterOpen && (
                    <div className="absolute right-0 top-11 z-20 min-w-[160px] rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-2 shadow-xl">
                      {FILTER_OPTIONS.map(option => (
                        <button
                          key={option.value}
                          type="button"
                          className={`flex w-full items-center rounded-xl px-3 py-2 text-left text-sm ${filterMode === option.value ? 'bg-[var(--color-accent-muted)] text-[var(--color-accent)]' : 'text-[var(--color-text-primary)] hover:bg-[var(--color-bg-muted)]'}`}
                          onClick={() => {
                            setFilterMode(option.value)
                            setFilterOpen(false)
                          }}
                        >
                          {option.label}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            </div>
          )}

          {compactRunningMode ? (
            <div className="mt-4 grid gap-3 md:grid-cols-[minmax(0,220px)_minmax(0,1fr)]">
              <div className="rounded-2xl border border-[#dce9ff] bg-[#f4f8ff] px-4 py-4">
                <div className="text-xs font-medium text-[#5f7fbd]">主控环境</div>
                <div className="mt-2 truncate text-base font-semibold text-[var(--color-text-primary)]">{masterProfile?.profileName || masterProfile?.profileId || '-'}</div>
                <div className="mt-2 text-xs text-[var(--color-text-secondary)]">PID {masterProfile?.pid || '-'} · 输入源</div>
                <div className="mt-3 flex flex-wrap gap-2">
                  <span className="rounded-full bg-white px-2.5 py-1 text-xs font-semibold text-[#3a6be0]">{statusLayoutLabel}</span>
                  <span className="rounded-full bg-white px-2.5 py-1 text-xs font-semibold text-[#2c9c59]">跟随 {followerCount}</span>
                </div>
              </div>

              <div className="overflow-hidden rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-surface)]">
                <div className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
                  <div>
                    <div className="text-sm font-semibold text-[var(--color-text-primary)]">跟随环境</div>
                    <div className="mt-1 text-xs text-[var(--color-text-secondary)]">运行态仅保留跟随列表，减少干扰</div>
                  </div>
                  <div className="rounded-full bg-[#eefbf3] px-3 py-1 text-xs font-semibold text-[#2c9c59]">{followerCount} 个</div>
                </div>
                <div className={`${syncPanelMode ? 'max-h-[260px]' : 'max-h-[320px]'} overflow-auto`}>
                  {followerProfiles.length === 0 ? (
                    <div className="px-6 py-10 text-center text-sm text-[var(--color-text-muted)]">当前没有跟随环境</div>
                  ) : (
                    followerProfiles.map(profile => (
                      <div key={profile.profileId} className="flex items-center gap-3 border-b border-[var(--color-border)] px-4 py-3 last:border-b-0">
                        <span className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-[#eefbf3] text-xs font-semibold text-[#2c9c59]">跟</span>
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-semibold text-[var(--color-text-primary)]">{profile.badgeNumber > 0 ? `#${profile.badgeNumber} · ` : ''}{profile.profileName || profile.profileId}</div>
                          <div className="mt-1 text-xs text-[var(--color-text-muted)]">PID {profile.pid || '-'} · 正在跟随主控输入</div>
                        </div>
                      </div>
                    ))
                  )}
                </div>
              </div>
            </div>
          ) : (
            <div className="mt-4 overflow-hidden rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-surface)]">
              <div className={`${syncPanelMode ? 'max-h-[300px] sm:max-h-[340px]' : 'max-h-[420px]'} overflow-auto`}>
                {visibleProfiles.length === 0 ? (
                  <div className="px-6 py-16 text-center text-sm text-[var(--color-text-muted)]">暂无可同步实例</div>
                ) : (
                  visibleProfiles.map(profile => {
                    const isSelected = selectedIds.has(profile.profileId)
                    const isMaster = masterId === profile.profileId
                    const isFollower = isSyncing && syncStatus?.followerIds?.includes(profile.profileId)
                    const isSelectable = profile.status === 'running'
                    return (
                      <div
                        key={profile.profileId}
                        className={`flex items-center gap-3 border-b border-[var(--color-border)] px-4 py-3 last:border-b-0 ${isMaster ? 'bg-[#eef4ff]' : isFollower ? 'bg-[#eefbf3]' : isSelected ? 'bg-[var(--color-accent-muted)]/50' : ''}`}
                      >
                        <button type="button" className="shrink-0" onClick={() => toggleSelect(profile.profileId)} disabled={isSyncing || !isSelectable}>
                          {isSelected || isMaster || isFollower
                            ? <CheckSquare className="h-4 w-4 text-[var(--color-accent)]" />
                            : <Square className="h-4 w-4 text-[var(--color-text-muted)]" />}
                        </button>
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2">
                            <span className="truncate text-sm font-semibold text-[var(--color-text-primary)]">{profile.badgeNumber > 0 ? `#${profile.badgeNumber} · ` : ''}{profile.profileName || profile.profileId}</span>
                            {isMaster && <span className="rounded-full bg-[#d9e7ff] px-2 py-0.5 text-xs font-semibold text-[#3a6be0]">主控</span>}
                            {!isMaster && isFollower && <span className="rounded-full bg-[#dcf7e4] px-2 py-0.5 text-xs font-semibold text-[#2c9c59]">跟随</span>}
                          </div>
                          <div className="mt-1 text-xs text-[var(--color-text-muted)]">PID {profile.pid || '-'} · {profile.status === 'running' ? '运行中' : profile.status === 'no_window' ? '无窗口' : '已停止'}</div>
                        </div>
                        {!isSyncing && (
                          <Button variant={isMaster ? 'primary' : 'secondary'} size="sm" onClick={() => setAsMaster(profile.profileId)} disabled={!isSelectable}>
                            {isMaster ? '主控' : '设为主控'}
                          </Button>
                        )}
                      </div>
                    )
                  })
                )}
              </div>
            </div>
          )}

          {!isSyncing && (
            <div className={`mt-5 grid gap-3 ${syncPanelMode ? 'lg:grid-cols-[1fr_180px_180px]' : 'md:grid-cols-[1fr_220px_220px]'}`}>
              <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-4 py-3">
                <div className="text-sm font-semibold text-[var(--color-text-primary)]">准备同步</div>
                <div className="mt-1 text-xs text-[var(--color-text-secondary)]">先勾选环境，再指定 1 个主控。当前支持直接平铺、堆叠、自定义行列数排列。</div>
              </div>
              <Select value={tileLayout} onChange={e => setTileLayout(e.target.value as TileLayoutMode)} options={LAYOUT_OPTIONS} />
              <Button variant="secondary" onClick={() => void handleTile(tileLayout)} disabled={selectedIds.size === 0}>
                <Move className="h-4 w-4" />先排列窗口
              </Button>
            </div>
          )}

          <div className={`space-y-4 ${compactRunningMode ? 'mt-4' : 'mt-6'}`}>
            {isSyncing && !compactRunningMode ? (
              <div className={`grid gap-3 ${syncPanelMode ? 'sm:grid-cols-3' : 'md:grid-cols-3'}`}>
                <div className="rounded-2xl border border-[#dce9ff] bg-[#f4f8ff] px-4 py-3">
                  <div className="text-xs text-[var(--color-text-secondary)]">主控环境</div>
                  <div className="mt-1 truncate text-sm font-semibold text-[var(--color-text-primary)]">{masterProfile?.profileName || masterProfile?.profileId || '-'}</div>
                </div>
                <div className="rounded-2xl border border-[#dcf7e4] bg-[#eefbf3] px-4 py-3">
                  <div className="text-xs text-[var(--color-text-secondary)]">跟随环境</div>
                  <div className="mt-1 text-sm font-semibold text-[var(--color-text-primary)]">{followerCount} 个</div>
                </div>
                <div className="rounded-2xl border border-[#ece3ff] bg-[#f7f1ff] px-4 py-3">
                  <div className="text-xs text-[var(--color-text-secondary)]">最近排列</div>
                  <div className="mt-1 text-sm font-semibold text-[var(--color-text-primary)]">{statusLayoutLabel}</div>
                </div>
              </div>
            ) : null}

            {isSyncing && followerProfiles.length > 0 && !compactRunningMode ? (
              <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-4 py-4">
                <div className="text-sm font-semibold text-[var(--color-text-primary)]">当前跟随列表</div>
                <div className="mt-3 flex flex-wrap gap-2">
                  {followerProfiles.map(profile => (
                    <span key={profile.profileId} className="inline-flex max-w-full items-center rounded-full bg-[#eefbf3] px-3 py-1 text-xs font-medium text-[#2c9c59]">
                      <span className="truncate">{profile.profileName || profile.profileId}</span>
                    </span>
                  ))}
                </div>
              </div>
            ) : null}

            <div className="flex w-full flex-col gap-3 sm:flex-row sm:justify-center">
              {!isSyncing ? (
                <Button className="h-11 w-full max-w-[320px] text-base" onClick={() => void handleStartSync()} loading={starting} disabled={!masterId || selectedIds.size < 2}>
                  开始同步
                </Button>
              ) : !compactRunningMode ? (
                <Button variant="danger" className="h-11 w-full max-w-[320px] text-base" onClick={() => void handleStopSync()}>
                  停止同步
                </Button>
              ) : null}

              {!isSyncing && (
                <Button variant="secondary" className="h-10 w-full max-w-[180px]" onClick={() => void handleExitAssistant()}>
                  退出同步助手
                </Button>
              )}


            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
