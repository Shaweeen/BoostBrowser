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
  addFollowerToSync,
  getSyncSnapshot,
  getSyncStatus,
  refreshSyncSnapshot,
  removeFollowerFromSync,
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
// Delay is OFF by default (immediate sync). Only "random" is an explicit opt-in.
type DelayPreset = 'off' | 'random'

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
const PANEL_COMPACT_STATUS_COLLAPSED_SIZE = { width: 400, height: 150, minWidth: 360, minHeight: 120 }
const PANEL_COMPACT_FUNCTION_SIZE = { width: 440, height: 108, minWidth: 440, minHeight: 108 }
const PANEL_TOP_MARGIN_PX = 8
const PANEL_COMPACT_EDGE_PADDING_PX = 0
const PANEL_MINI_SIZE = { width: 136, height: 44, minWidth: 136, minHeight: 44 }

export function WindowSyncPage() {
  const compactPanelRef = useRef<HTMLDivElement | null>(null)
  const compactPanelLeaveTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null)
  const syncPanelWindowBootstrappedRef = useRef(false)
  const autoCollapseTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null)
  const resumeNoticeTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null)
  const [syncPanelMode, setSyncPanelMode] = useState(false)
  const [panelPresentation, setPanelPresentation] = useState<'minimized' | 'compact' | 'full'>('full')
  const [showSyncControls, setShowSyncControls] = useState(false)
  const [profiles, setProfiles] = useState<SyncProfileInfo[]>([])
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set())
  const [masterId, setMasterId] = useState<string | null>(null)
  const [syncStatus, setSyncStatus] = useState<SyncStatus | null>(null)
  const [starting, setStarting] = useState(false)
  const [stopping, setStopping] = useState(false)
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
  const [delayPreset, setDelayPreset] = useState<DelayPreset>('off')
  const [resumeNoticeVisible, setResumeNoticeVisible] = useState(false)

  const loadProfilesSeq = useRef(0)
  const startingRef = useRef(false)
  const stoppingRef = useRef(false)
  const loadProfiles = useCallback((force = false, silent = false): Promise<SyncProfileInfo[]> => {
    const seq = ++loadProfilesSeq.current
    // Background polls run silently: they must not flash the refresh button
    // spinner or prune the user's in-progress selection when a window is
    // transiently resolving.
    if (!silent) setRefreshing(true)
    const request = (async () => {
      // force = explicit refresh: bypass the backend's 2s process-scan cache so
      // a just-started environment is visible immediately.
      const snapshot = force ? await refreshSyncSnapshot() : await getSyncSnapshot()
      const list = snapshot.profiles
      const status = snapshot.status
      const sorted = [...list].sort(compareProfileName)
      if (seq !== loadProfilesSeq.current) return sorted

      setProfiles(sorted)
      setSyncStatus(status)
      if (status?.active) {
        // Any enabled delay maps to the single opt-in "random" mode.
        setDelayPreset(status.randomDelayEnabled ? 'random' : 'off')
      } else {
        setDelayPreset('off')
      }
      if (status?.active) {
        const nextSelected = new Set([status.masterId, ...(status.followerIds || [])].filter(Boolean))
        setSelectedIds(nextSelected)
        setMasterId(status.masterId)
        return sorted
      }

      if (!silent) {
        // This is an explicit collection boundary (manual refresh only). Drop
        // selections whose current top-level window no longer exists so a closed
        // environment cannot poison the next master/follower configuration.
        const availableIds = new Set(sorted.filter(item => item.status === 'running').map(item => item.profileId))
        setSelectedIds(prev => {
          const next = new Set<string>()
          prev.forEach(id => {
            if (availableIds.has(id)) next.add(id)
          })
          return next
        })
        setMasterId(prev => (prev && availableIds.has(prev) ? prev : null))
      }
      return sorted
    })().finally(() => {
      if (seq === loadProfilesSeq.current && !silent) setRefreshing(false)
    })
    return request
  }, [])

  const releaseCollectedSyncData = useCallback(() => {
    loadProfilesSeq.current += 1
    setProfiles([])
    setSelectedIds(new Set())
    setMasterId(null)
    setSyncStatus(null)
    setDelayPreset('off')
    setFilterMode('all')
    setFilterOpen(false)
    setRefreshing(false)
  }, [])

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
    // Collect once when the assistant opens. Afterwards collection is strictly
    // user-driven; focus changes and one-second timers must not mutate a
    // configuration while the user is pausing, closing or replacing windows.
    void loadProfiles()
    const offPauseChanged = EventsOn('window-sync:pause-changed', (payload: { paused?: boolean }) => {
      const paused = payload?.paused === true
      setSyncStatus(prev => prev ? { ...prev, paused } : prev)
      if (!paused) {
        if (resumeNoticeTimerRef.current) window.clearTimeout(resumeNoticeTimerRef.current)
        setResumeNoticeVisible(true)
        resumeNoticeTimerRef.current = window.setTimeout(() => {
          setResumeNoticeVisible(false)
          resumeNoticeTimerRef.current = null
        }, 1000)
      }
    })

    return () => {
      offPauseChanged?.()
      if (resumeNoticeTimerRef.current) {
        window.clearTimeout(resumeNoticeTimerRef.current)
        resumeNoticeTimerRef.current = null
      }
    }
  }, [loadProfiles])

  useEffect(() => {
    const screen = window.screen
    if (!screen) return
    const width = screen.availWidth || screen.width
    const height = screen.availHeight || screen.height
    setDisplayLabel(`当前显示器（${width}x${height}）`)
  }, [])

  const displayProfiles = profiles
  const isSyncing = syncStatus?.active === true
  const isSyncPaused = isSyncing && syncStatus?.paused === true
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

  // Auto-refresh the live environment list while the assistant is open, both
  // idle and during an active sync session. A closed/reopened or newly opened
  // environment then appears without a manual refresh — so a just-started
  // environment can be added as a follower and arranged through the sync tool
  // right away. Silent mode keeps the refresh button idle and never prunes the
  // user's in-progress selection (while syncing, selection mirrors the session).
  // During an active session the interval is longer: the panel's live process
  // scan is serialized with sync start/stop/tile actions, so a slow scan on
  // dense multi-open must not stall input-sync actions on every tick.
  useEffect(() => {
    const intervalMs = isSyncing ? 10000 : 3000
    const timer = window.setInterval(() => {
      void loadProfiles(false, true)
    }, intervalMs)
    return () => window.clearInterval(timer)
  }, [isSyncing, loadProfiles])

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
  }, [compactFunctionPanelMode, compactSyncStatusMode, syncControlsVisible, syncPanelMode, activeSyncCount, followerCount, statusLayoutLabel, masterProfile?.profileId, masterProfile?.profileName, syncStatus?.mouseEnabled, syncStatus?.keyEnabled, syncStatus?.paused, displayProfiles.length, selectedCount, masterId])

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
    if (isSyncing || startingRef.current || stoppingRef.current) return
    if (!profiles.some(item => item.profileId === id && item.status === 'running')) {
      toast.error('该环境窗口已关闭，请重新选择主控')
      return
    }
    setMasterId(id)
    setSelectedIds(prev => new Set([...prev, id]))
  }

  const handleSelectAllVisible = () => {
    if (isSyncing || startingRef.current || stoppingRef.current) return
    const visibleIds = profiles.filter(item => item.status === 'running').map(item => item.profileId)
    if (visibleIds.length === 0) {
      toast.error('没有检测到已打开的环境窗口')
      return
    }
    setSelectedIds(prev => {
      const allSelected = visibleIds.every(id => prev.has(id))
      const next = new Set(prev)
      if (allSelected) {
        visibleIds.forEach(id => next.delete(id))
        if (masterId && !next.has(masterId)) {
          setMasterId(null)
        }
      } else {
        visibleIds.forEach(id => next.add(id))
      }
      return next
    })
  }

  const handleStartSync = async () => {
    if (startingRef.current || stoppingRef.current || isSyncing) return
    if (!masterId) {
      toast.error('请先指定主控环境')
      return
    }
    const followers = Array.from(selectedIds).filter(id => id !== masterId)
    if (followers.length === 0) {
      toast.error('请至少再选 1 个跟随环境')
      return
    }
    startingRef.current = true
    setStarting(true)
    try {
      const err = await startInputSync(masterId, followers)
      if (err) {
        toast.error(`启动同步失败：${err}`)
        await loadProfiles()
        return
      }
      const status = await getSyncStatus()
      if (status) {
        setSyncStatus(status)
        setSelectedIds(new Set([status.masterId, ...(status.followerIds || [])].filter(Boolean)))
        setMasterId(status.masterId)
      }
      // Always start in immediate mode; random delay is opt-in only.
      setDelayPreset('off')
      // Backend also defaults to immediate; re-assert in case a previous session
      // left random delay enabled on a recycled process.
      void updateSyncRandomDelay(false, 0, 0)
      setPanelPresentation('compact')
      setShowSyncControls(false)
    } finally {
      startingRef.current = false
      setStarting(false)
    }
  }

  const handleStopSync = async () => {
    if (stoppingRef.current || startingRef.current) return
    stoppingRef.current = true
    setStopping(true)
    try {
      const err = await stopInputSync()
      if (err) {
        toast.error(`停止同步失败：${err}`)
        return
      }
      setShowSyncControls(false)
      setPanelPresentation('compact')
      setToolbarMenu(null)
      releaseCollectedSyncData()
    } finally {
      stoppingRef.current = false
      setStopping(false)
    }
  }

  const handleDelayPresetChange = async (preset: DelayPreset) => {
    if (!isSyncing) return
    if (preset === 'off') {
      const err = await updateSyncRandomDelay(false, 0, 0)
      if (err) {
        toast.error(`关闭同步延时失败：${err}`)
        return
      }
      setDelayPreset('off')
      setSyncStatus(prev => prev ? { ...prev, randomDelayEnabled: false, randomDelayMinMs: 0, randomDelayMaxMs: 0 } : prev)
      return
    }
    // Opt-in: each follower window runs master actions after a random delay.
    const minMs = 1
    const maxMs = 30
    const err = await updateSyncRandomDelay(true, minMs, maxMs)
    if (err) {
      toast.error(`更新同步延时失败：${err}`)
      return
    }
    setDelayPreset('random')
    setSyncStatus(prev => prev ? { ...prev, randomDelayEnabled: true, randomDelayMinMs: minMs, randomDelayMaxMs: maxMs } : prev)
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

  const handleExitAssistant = async () => {
    releaseCollectedSyncData()
    await ExitWindowSyncPanel().catch(() => {})
  }

  const handleOpenFullPanel = () => {
    if (!syncPanelMode) return
    setShowSyncControls(true)
    setPanelPresentation('full')
  }

  // Dynamic follower management during active sync
  const [followerUpdating, setFollowerUpdating] = useState(false)

  const handleAddFollower = async (profileId: string) => {
    if (followerUpdating) return
    setFollowerUpdating(true)
    try {
      const err = await addFollowerToSync(profileId)
      if (err) {
        toast.error(`添加跟随失败：${err}`)
        return
      }
      toast.success('已添加跟随环境')
      // Refresh status to update the UI
      const status = await getSyncStatus().catch(() => null)
      if (status) {
        setSyncStatus(status)
      }
    } finally {
      setFollowerUpdating(false)
    }
  }

  const handleRemoveFollower = async (profileId: string) => {
    if (followerUpdating) return
    setFollowerUpdating(true)
    try {
      const err = await removeFollowerFromSync(profileId)
      if (err) {
        toast.error(`移除跟随失败：${err}`)
        return
      }
      toast.success('已移除跟随环境')
      // Refresh status to update the UI
      const status = await getSyncStatus().catch(() => null)
      if (status) {
        setSyncStatus(status)
      }
    } finally {
      setFollowerUpdating(false)
    }
  }

  // Profiles that can be added as followers (running, not master, not already follower)
  const availableFollowers = useMemo(() => {
    if (!isSyncing) return []
    const followerSet = new Set(syncStatus?.followerIds || [])
    return displayProfiles.filter(
      p => p.status === 'running' && p.profileId !== syncStatus?.masterId && !followerSet.has(p.profileId)
    )
  }, [displayProfiles, isSyncing, syncStatus?.followerIds, syncStatus?.masterId])

  if (minimizedPanelMode) {
    return (
      <div className="relative flex h-11 w-[160px] items-center overflow-hidden bg-[#f8fafc] px-1.5 shadow-[0_8px_22px_rgba(30,58,110,.18)]">
        {resumeNoticeVisible && (
          <div className="absolute inset-1 z-20 flex items-center justify-center rounded-[8px] bg-[#16a34a] px-1 text-center text-[9px] font-bold leading-3 text-white shadow-[0_4px_12px_rgba(22,163,74,.3)]">
            同步已恢复
          </div>
        )}
        <div className="flex h-9 w-9 shrink-0 cursor-move items-center justify-center" title="拖动同步工具" style={{ ['--wails-draggable' as any]: 'drag' }}>
          <span className="relative flex h-8 w-8 items-center justify-center rounded-[9px] bg-[#17263d] text-[14px] font-black text-white">
            B
            <span className={`absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full border-2 border-[#f7f9fc] ${isSyncPaused ? 'bg-[#f59e0b]' : isSyncing ? 'bg-[#22c55e]' : 'bg-[#f59e0b]'}`} />
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
            <span className="block truncate text-[8px] leading-3 text-[#738199]">
              {isSyncPaused 
                ? `已暂停 · ${followerCount}个跟随` 
                : isSyncing 
                  ? `${followerCount}个跟随 · ${statusLayoutLabel}` 
                  : '点击展开'}
            </span>
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
              onClick={() => void loadProfiles(true)}
              disabled={refreshing || starting || stopping}
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              <RefreshCw className="mr-1.5 h-4 w-4" />刷新
            </button>
            <button
              type="button"
              className="inline-flex h-9 w-9 shrink-0 items-center justify-center self-center rounded-full border border-[#c8d0dc] bg-white text-[#344054] shadow-[0_8px_18px_rgba(16,24,40,0.12)] transition hover:bg-[#eef2f7] hover:text-[#111827]"
              onClick={() => void handleExitAssistant()}
              title="退出同步助手"
              aria-label="退出同步助手"
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
          className="relative w-[400px] border border-[#26324a] bg-[#0f172a] px-3 py-3 shadow-[0_12px_28px_rgba(15,23,42,.24)]"
          onMouseEnter={handleCompactPanelMouseEnter}
          onMouseLeave={handleCompactPanelMouseLeave}
        >
          {resumeNoticeVisible && (
            <div className="absolute inset-x-3 top-2 z-30 flex h-9 items-center justify-center rounded-xl bg-[#16a34a] px-3 text-[12px] font-bold text-white shadow-[0_8px_20px_rgba(22,163,74,.3)]">
              同步功能已恢复
            </div>
          )}
          <div className="flex min-h-[42px] items-center gap-3" style={{ ['--wails-draggable' as any]: 'drag' }}>
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-white/14 text-white shadow-[inset_0_0_0_1px_rgba(255,255,255,0.08)]">
              <Monitor className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="whitespace-normal break-words text-[15px] font-semibold leading-5 text-white">
                {isSyncPaused ? '同步已暂停' : `${activeSyncCount} 个环境同步中`}
              </div>
              <div className="mt-0.5 text-[11px] text-white/72">主控 {masterProfile?.profileName || masterProfile?.profileId || '-'} · 跟随 {followerCount} · {statusLayoutLabel} · Esc {isSyncPaused ? '恢复' : '暂停'}</div>
              <div className="mt-0.5 text-[10px] text-white/55">Ctrl+滚轮缩放 · Shift+滚轮横向 · 中文输入法自动跟打</div>
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
              disabled={stopping}
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              重新配置
            </button>
            <button
              type="button"
              className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-white/20 bg-white/10 text-white transition hover:border-[#d74c68] hover:bg-[#d74c68]"
              onClick={() => void handleExitAssistant()}
              title="退出同步助手"
              aria-label="退出同步助手"
              style={{ ['--wails-draggable' as any]: 'no-drag' }}
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <div className="mt-2 grid grid-cols-2 gap-2" style={{ ['--wails-draggable' as any]: 'no-drag' }}>
            {([['off', '立即同步'], ['random', '随机延时']] as Array<[DelayPreset, string]>).map(([value, label]) => (
              <button
                key={value}
                type="button"
                className={`h-8 rounded-xl border px-2 text-xs font-semibold transition ${delayPreset === value ? 'border-[#4ade80] bg-[#22c55e] text-white shadow-[0_6px_16px_rgba(34,197,94,.24)]' : 'border-white/16 bg-white/10 text-white/80 hover:bg-white/16'}`}
                onClick={() => void handleDelayPresetChange(value)}
                title={value === 'off' ? '默认：主控操作后所有窗口立即同步，无额外延时' : '可选：主控操作后，各跟随窗口在 1–30ms 内随机延时执行'}
              >
                {label}
              </button>
            ))}
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
          {resumeNoticeVisible && (
            <div className="absolute inset-0 z-50 flex items-center justify-center rounded-[20px] bg-[#16a34a] px-5 text-base font-bold text-white shadow-[0_16px_40px_rgba(22,163,74,.35)]">
              同步功能已恢复
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2.5">
            <div className="min-w-[160px] pr-2">
              <div className="text-[11px] uppercase tracking-[0.16em] text-white/50">{isSyncPaused ? '窗口同步已暂停' : '窗口同步中'}</div>
              <div className={`mt-1 text-base font-semibold ${isSyncPaused ? 'text-amber-300' : 'text-green-400'}`}>
                {isSyncPaused ? '按 Esc 恢复同步' : `${activeSyncCount} 个环境同步中`}
              </div>
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

            <div className="inline-flex h-9 items-center gap-2 rounded-xl border border-white/12 bg-white/10 px-3 text-xs text-white/80" title="同步开启期间全局生效">
              <kbd className="rounded border border-white/25 bg-white/12 px-1.5 py-0.5 font-mono text-white">Esc</kbd>
              {isSyncPaused ? '恢复同步' : '暂停同步'}
            </div>

            <div className="ml-auto flex flex-wrap items-center gap-2 text-sm">
              <button type="button" disabled={stopping} className="inline-flex h-9 items-center rounded-xl bg-[#4b1620] px-3.5 text-sm font-medium text-[#ff9db0] hover:bg-[#5a1b27] disabled:opacity-60" onClick={() => void handleStopSync()}>
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
                onClick={() => void handleExitAssistant()}
                title="退出同步助手"
                aria-label="退出同步助手"
              >
                <X className="h-4 w-4" />
              </button>
            )}

          </div>

          {!compactRunningMode && (
            <div className="mt-5 rounded-2xl border border-[#cddcff] bg-[#edf3ff] px-4 py-3 text-sm text-[#4c6fb8]">
              <div className="flex items-start gap-2">
                <Info className="mt-0.5 h-4 w-4 shrink-0" />
                <div className="space-y-1.5">
                  <div className="font-medium text-[#3a5fad]">同步操作提示</div>
                  <div>1. 勾选环境 → 设主控 → 开始同步。在主控窗口操作，跟随窗口实时复现。</div>
                  <div>2. <span className="font-medium">Esc</span> 暂停/恢复 · <span className="font-medium">Ctrl+滚轮</span> 缩放 · <span className="font-medium">Shift+滚轮</span> 横向滚动 · 滚轮/滚动条/键鼠/输入法均可同步。</div>
                  <div>3. 默认同步无延时（最跟手）；仅当需要模拟人工错峰时，再手动打开「随机延时」。</div>
                </div>
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
                <Button variant="secondary" size="sm" onClick={() => void loadProfiles(true)} loading={refreshing} disabled={starting || stopping}>
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
                        <button
                          type="button"
                          className="inline-flex h-7 shrink-0 items-center justify-center rounded-lg border border-red-200 bg-red-50 px-2 text-xs font-medium text-red-600 transition hover:bg-red-100 disabled:opacity-50"
                          onClick={() => void handleRemoveFollower(profile.profileId)}
                          disabled={followerUpdating}
                          title="移除此跟随环境"
                        >
                          <X className="h-3 w-3" />
                        </button>
                      </div>
                    ))
                  )}
                </div>
                {availableFollowers.length > 0 && (
                  <div className="border-t border-[var(--color-border)] px-4 py-3">
                    <div className="text-xs font-medium text-[var(--color-text-secondary)] mb-2">添加更多跟随环境</div>
                    <div className="flex flex-wrap gap-2">
                      {availableFollowers.slice(0, 5).map(profile => (
                        <button
                          key={profile.profileId}
                          type="button"
                          className="inline-flex items-center gap-1.5 rounded-lg border border-dashed border-[var(--color-border)] bg-[var(--color-bg-muted)] px-2.5 py-1.5 text-xs font-medium text-[var(--color-text-secondary)] transition hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:opacity-50"
                          onClick={() => void handleAddFollower(profile.profileId)}
                          disabled={followerUpdating}
                        >
                          <span className="text-[10px]">+</span>
                          {profile.badgeNumber > 0 ? `#${profile.badgeNumber}` : (profile.profileName || profile.profileId).slice(0, 8)}
                        </button>
                      ))}
                      {availableFollowers.length > 5 && (
                        <span className="inline-flex items-center px-2 text-xs text-[var(--color-text-muted)]">
                          +{availableFollowers.length - 5} 更多
                        </span>
                      )}
                    </div>
                  </div>
                )}
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

            {isSyncing && !compactRunningMode ? (
              <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-4 py-4">
                <div className="flex items-center justify-between">
                  <div className="text-sm font-semibold text-[var(--color-text-primary)]">当前跟随列表</div>
                  <div className="rounded-full bg-[#eefbf3] px-2.5 py-1 text-xs font-semibold text-[#2c9c59]">{followerCount} 个</div>
                </div>
                {followerProfiles.length > 0 && (
                  <div className="mt-3 flex flex-wrap gap-2">
                    {followerProfiles.map(profile => (
                      <span key={profile.profileId} className="inline-flex max-w-full items-center gap-1.5 rounded-full bg-[#eefbf3] px-3 py-1 text-xs font-medium text-[#2c9c59]">
                        <span className="truncate">{profile.profileName || profile.profileId}</span>
                        <button
                          type="button"
                          className="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-red-100 text-red-500 transition hover:bg-red-200 disabled:opacity-50"
                          onClick={() => void handleRemoveFollower(profile.profileId)}
                          disabled={followerUpdating}
                          title="移除此跟随环境"
                        >
                          <X className="h-2.5 w-2.5" />
                        </button>
                      </span>
                    ))}
                  </div>
                )}
                {followerProfiles.length === 0 && (
                  <div className="mt-2 text-xs text-[var(--color-text-muted)]">暂无跟随环境</div>
                )}
                {availableFollowers.length > 0 && (
                  <div className="mt-3 border-t border-[var(--color-border)] pt-3">
                    <div className="text-xs font-medium text-[var(--color-text-secondary)] mb-2">添加更多跟随环境</div>
                    <div className="flex flex-wrap gap-2">
                      {availableFollowers.slice(0, 8).map(profile => (
                        <button
                          key={profile.profileId}
                          type="button"
                          className="inline-flex items-center gap-1.5 rounded-full border border-dashed border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-1.5 text-xs font-medium text-[var(--color-text-secondary)] transition hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:opacity-50"
                          onClick={() => void handleAddFollower(profile.profileId)}
                          disabled={followerUpdating}
                        >
                          <span className="text-[10px]">+</span>
                          {profile.badgeNumber > 0 ? `#${profile.badgeNumber}` : (profile.profileName || profile.profileId).slice(0, 10)}
                        </button>
                      ))}
                      {availableFollowers.length > 8 && (
                        <span className="inline-flex items-center px-2 text-xs text-[var(--color-text-muted)]">
                          +{availableFollowers.length - 8} 更多
                        </span>
                      )}
                    </div>
                  </div>
                )}
              </div>
            ) : null}

            <div className="flex w-full flex-col gap-3 sm:flex-row sm:justify-center">
              {!isSyncing ? (
                <Button className="h-11 w-full max-w-[320px] text-base" onClick={() => void handleStartSync()} loading={starting} disabled={!masterId || selectedIds.size < 2}>
                  开始同步
                </Button>
              ) : !compactRunningMode ? (
                <Button variant="danger" className="h-11 w-full max-w-[320px] text-base" onClick={() => void handleStopSync()} loading={stopping}>
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
