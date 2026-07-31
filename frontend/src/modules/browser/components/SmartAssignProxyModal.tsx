import { useEffect, useMemo, useState } from 'react'
import { Button, FormItem, Input, Modal, Select, Switch, toast } from '../../../shared/components'
import { Loader2 } from 'lucide-react'
import type { BrowserProfile, BrowserProxy, BrowserGroup } from '../types'
import { applyTimezoneToFingerprintArgs, fetchBrowserProfiles, fetchGroups, suggestTimezoneFromProxy, updateBrowserProfile } from '../api'

const ALL_GROUPS = '__all__'
const NO_GROUP = '__none__'

// 代理范围
type ProxyScope = 'all' | 'filtered' | 'selected' | 'unassigned'
// 实例范围
type ProfileScope = 'all' | 'group' | 'unassigned'

interface SmartAssignProxyModalProps {
  open: boolean
  /** 全部可用代理（已经过过滤掉内置 direct/local 之外的，按调用方决定） */
  allProxies: BrowserProxy[]
  /** 当前页面筛选后的代理（用户在代理池表格里筛出来的那批） */
  filteredProxies: BrowserProxy[]
  /** 已勾选的代理 ID */
  selectedProxyIds: Set<string>
  onClose: () => void
  /** 分配完成后通知调用方刷新（可选） */
  onDone?: () => void
}

interface AssignmentRow {
  profile: BrowserProfile
  proxy: BrowserProxy
}

function parseProxyEndpoint(proxyConfig?: string): { protocol: string; hostPort: string } {
  const raw = (proxyConfig || '').trim()
  if (!raw) return { protocol: '-', hostPort: '-' }
  try {
    const withScheme = raw.includes('://') ? raw : `http://${raw}`
    const u = new URL(withScheme)
    const protocol = (u.protocol.replace(':', '') || 'http').toUpperCase()
    const hostPort = u.port ? `${u.hostname}:${u.port}` : u.hostname || raw
    return { protocol, hostPort }
  } catch {
    return { protocol: '-', hostPort: raw.slice(0, 48) }
  }
}

function formatProxyOptionLabel(proxy: BrowserProxy, assignedCount: number): string {
  const { protocol, hostPort } = parseProxyEndpoint(proxy.proxyConfig)
  const name = proxy.proxyName || proxy.proxyId
  const status = assignedCount > 0 ? `已绑${assignedCount}` : '未分配'
  const group = proxy.groupName ? ` · ${proxy.groupName}` : ''
  const latency =
    typeof proxy.lastLatencyMs === 'number' && proxy.lastLatencyMs >= 0 && proxy.lastTestOk
      ? ` · ${proxy.lastLatencyMs}ms`
      : proxy.lastTestOk === false && proxy.lastTestedAt
        ? ' · 不通'
        : ''
  return `${name} · ${protocol} ${hostPort}${latency} · ${status}${group}`
}

export function SmartAssignProxyModal({
  open,
  allProxies,
  filteredProxies,
  selectedProxyIds,
  onClose,
  onDone,
}: SmartAssignProxyModalProps) {
  const [perProxyCount, setPerProxyCount] = useState('1')
  const [proxyScope, setProxyScope] = useState<ProxyScope>('unassigned')
  const [profileScope, setProfileScope] = useState<ProfileScope>('unassigned')
  const [groupId, setGroupId] = useState<string>(ALL_GROUPS)
  const [skipAlreadyAssigned, setSkipAlreadyAssigned] = useState(true)
  const [includeBuiltin, setIncludeBuiltin] = useState(false)
  /** Optional: write fingerprint --timezone from proxy exit country after bind. */
  const [alignTimezoneToProxy, setAlignTimezoneToProxy] = useState(false)

  const [profiles, setProfiles] = useState<BrowserProfile[]>([])
  const [groups, setGroups] = useState<BrowserGroup[]>([])
  const [loading, setLoading] = useState(false)
  const [running, setRunning] = useState(false)
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)

  // 打开时拉数据；默认优先「未分配 IP / 未绑定实例」
  useEffect(() => {
    if (!open) return
    setProgress(null)
    setProxyScope(selectedProxyIds.size > 0 ? 'selected' : 'unassigned')
    setProfileScope('unassigned')
    setSkipAlreadyAssigned(true)
    void (async () => {
      setLoading(true)
      try {
        const [profileList, groupList] = await Promise.all([fetchBrowserProfiles(), fetchGroups()])
        setProfiles(profileList)
        setGroups(groupList)
      } finally {
        setLoading(false)
      }
    })()
  }, [open, selectedProxyIds])

  // 每个代理当前绑定了多少环境
  const proxyBindCount = useMemo(() => {
    const m = new Map<string, number>()
    for (const p of profiles) {
      const id = (p.proxyId || '').trim()
      if (!id || id === '__direct__') continue
      m.set(id, (m.get(id) || 0) + 1)
    }
    return m
  }, [profiles])

  const realProxies = useMemo(
    () => allProxies.filter(p => p.proxyId !== '__direct__' && p.proxyId !== '__local__'),
    [allProxies],
  )

  const unassignedProxies = useMemo(
    () => realProxies.filter(p => (proxyBindCount.get(p.proxyId) || 0) === 0),
    [realProxies, proxyBindCount],
  )

  // 候选代理（按当前选择的范围决定，并按是否包含内置代理过滤）
  const candidateProxies = useMemo<BrowserProxy[]>(() => {
    let pool: BrowserProxy[] = []
    if (proxyScope === 'all') pool = allProxies
    else if (proxyScope === 'filtered') pool = filteredProxies
    else if (proxyScope === 'unassigned') pool = unassignedProxies
    else pool = allProxies.filter(p => selectedProxyIds.has(p.proxyId))

    if (!includeBuiltin) {
      pool = pool.filter(p => p.proxyId !== '__direct__' && p.proxyId !== '__local__')
    }
    return pool
  }, [proxyScope, allProxies, filteredProxies, selectedProxyIds, includeBuiltin, unassignedProxies])

  // 候选实例（按分组等条件筛选；按 profileName 字典序稳定排序）
  const candidateProfiles = useMemo<BrowserProfile[]>(() => {
    let pool = profiles
    if (profileScope === 'group') {
      if (groupId === NO_GROUP) {
        pool = pool.filter(p => !p.groupId)
      } else if (groupId !== ALL_GROUPS) {
        pool = pool.filter(p => p.groupId === groupId)
      }
    } else if (profileScope === 'unassigned') {
      pool = pool.filter(p => !p.proxyId || p.proxyId === '__direct__')
    }
    if (skipAlreadyAssigned) {
      pool = pool.filter(p => !p.proxyId || p.proxyId === '__direct__')
    }
    return [...pool].sort((a, b) =>
      (a.profileName || a.profileId).localeCompare(b.profileName || b.profileId, 'zh-Hans-CN', { numeric: true }),
    )
  }, [profiles, profileScope, groupId, skipAlreadyAssigned])

  const unassignedProfiles = useMemo(
    () => profiles.filter(p => !p.proxyId || p.proxyId === '__direct__'),
    [profiles],
  )

  // 分配预览
  const assignments = useMemo<AssignmentRow[]>(() => {
    const n = Math.max(1, Number(perProxyCount) || 1)
    const proxies = candidateProxies
    if (proxies.length === 0) return []
    return candidateProfiles.map((profile, idx) => {
      const proxyIdx = Math.floor(idx / n) % proxies.length
      return { profile, proxy: proxies[proxyIdx] }
    })
  }, [candidateProfiles, candidateProxies, perProxyCount])

  const distribution = useMemo<Map<string, number>>(() => {
    const m = new Map<string, number>()
    for (const row of assignments) {
      m.set(row.proxy.proxyId, (m.get(row.proxy.proxyId) || 0) + 1)
    }
    return m
  }, [assignments])

  const proxyScopeOptions = useMemo(
    () => [
      {
        value: 'unassigned',
        label: `未分配 IP（${unassignedProxies.length}）— 尚未绑定任何环境`,
      },
      { value: 'all', label: `全部代理（${allProxies.length}）` },
      { value: 'filtered', label: `当前筛选后的代理（${filteredProxies.length}）` },
      { value: 'selected', label: `已勾选的代理（${selectedProxyIds.size}）` },
    ],
    [unassignedProxies.length, allProxies.length, filteredProxies.length, selectedProxyIds.size],
  )

  const profileScopeOptions = useMemo(
    () => [
      {
        value: 'unassigned',
        label: `未绑定代理的环境（${unassignedProfiles.length}）`,
      },
      { value: 'all', label: `全部实例（${profiles.length}）` },
      { value: 'group', label: '按分组筛选' },
    ],
    [unassignedProfiles.length, profiles.length],
  )

  const reset = () => {
    setPerProxyCount('1')
    setProxyScope('unassigned')
    setProfileScope('unassigned')
    setGroupId(ALL_GROUPS)
    setSkipAlreadyAssigned(true)
    setIncludeBuiltin(false)
    setAlignTimezoneToProxy(false)
    setProgress(null)
  }

  const handleClose = () => {
    if (running) return
    reset()
    onClose()
  }

  const handleApply = async () => {
    if (assignments.length === 0) {
      toast.error('没有可分配的实例或代理')
      return
    }
    setRunning(true)
    setProgress({ done: 0, total: assignments.length })
    let okCount = 0
    let failCount = 0
    let tzApplied = 0
    // Cache timezone hints per proxy to avoid repeated network health calls.
    const tzByProxy = new Map<string, string>()
    try {
      for (let i = 0; i < assignments.length; i += 1) {
        const { profile, proxy } = assignments[i]
        try {
          let fingerprintArgs = profile.fingerprintArgs || []
          if (alignTimezoneToProxy && proxy.proxyId !== '__direct__' && proxy.proxyId !== '__local__') {
            let tz = tzByProxy.get(proxy.proxyId)
            if (tz === undefined) {
              tz = (await suggestTimezoneFromProxy(proxy.proxyId)) || ''
              tzByProxy.set(proxy.proxyId, tz)
            }
            if (tz) {
              fingerprintArgs = applyTimezoneToFingerprintArgs(fingerprintArgs, tz)
              tzApplied += 1
            }
          }
          await updateBrowserProfile(profile.profileId, {
            profileName: profile.profileName,
            userDataDir: profile.userDataDir,
            coreId: profile.coreId,
            fingerprintArgs,
            proxyId: proxy.proxyId,
            proxyConfig: proxy.proxyConfig,
            launchArgs: profile.launchArgs || [],
            tags: profile.tags || [],
            keywords: profile.keywords || [],
            groupId: profile.groupId || '',
          })
          okCount += 1
        } catch {
          failCount += 1
        }
        setProgress({ done: i + 1, total: assignments.length })
      }
      if (failCount === 0) {
        toast.success(
          tzApplied > 0
            ? `已为 ${okCount} 个实例分配代理，其中 ${tzApplied} 个已写入出口时区`
            : `已为 ${okCount} 个实例分配代理`,
        )
      } else {
        toast.success(`完成：成功 ${okCount} 个，失败 ${failCount} 个${tzApplied > 0 ? `，时区 ${tzApplied}` : ''}`)
      }
      onDone?.()
      reset()
      onClose()
    } finally {
      setRunning(false)
    }
  }

  const groupOptions = useMemo(() => {
    const opts = [
      { value: ALL_GROUPS, label: '全部分组' },
      { value: NO_GROUP, label: '未分组' },
    ]
    for (const g of groups) {
      opts.push({ value: g.groupId, label: g.groupName })
    }
    return opts
  }, [groups])

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title="智能分配代理"
      width="780px"
      footer={
        <>
          <Button variant="secondary" onClick={handleClose} disabled={running}>
            取消
          </Button>
          <Button
            onClick={handleApply}
            loading={running}
            disabled={running || loading || assignments.length === 0 || candidateProxies.length === 0}
          >
            {running && progress
              ? `分配中 ${progress.done}/${progress.total}`
              : `应用分配（${assignments.length}）`}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <p className="text-xs text-[var(--color-text-muted)] bg-[var(--color-bg-secondary)] px-3 py-2 rounded">
          默认优先使用「未分配 IP」绑定「尚未设置代理」的环境，避免重复占用。顺序为每个代理分配 N
          个实例；超出时循环复用。
        </p>

        <div className="grid grid-cols-2 gap-4">
          <FormItem label="每个代理分配多少个实例">
            <Input
              type="number"
              min={1}
              max={9999}
              value={perProxyCount}
              onChange={e => setPerProxyCount(e.target.value)}
              disabled={running}
            />
          </FormItem>

          <FormItem label="代理范围（下拉含协议/地址/绑定状态）">
            <Select
              value={proxyScope}
              onChange={e => setProxyScope(e.target.value as ProxyScope)}
              disabled={running}
              options={proxyScopeOptions}
            />
          </FormItem>

          <FormItem label="实例范围">
            <Select
              value={profileScope}
              onChange={e => setProfileScope(e.target.value as ProfileScope)}
              disabled={running}
              options={profileScopeOptions}
            />
          </FormItem>

          {profileScope === 'group' && (
            <FormItem label="选择分组">
              <Select
                value={groupId}
                onChange={e => setGroupId(e.target.value)}
                disabled={running}
                options={groupOptions}
              />
            </FormItem>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
          <label className="flex items-center gap-2 cursor-pointer">
            <Switch checked={skipAlreadyAssigned} onChange={setSkipAlreadyAssigned} />
            <span className="text-[var(--color-text-muted)]">跳过已设置代理的实例</span>
          </label>
          <label className="flex items-center gap-2 cursor-pointer">
            <Switch checked={includeBuiltin} onChange={setIncludeBuiltin} />
            <span className="text-[var(--color-text-muted)]">包含内置代理（直连/本地）</span>
          </label>
          <label className="flex items-center gap-2 cursor-pointer">
            <Switch checked={alignTimezoneToProxy} onChange={setAlignTimezoneToProxy} />
            <span className="text-[var(--color-text-muted)]" title="根据代理出口国家写入指纹 --timezone，默认关闭">
              按出口自动写时区
            </span>
          </label>
        </div>
        {alignTimezoneToProxy && (
          <p className="text-[11px] text-[var(--color-text-muted)] -mt-2">
            开启后会查询出口国家并写入环境指纹时区；同一代理只查询一次。未识别国家时跳过，不改动原时区。
          </p>
        )}

        {/* 未分配 IP 列表 */}
        <div className="border border-[var(--color-border)] rounded overflow-hidden">
          <div className="px-3 py-2 bg-[var(--color-bg-secondary)] text-xs text-[var(--color-text-muted)] flex items-center justify-between">
            <span>未分配 IP 列表（{unassignedProxies.length}）</span>
            <button
              type="button"
              className="text-[var(--color-primary)] hover:underline disabled:opacity-50"
              disabled={running || unassignedProxies.length === 0}
              onClick={() => setProxyScope('unassigned')}
            >
              使用未分配 IP 作为代理范围
            </button>
          </div>
          {unassignedProxies.length === 0 ? (
            <div className="px-3 py-3 text-xs text-[var(--color-text-muted)]">
              当前没有未绑定环境的代理；可先导入 SOCKS5 节点，或改用「全部代理」。
            </div>
          ) : (
            <div className="max-h-[160px] overflow-y-auto">
              <table className="w-full text-xs">
                <thead className="bg-[var(--color-bg-secondary)]/60 sticky top-0">
                  <tr>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)]">名称</th>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)]">协议</th>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)]">地址</th>
                    <th className="px-3 py-1.5 text-right font-medium text-[var(--color-text-muted)]">延迟</th>
                  </tr>
                </thead>
                <tbody>
                  {unassignedProxies.slice(0, 40).map(proxy => {
                    const { protocol, hostPort } = parseProxyEndpoint(proxy.proxyConfig)
                    const latency =
                      typeof proxy.lastLatencyMs === 'number' && proxy.lastLatencyMs >= 0 && proxy.lastTestOk
                        ? `${proxy.lastLatencyMs}ms`
                        : proxy.lastTestOk === false && proxy.lastTestedAt
                          ? '不通'
                          : '-'
                    return (
                      <tr key={proxy.proxyId} className="border-t border-[var(--color-border)]/40">
                        <td className="px-3 py-1.5 truncate max-w-[160px]">{proxy.proxyName || proxy.proxyId}</td>
                        <td className="px-3 py-1.5">{protocol}</td>
                        <td className="px-3 py-1.5 font-mono truncate max-w-[220px]">{hostPort}</td>
                        <td className="px-3 py-1.5 text-right">{latency}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
              {unassignedProxies.length > 40 && (
                <div className="px-3 py-1.5 text-[10px] text-[var(--color-text-muted)] border-t border-[var(--color-border)]/40">
                  仅显示前 40 条，共 {unassignedProxies.length} 条未分配
                </div>
              )}
            </div>
          )}
        </div>

        {/* 当前参与分配的代理（下拉信息同款明细） */}
        {candidateProxies.length > 0 && (
          <div className="border border-[var(--color-border)] rounded overflow-hidden">
            <div className="px-3 py-2 bg-[var(--color-bg-secondary)] text-xs text-[var(--color-text-muted)]">
              本次将使用的代理（{candidateProxies.length}）
            </div>
            <div className="max-h-[120px] overflow-y-auto px-3 py-2 space-y-1">
              {candidateProxies.slice(0, 20).map(proxy => (
                <div key={proxy.proxyId} className="text-[11px] text-[var(--color-text-secondary)] truncate">
                  {formatProxyOptionLabel(proxy, proxyBindCount.get(proxy.proxyId) || 0)}
                </div>
              ))}
              {candidateProxies.length > 20 && (
                <div className="text-[10px] text-[var(--color-text-muted)]">… 另有 {candidateProxies.length - 20} 条</div>
              )}
            </div>
          </div>
        )}

        <div className="grid grid-cols-3 gap-3 text-sm">
          <SummaryCell label="参与代理" value={candidateProxies.length} />
          <SummaryCell label="参与实例" value={candidateProfiles.length} />
          <SummaryCell
            label="预计分配"
            value={assignments.length}
            tone={assignments.length > 0 ? 'primary' : 'muted'}
          />
        </div>

        {!loading && candidateProxies.length === 0 && (
          <div className="text-xs text-red-500 bg-red-500/10 border border-red-500/30 rounded px-3 py-2">
            当前条件下没有可用代理。请切换代理范围（例如「全部代理」）或先导入 SOCKS5 节点。
          </div>
        )}
        {!loading && candidateProxies.length > 0 && candidateProfiles.length === 0 && (
          <div className="text-xs text-amber-500 bg-amber-500/10 border border-amber-500/30 rounded px-3 py-2">
            当前条件下没有可分配的实例。可切换实例范围为「全部实例」，或关闭「跳过已设置代理」。
          </div>
        )}

        {assignments.length > 0 && (
          <div className="border border-[var(--color-border)] rounded overflow-hidden">
            <div className="px-3 py-2 bg-[var(--color-bg-secondary)] text-xs text-[var(--color-text-muted)] flex items-center justify-between">
              <span>分配预览（前 {Math.min(assignments.length, 12)} 条）</span>
              <span>
                平均每代理 ≈ {(assignments.length / Math.max(1, candidateProxies.length)).toFixed(1)} 个实例
              </span>
            </div>
            <div className="max-h-[260px] overflow-y-auto">
              <table className="w-full text-xs">
                <thead className="bg-[var(--color-bg-secondary)]/60 sticky top-0">
                  <tr>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)] w-10">#</th>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)]">实例</th>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)]">代理</th>
                    <th className="px-3 py-1.5 text-left font-medium text-[var(--color-text-muted)]">协议/地址</th>
                    <th className="px-3 py-1.5 text-right font-medium text-[var(--color-text-muted)] w-20">共分到</th>
                  </tr>
                </thead>
                <tbody>
                  {assignments.slice(0, 12).map((row, idx) => {
                    const { protocol, hostPort } = parseProxyEndpoint(row.proxy.proxyConfig)
                    return (
                      <tr key={row.profile.profileId} className="border-t border-[var(--color-border)]/40">
                        <td className="px-3 py-1.5 text-[var(--color-text-muted)]">{idx + 1}</td>
                        <td className="px-3 py-1.5 text-[var(--color-text-primary)] truncate max-w-[160px]">
                          {row.profile.profileName || row.profile.profileId}
                        </td>
                        <td className="px-3 py-1.5 text-[var(--color-text-primary)] truncate max-w-[160px]">
                          {row.proxy.proxyName || row.proxy.proxyId}
                        </td>
                        <td className="px-3 py-1.5 font-mono text-[var(--color-text-muted)] truncate max-w-[200px]">
                          {protocol} {hostPort}
                        </td>
                        <td className="px-3 py-1.5 text-right text-[var(--color-text-muted)]">
                          {distribution.get(row.proxy.proxyId) || 0}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {loading && (
          <div className="flex items-center justify-center py-4 text-[var(--color-text-muted)] text-sm gap-2">
            <Loader2 className="w-4 h-4 animate-spin" />
            加载实例数据...
          </div>
        )}
      </div>
    </Modal>
  )
}

function SummaryCell({
  label,
  value,
  tone = 'default',
}: {
  label: string
  value: number
  tone?: 'default' | 'primary' | 'muted'
}) {
  const valueClass =
    tone === 'primary'
      ? 'text-[var(--color-primary)]'
      : tone === 'muted'
        ? 'text-[var(--color-text-muted)]'
        : 'text-[var(--color-text-primary)]'
  return (
    <div className="border border-[var(--color-border)] rounded px-3 py-2">
      <div className="text-xs text-[var(--color-text-muted)]">{label}</div>
      <div className={`text-lg font-semibold mt-0.5 ${valueClass}`}>{value}</div>
    </div>
  )
}
