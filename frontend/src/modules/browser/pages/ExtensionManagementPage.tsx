import { useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, FileDown, KeyRound, MoreHorizontal, PackagePlus, Puzzle, Search, ShieldCheck, UploadCloud, Wallet, X } from 'lucide-react'
import { Button, Card, FormItem, Input, Modal, Select, Textarea, toast } from '../../../shared/components'
import { EventsOn } from '../../../wailsjs/runtime/runtime'
import {
  cancelWalletImport,
  executeWalletImport,
  exportWalletImportTemplate,
  fetchBrowserProfiles,
  fetchGlobalExtensions,
  importExtensionToBrowserProfiles,
  importGlobalExtension,
  prepareWalletImport,
  removeExtensionFromBrowserProfiles,
  removeGlobalExtension,
} from '../api'
import type { BrowserProfile, WalletImportPreview, WalletImportProgress, WalletImportResult, WalletImportType } from '../types'
import { resolveActionErrorMessage } from '../utils/actionErrors'

type ExtensionPlatform = 'google' | 'firefox'
type DistributionMode = 'manual' | 'global'

interface ManagedExtension {
  id: string
  name: string
  description: string
  developer: string
  downloadAddress: string
  platform: ExtensionPlatform
  distributionMode: DistributionMode
  profileIds: string[]
  updatedAt: string
}

const STORAGE_KEY = 'boost-browser-managed-extensions'

const platformLabel: Record<ExtensionPlatform, string> = {
  google: 'Google',
  firefox: 'Firefox',
}

const modeLabel: Record<DistributionMode, string> = {
  manual: '手动分配',
  global: '全局使用',
}

const defaultExtensions: ManagedExtension[] = []

const walletLabels: Record<WalletImportType, string> = {
  rabby: 'Rabby',
  jupiter: 'Jupiter',
  metamask: 'MetaMask',
}

function loadExtensions(): ManagedExtension[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return defaultExtensions
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed : defaultExtensions
  } catch {
    return defaultExtensions
  }
}

function saveExtensions(items: ManagedExtension[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(items))
}

function extensionAddressKey(value: string) {
  return value.trim().toLowerCase()
}

function extensionIcon(name: string) {
  const text = (name || 'E').trim().slice(0, 1).toUpperCase()
  const palettes = [
    'from-blue-500 to-cyan-400',
    'from-purple-500 to-pink-400',
    'from-emerald-500 to-lime-400',
    'from-orange-500 to-amber-400',
  ]
  const idx = Math.abs(name.split('').reduce((acc, ch) => acc + ch.charCodeAt(0), 0)) % palettes.length
  return (
    <div className={`w-10 h-10 rounded-xl bg-gradient-to-br ${palettes[idx]} text-white flex items-center justify-center font-bold shadow-sm`}>
      {text}
    </div>
  )
}

function matchesProfileSearch(profile: BrowserProfile, query: string) {
  const tokens = query
    .trim()
    .toLowerCase()
    .split(/[\s,，;；]+/)
    .filter(Boolean)
  if (tokens.length === 0) return true

  const name = (profile.profileName || '').toLowerCase()
  const profileId = (profile.profileId || '').toLowerCase()
  const launchCode = (profile.launchCode || '').toLowerCase()
  const nameNumbers = name.match(/\d+/g) || []

  return tokens.some(token => {
    if (/^\d+$/.test(token)) {
      const normalized = String(Number(token))
      return nameNumbers.some(value => String(Number(value)) === normalized) || launchCode === token
    }
    return name.includes(token) || profileId.includes(token) || launchCode.includes(token)
  })
}

export function ExtensionManagementPage() {
  const [extensions, setExtensions] = useState<ManagedExtension[]>(() => loadExtensions())
  const [profiles, setProfiles] = useState<BrowserProfile[]>([])
  const [appliedGlobalAddresses, setAppliedGlobalAddresses] = useState<Set<string>>(() => new Set())
  const [activePlatform, setActivePlatform] = useState<'all' | ExtensionPlatform>('all')
  const [keyword, setKeyword] = useState('')
  const [profileKeyword, setProfileKeyword] = useState('')
  const [uploadOpen, setUploadOpen] = useState(false)
  const [configOpen, setConfigOpen] = useState(false)
  const [rabbyOpen, setRabbyOpen] = useState(false)
  const [walletType, setWalletType] = useState<WalletImportType>('rabby')
  const [rabbyPreparing, setRabbyPreparing] = useState(false)
  const [rabbyExecuting, setRabbyExecuting] = useState(false)
  const [rabbyPreview, setRabbyPreview] = useState<WalletImportPreview | null>(null)
  const [rabbyPassword, setRabbyPassword] = useState('')
  const [rabbyConfirmed, setRabbyConfirmed] = useState(false)
  const [rabbyProgress, setRabbyProgress] = useState<WalletImportProgress | null>(null)
  const [rabbyResult, setRabbyResult] = useState<WalletImportResult | null>(null)
  const [currentId, setCurrentId] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const initializedRef = useRef(false)
  const [form, setForm] = useState({
    name: '',
    description: '',
    developer: '',
    downloadAddress: '',
    platform: 'google' as ExtensionPlatform,
    distributionMode: 'manual' as DistributionMode,
    profileIds: [] as string[],
  })

  useEffect(() => {
    if (initializedRef.current) return
    initializedRef.current = true
    const initialize = async () => {
      const [loadedProfiles, globalPolicies] = await Promise.all([
        fetchBrowserProfiles().catch(() => []),
        fetchGlobalExtensions().catch(() => []),
      ])
      setProfiles(loadedProfiles)
      const applied = new Set(
        globalPolicies
          .filter(item => item.installed)
          .map(item => extensionAddressKey(item.downloadAddress)),
      )

      // Migrate items saved by older clients. Previously “global use” was only
      // localStorage metadata, so register each missing item with the backend.
      const missing = loadExtensions().filter(item =>
        item.platform === 'google' &&
        item.distributionMode === 'global' &&
        !applied.has(extensionAddressKey(item.downloadAddress)),
      )
      let migratedCount = 0
      for (const item of missing) {
        try {
          await importGlobalExtension(item.downloadAddress)
          applied.add(extensionAddressKey(item.downloadAddress))
          migratedCount += 1
        } catch (error: any) {
          toast.error(`${item.name} 全局同步失败：${error?.message || '请检查扩展下载地址'}`)
        }
      }
      setAppliedGlobalAddresses(new Set(applied))
      if (migratedCount > 0) {
        toast.success('旧版全局扩展配置已同步到后端，重启运行中的浏览器后生效')
      }
    }
    initialize().catch(() => {
      setProfiles([])
      setAppliedGlobalAddresses(new Set())
    })
  }, [])

  useEffect(() => {
    saveExtensions(extensions)
  }, [extensions])

  useEffect(() => EventsOn('wallet-import:progress', (progress: WalletImportProgress) => {
    setRabbyProgress(current => progress.walletType === walletType ? progress : current)
  }), [walletType])

  const filtered = useMemo(() => {
    const q = keyword.trim().toLowerCase()
    return extensions.filter(item => {
      if (activePlatform !== 'all' && item.platform !== activePlatform) return false
      if (!q) return true
      return [item.name, item.description, item.developer, item.downloadAddress]
        .some(value => value.toLowerCase().includes(q))
    })
  }, [extensions, activePlatform, keyword])

  const currentExtension = extensions.find(item => item.id === currentId) || null
  const selectedCount = form.distributionMode === 'global' ? profiles.length : form.profileIds.length
  const filteredProfiles = useMemo(
    () => profiles.filter(profile => matchesProfileSearch(profile, profileKeyword)),
    [profiles, profileKeyword],
  )
  const selectedProfileIds = useMemo(() => new Set(form.profileIds), [form.profileIds])
  const allProfilesSelected = profiles.length > 0 && profiles.every(profile => selectedProfileIds.has(profile.profileId))
  const allFilteredProfilesSelected = filteredProfiles.length > 0 && filteredProfiles.every(profile => selectedProfileIds.has(profile.profileId))

  const resetForm = () => {
    setProfileKeyword('')
    setForm({
      name: '',
      description: '',
      developer: '',
      downloadAddress: '',
      platform: 'google',
      distributionMode: 'manual',
      profileIds: [],
    })
  }

  const openUpload = () => {
    resetForm()
    setCurrentId(null)
    setUploadOpen(true)
  }

  const openConfig = (item: ManagedExtension) => {
    setProfileKeyword('')
    setCurrentId(item.id)
    setForm({
      name: item.name,
      description: item.description,
      developer: item.developer,
      downloadAddress: item.downloadAddress,
      platform: item.platform,
      distributionMode: item.distributionMode,
      profileIds: item.profileIds || [],
    })
    setConfigOpen(true)
  }

  const toggleProfile = (profileId: string) => {
    setForm(prev => {
      const exists = prev.profileIds.includes(profileId)
      return {
        ...prev,
        profileIds: exists
          ? prev.profileIds.filter(id => id !== profileId)
          : [...prev.profileIds, profileId],
      }
    })
  }

  const setProfileSelection = (profileIds: string[], selected: boolean) => {
    setForm(prev => {
      const next = new Set(prev.profileIds)
      profileIds.forEach(profileId => selected ? next.add(profileId) : next.delete(profileId))
      return { ...prev, profileIds: Array.from(next) }
    })
  }

  const submitExtension = async () => {
    const name = form.name.trim()
    const downloadAddress = form.downloadAddress.trim()
    if (!name) {
      toast.warning('请输入扩展名称')
      return
    }
    if (!downloadAddress) {
      toast.warning('请输入扩展下载地址或扩展 ID')
      return
    }
    setSubmitting(true)
    try {
      if (form.distributionMode === 'global' && form.platform !== 'google') {
        toast.warning('全局自动安装当前仅支持 Google/Chrome 扩展')
        return
      }
      if (currentExtension?.distributionMode === 'global' && (
        form.distributionMode !== 'global' ||
        extensionAddressKey(currentExtension.downloadAddress) !== extensionAddressKey(downloadAddress)
      )) {
        await removeGlobalExtension(currentExtension.downloadAddress)
        setAppliedGlobalAddresses(prev => {
          const next = new Set(prev)
          next.delete(extensionAddressKey(currentExtension.downloadAddress))
          return next
        })
      }
      let globalMessage = ''
      if (form.distributionMode === 'global') {
        const result = await importGlobalExtension(downloadAddress)
        globalMessage = result?.message || ''
        setAppliedGlobalAddresses(prev => new Set(prev).add(extensionAddressKey(downloadAddress)))
      }
      const nextItem: ManagedExtension = {
        id: currentId || `ext-${Date.now()}`,
        name,
        description: form.description.trim() || '暂无描述',
        developer: form.developer.trim() || '未知开发者',
        downloadAddress,
        platform: form.platform,
        distributionMode: form.distributionMode,
        profileIds: form.distributionMode === 'global' ? [] : form.profileIds,
        updatedAt: new Date().toISOString(),
      }
      setExtensions(prev => currentId
        ? prev.map(item => item.id === currentId ? nextItem : item)
        : [nextItem, ...prev]
      )
      toast.success(globalMessage || (currentId ? '扩展配置已保存' : '扩展已加入列表'))
      setUploadOpen(false)
      setConfigOpen(false)
      resetForm()
      setCurrentId(null)
    } finally {
      setSubmitting(false)
    }
  }

  const distributeExtension = async (item: ManagedExtension) => {
    const targetIds = item.distributionMode === 'global'
      ? profiles.map(profile => profile.profileId)
      : item.profileIds
    if (item.distributionMode === 'manual' && targetIds.length === 0) {
      toast.warning('请先在配置里选择要分配的实例')
      return
    }
    setSubmitting(true)
    try {
      const result = item.distributionMode === 'global'
        ? await importGlobalExtension(item.downloadAddress)
        : await importExtensionToBrowserProfiles(targetIds, item.downloadAddress)
      toast.success(result?.message || `已分配到 ${targetIds.length} 个实例`)
      if (item.distributionMode === 'global') {
        setAppliedGlobalAddresses(prev => new Set(prev).add(extensionAddressKey(item.downloadAddress)))
      }
      setExtensions(prev => prev.map(ext => ext.id === item.id ? { ...ext, updatedAt: new Date().toISOString() } : ext))
    } catch (error: any) {
      toast.error(error?.message || '扩展分配失败')
    } finally {
      setSubmitting(false)
    }
  }

  const removeExtension = async (item: ManagedExtension) => {
    const targetIds = item.distributionMode === 'global'
      ? profiles.map(profile => profile.profileId)
      : item.profileIds
    setSubmitting(true)
    try {
      if (item.distributionMode === 'global') {
        const result = await removeGlobalExtension(item.downloadAddress)
        toast.success(result?.message || '全局扩展已移除')
        setAppliedGlobalAddresses(prev => {
          const next = new Set(prev)
          next.delete(extensionAddressKey(item.downloadAddress))
          return next
        })
      } else if (targetIds.length > 0) {
        const result = await removeExtensionFromBrowserProfiles(targetIds, item.downloadAddress)
        toast.success(result?.message || '扩展已解绑')
      } else {
        toast.success('扩展已从列表移除')
      }
      setExtensions(prev => prev.filter(ext => ext.id !== item.id))
      if (currentId === item.id) {
        setConfigOpen(false)
        setCurrentId(null)
      }
    } catch (error: any) {
      toast.error(error?.message || '移除扩展失败')
    } finally {
      setSubmitting(false)
    }
  }

  const openRabbyImport = () => {
    setRabbyOpen(true)
    setRabbyPassword('')
    setRabbyConfirmed(false)
    setRabbyProgress(null)
    setRabbyResult(null)
  }

  const closeRabbyImport = () => {
    if (rabbyExecuting) return
    if (rabbyPreview?.sessionId && !rabbyResult) {
      void cancelWalletImport(rabbyPreview.sessionId)
    }
    setRabbyOpen(false)
    setRabbyPreview(null)
    setRabbyPassword('')
    setRabbyConfirmed(false)
    setRabbyProgress(null)
    setRabbyResult(null)
  }

  const changeWalletType = async (nextType: WalletImportType) => {
    if (nextType === walletType || rabbyExecuting) return
    if (rabbyPreview?.sessionId && !rabbyResult) await cancelWalletImport(rabbyPreview.sessionId)
    setWalletType(nextType)
    setRabbyPreview(null)
    setRabbyPassword('')
    setRabbyConfirmed(false)
    setRabbyProgress(null)
    setRabbyResult(null)
  }

  const selectRabbyImportFile = async () => {
    setRabbyPreparing(true)
    try {
      if (rabbyPreview?.sessionId && !rabbyResult) {
        await cancelWalletImport(rabbyPreview.sessionId)
      }
      const preview = await prepareWalletImport(walletType)
      if (preview?.cancelled) return
      setRabbyPreview(preview)
      setRabbyPassword('')
      setRabbyConfirmed(false)
      setRabbyProgress(null)
      setRabbyResult(null)
      toast.success(preview.message || `已读取 ${preview.rows.length} 条映射`)
    } catch (error: any) {
      toast.error(resolveActionErrorMessage(error, `读取 ${walletLabels[walletType]} 导入文件失败`))
    } finally {
      setRabbyPreparing(false)
    }
  }

  const downloadRabbyTemplate = async () => {
    try {
      const response = await exportWalletImportTemplate(walletType)
      if (!response?.cancelled) toast.success(response?.message || `${walletLabels[walletType]} CSV 模板已生成`)
    } catch (error: any) {
      toast.error(resolveActionErrorMessage(error, `导出 ${walletLabels[walletType]} 模板失败`))
    }
  }

  const startRabbyImport = async () => {
    if (!rabbyPreview?.sessionId) {
      toast.warning('请先选择 CSV 或 TXT 文件')
      return
    }
    if (rabbyPassword.length < 8) {
      toast.warning(`${walletLabels[walletType]} 本地解锁密码至少需要 8 个字符`)
      return
    }
    if (!rabbyConfirmed) {
      toast.warning('请确认已备份文件并理解不可覆盖限制')
      return
    }

    const password = rabbyPassword
    setRabbyPassword('')
    setRabbyExecuting(true)
    setRabbyProgress({
      completed: 0,
      total: rabbyPreview.rows.length,
      profileId: '',
      profileName: '',
      status: 'running',
      message: '准备开始导入',
    })
    try {
      const result = await executeWalletImport({ sessionId: rabbyPreview.sessionId, walletType, password })
      setRabbyResult(result)
      if (result.failed > 0) toast.warning(result.message)
      else toast.success(result.message)
    } catch (error: any) {
      toast.error(resolveActionErrorMessage(error, `${walletLabels[walletType]} 批量导入失败`))
      setRabbyPreview(null)
      setRabbyConfirmed(false)
    } finally {
      setRabbyExecuting(false)
    }
  }

  const modalTitle = currentId ? '配置扩展' : '上传扩展'

  return (
    <div className="flex flex-col h-full bg-[var(--color-bg-layout)]">
      <div className="flex items-center justify-between px-6 py-4 border-b border-[var(--color-border-default)] bg-[var(--color-bg-surface)]">
        <div className="flex items-center gap-3">
          <Puzzle className="w-5 h-5 text-[var(--color-accent)]" />
          <div>
            <h1 className="text-lg font-semibold text-[var(--color-text-primary)]">扩展管理</h1>
            <p className="text-xs text-[var(--color-text-muted)] mt-0.5">仅添加你信任的扩展，并分配到指定浏览器实例</p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={openRabbyImport}><Wallet className="w-4 h-4" />钱包批量导入</Button>
          <Button onClick={openUpload}><UploadCloud className="w-4 h-4" />上传扩展</Button>
          <Button variant="secondary" onClick={() => toast.info('扩展中心入口已预留，可继续接入在线扩展市场')}><PackagePlus className="w-4 h-4" />扩展中心</Button>
        </div>
      </div>

      <div className="px-6 py-3 bg-blue-50 dark:bg-blue-900/20 text-blue-700 dark:text-blue-300 text-sm border-b border-[var(--color-border-default)]">
        安全提示：扩展可读取网页内容、剪贴板或钱包页面数据。只安装来自可信来源的扩展；已启动实例需要重启后生效。
      </div>

      <div className="p-6 space-y-4 overflow-auto">
        <Card padding="none" className="shadow-sm">
          <div className="flex items-center justify-between gap-4 px-5 py-4 border-b border-[var(--color-border-muted)]">
            <div className="flex items-center gap-2">
              {[
                { key: 'all', label: '全部' },
                { key: 'google', label: 'Google' },
                { key: 'firefox', label: 'Firefox' },
              ].map(tab => (
                <button
                  key={tab.key}
                  onClick={() => setActivePlatform(tab.key as any)}
                  className={`px-3 py-1.5 rounded-lg text-sm font-medium transition-colors ${
                    activePlatform === tab.key
                      ? 'bg-[var(--color-accent)] text-[var(--color-text-inverse)]'
                      : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-accent-muted)]'
                  }`}
                >
                  {tab.label}
                </button>
              ))}
            </div>
            <div className="relative w-72">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-[var(--color-text-muted)]" />
              <Input value={keyword} onChange={e => setKeyword(e.target.value)} placeholder="请输入扩展名称" className="pl-9" />
            </div>
          </div>

          <table className="w-full">
            <thead className="bg-[var(--color-bg-muted)] border-b border-[var(--color-border-muted)]">
              <tr className="text-left text-xs font-semibold text-[var(--color-text-muted)]">
                <th className="px-5 py-3">扩展</th>
                <th className="px-5 py-3 w-48">开发者</th>
                <th className="px-5 py-3 w-36">分配方式</th>
                <th className="px-5 py-3 w-32">平台</th>
                <th className="px-5 py-3 w-48 text-right">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--color-border-muted)]">
              {filtered.length === 0 ? (
                <tr>
                  <td colSpan={5} className="px-5 py-14 text-center text-sm text-[var(--color-text-muted)]">暂无扩展</td>
                </tr>
              ) : filtered.map(item => {
                const globalApplied = item.distributionMode === 'global' && appliedGlobalAddresses.has(extensionAddressKey(item.downloadAddress))
                const count = item.distributionMode === 'global' ? (globalApplied ? profiles.length : 0) : item.profileIds.length
                return (
                  <tr key={item.id} className="hover:bg-[var(--color-bg-hover)] transition-colors">
                    <td className="px-5 py-4">
                      <div className="flex items-center gap-3">
                        {extensionIcon(item.name)}
                        <div className="min-w-0">
                          <div className="font-medium text-[var(--color-text-primary)] truncate">{item.name}</div>
                          <div className="text-xs text-[var(--color-text-muted)] truncate max-w-xl">{item.description}</div>
                        </div>
                      </div>
                    </td>
                    <td className="px-5 py-4 text-sm text-[var(--color-text-secondary)]">{item.developer}</td>
                    <td className="px-5 py-4">
                      <span className={`inline-flex items-center rounded-full px-2.5 py-1 text-xs font-medium ${
                        item.distributionMode === 'global'
                          ? 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300'
                          : 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
                      }`}>
                        {modeLabel[item.distributionMode]}{count > 0 ? ` · ${count}` : item.distributionMode === 'global' ? ' · 待同步' : ''}
                      </span>
                    </td>
                    <td className="px-5 py-4 text-sm text-[var(--color-text-secondary)]">{platformLabel[item.platform]}</td>
                    <td className="px-5 py-4">
                      <div className="flex items-center justify-end gap-2">
                        <Button size="sm" variant="secondary" onClick={() => openConfig(item)}>配置</Button>
                        <Button size="sm" onClick={() => distributeExtension(item)} disabled={submitting}>分配</Button>
                        <button onClick={() => removeExtension(item)} className="p-2 rounded-lg text-[var(--color-text-muted)] hover:bg-red-50 hover:text-red-600" title="移除" disabled={submitting}>
                          <MoreHorizontal className="w-4 h-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </Card>
      </div>

      <Modal
        open={uploadOpen || configOpen}
        onClose={() => { if (!submitting) { setUploadOpen(false); setConfigOpen(false) } }}
        title={modalTitle}
        width="760px"
        footer={
          <>
            <Button variant="secondary" onClick={() => { setUploadOpen(false); setConfigOpen(false) }} disabled={submitting}>取消</Button>
            <Button onClick={submitExtension} loading={submitting}>保存</Button>
          </>
        }
      >
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <FormItem label="扩展名称" required>
              <Input value={form.name} onChange={e => setForm(prev => ({ ...prev, name: e.target.value }))} placeholder="例如 MetaMask" />
            </FormItem>
            <FormItem label="开发者">
              <Input value={form.developer} onChange={e => setForm(prev => ({ ...prev, developer: e.target.value }))} placeholder="开发者/团队/邮箱" />
            </FormItem>
          </div>
          <FormItem label="扩展说明">
            <Input value={form.description} onChange={e => setForm(prev => ({ ...prev, description: e.target.value }))} placeholder="显示在扩展列表中的简短描述" />
          </FormItem>
          <FormItem label="扩展程序下载地址" required hint="支持 Chrome Web Store 详情页、32位扩展ID、HTTPS .crx/.zip 下载地址；请只使用可信来源">
            <Textarea rows={3} value={form.downloadAddress} onChange={e => setForm(prev => ({ ...prev, downloadAddress: e.target.value }))} placeholder="例如：https://chromewebstore.google.com/detail/.../扩展ID 或 32位扩展ID" />
          </FormItem>
          <div className="grid grid-cols-2 gap-4">
            <FormItem label="平台">
              <Select value={form.platform} onChange={e => setForm(prev => ({ ...prev, platform: e.target.value as ExtensionPlatform }))} options={[{ value: 'google', label: 'Google' }, { value: 'firefox', label: 'Firefox' }]} />
            </FormItem>
            <FormItem label="分配方式">
              <Select value={form.distributionMode} onChange={e => setForm(prev => ({ ...prev, distributionMode: e.target.value as DistributionMode }))} options={[{ value: 'manual', label: '手动分配' }, { value: 'global', label: '全局使用' }]} />
            </FormItem>
          </div>

          {form.distributionMode === 'manual' ? (
            <div className="rounded-xl border border-[var(--color-border-default)] overflow-hidden">
              <div className="flex items-center justify-between px-4 py-3 bg-[var(--color-bg-muted)] border-b border-[var(--color-border-muted)]">
                <div className="text-sm font-medium text-[var(--color-text-primary)]">选择分配实例</div>
                <div className="text-xs text-[var(--color-text-muted)]">已选 {selectedCount} / {profiles.length} 个</div>
              </div>
              <div className="flex items-center gap-2 px-3 py-2.5 border-b border-[var(--color-border-muted)] bg-[var(--color-bg-surface)]">
                <div className="relative flex-1 min-w-0">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-[var(--color-text-muted)]" />
                  <Input
                    value={profileKeyword}
                    onChange={event => setProfileKeyword(event.target.value)}
                    placeholder="搜索编号/名称，多个编号用逗号分隔"
                    className="pl-9"
                  />
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() => setProfileSelection(filteredProfiles.map(profile => profile.profileId), !allFilteredProfilesSelected)}
                  disabled={filteredProfiles.length === 0}
                >
                  {allFilteredProfilesSelected ? '取消结果' : `选择结果${profileKeyword.trim() ? ` (${filteredProfiles.length})` : ''}`}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() => setProfileSelection(profiles.map(profile => profile.profileId), !allProfilesSelected)}
                  disabled={profiles.length === 0}
                >
                  {allProfilesSelected ? '取消全选' : '全选'}
                </Button>
              </div>
              <div className="max-h-56 overflow-auto divide-y divide-[var(--color-border-muted)]">
                {profiles.length === 0 ? (
                  <div className="px-4 py-8 text-sm text-center text-[var(--color-text-muted)]">暂无实例</div>
                ) : filteredProfiles.length === 0 ? (
                  <div className="px-4 py-8 text-sm text-center text-[var(--color-text-muted)]">未找到匹配的实例</div>
                ) : filteredProfiles.map(profile => (
                  <label key={profile.profileId} className="flex items-center justify-between px-4 py-2.5 cursor-pointer hover:bg-[var(--color-bg-hover)]">
                    <span className="flex items-center gap-2 text-sm text-[var(--color-text-primary)]">
                      <input type="checkbox" className="accent-[var(--color-accent)]" checked={selectedProfileIds.has(profile.profileId)} onChange={() => toggleProfile(profile.profileId)} />
                      {profile.profileName || profile.profileId}
                    </span>
                    <span className={`text-xs ${profile.running ? 'text-green-600' : 'text-[var(--color-text-muted)]'}`}>{profile.running ? '运行中' : '未启动'}</span>
                  </label>
                ))}
              </div>
            </div>
          ) : (
            <div className="flex items-center gap-2 rounded-xl border border-green-200 bg-green-50 dark:bg-green-900/20 dark:border-green-900 px-4 py-3 text-sm text-green-700 dark:text-green-300">
              <ShieldCheck className="w-4 h-4" />
              保存后会自动安装到当前全部 {profiles.length} 个实例；后续新建实例及每次启动也会自动继承，无需再次分配。
            </div>
          )}

          {currentExtension && (
            <div className="flex items-center justify-between rounded-lg bg-[var(--color-bg-muted)] px-3 py-2 text-xs text-[var(--color-text-muted)]">
              <span>上次更新：{new Date(currentExtension.updatedAt).toLocaleString()}</span>
              <button onClick={() => removeExtension(currentExtension)} className="inline-flex items-center gap-1 text-red-500 hover:text-red-600" disabled={submitting}>
                <X className="w-3 h-3" />移除扩展
              </button>
            </div>
          )}
        </div>
      </Modal>

      <Modal
        open={rabbyOpen}
        onClose={closeRabbyImport}
        title="钱包批量导入"
        width="900px"
        closable={!rabbyExecuting}
        footer={rabbyResult ? (
          <Button onClick={closeRabbyImport}>完成</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={closeRabbyImport} disabled={rabbyExecuting}>取消</Button>
            <Button variant="secondary" onClick={selectRabbyImportFile} loading={rabbyPreparing} disabled={rabbyExecuting}>选择 CSV/TXT</Button>
            <Button onClick={startRabbyImport} loading={rabbyExecuting} disabled={!rabbyPreview}>开始导入</Button>
          </>
        )}
      >
        <div className="space-y-4">
          <div className="grid grid-cols-3 gap-2">
            {(Object.keys(walletLabels) as WalletImportType[]).map(type => (
              <button
                key={type}
                type="button"
                onClick={() => void changeWalletType(type)}
                disabled={rabbyExecuting}
                className={`rounded-xl border px-4 py-3 text-sm font-semibold transition-colors disabled:opacity-50 ${walletType === type ? 'border-[var(--color-accent)] bg-blue-50 text-blue-700 dark:bg-blue-900/20 dark:text-blue-300' : 'border-[var(--color-border-default)] text-[var(--color-text-secondary)] hover:border-[var(--color-accent)]'}`}
              >
                {walletLabels[type]}
              </button>
            ))}
          </div>

          <div className="rounded-xl border border-amber-200 bg-amber-50 dark:bg-amber-900/20 dark:border-amber-900 px-4 py-3 text-sm text-amber-800 dark:text-amber-300">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 mt-0.5 flex-shrink-0" />
              <div>
                <div className="font-medium">无需预先启动环境；客户端会自动启动模板指定的环境，导入后保持窗口打开</div>
                <div className="mt-1 text-xs leading-5">导入前会交叉核验环境编号、Profile ID、名称和数据文件夹 ID，仅导入未初始化的官方 {walletLabels[walletType]} 扩展。助记词和自定义密码不写入数据库或日志，也不提供导出接口。Jupiter 为闭源第三方扩展，BrowserStudio 无法审计或保证其内部行为。</div>
              </div>
            </div>
          </div>

          <div className="flex items-center justify-between gap-4 rounded-xl border border-[var(--color-border-default)] px-4 py-3">
            <div>
              <div className="text-sm font-medium text-[var(--color-text-primary)]">1. 准备环境映射文件</div>
              <div className="text-xs text-[var(--color-text-muted)] mt-1">请使用下载的 CSV 模板；只填写 mnemonic 列，不要修改环境编号、Profile ID、名称和 storage_id</div>
            </div>
            <Button variant="secondary" size="sm" onClick={downloadRabbyTemplate} disabled={rabbyExecuting}><FileDown className="w-4 h-4" />下载 CSV 模板</Button>
          </div>

          {rabbyPreview ? (
            <div className="rounded-xl border border-[var(--color-border-default)] overflow-hidden">
              <div className="flex items-center justify-between px-4 py-3 bg-[var(--color-bg-muted)] border-b border-[var(--color-border-muted)]">
                <div>
                  <div className="text-sm font-medium text-[var(--color-text-primary)]">{rabbyPreview.fileName}</div>
                  <div className="text-xs text-[var(--color-text-muted)] mt-0.5">已校验 {rabbyPreview.rows.length} 条；助记词只保存在后端临时内存，15 分钟后自动失效</div>
                </div>
                <span className="text-xs text-[var(--color-text-muted)]">四项环境信息已交叉核验</span>
              </div>
              <div className="max-h-52 overflow-auto">
                <table className="w-full text-sm">
                  <thead className="sticky top-0 bg-[var(--color-bg-surface)] text-xs text-[var(--color-text-muted)]">
                    <tr><th className="px-4 py-2 text-left">行</th><th className="px-4 py-2 text-left">编号</th><th className="px-4 py-2 text-left">环境</th><th className="px-4 py-2 text-left">数据文件夹 ID</th><th className="px-4 py-2 text-left">词数</th><th className="px-4 py-2 text-right">状态</th></tr>
                  </thead>
                  <tbody className="divide-y divide-[var(--color-border-muted)]">
                    {rabbyPreview.rows.map(row => (
                      <tr key={`${row.rowNumber}-${row.profileId}`}>
                        <td className="px-4 py-2 text-[var(--color-text-muted)]">{row.rowNumber}</td>
                        <td className="px-4 py-2 font-semibold text-[var(--color-text-primary)]">#{row.environmentNumber}</td>
                        <td className="px-4 py-2 text-[var(--color-text-primary)]"><div>{row.profileName || row.profileId}</div><div className="font-mono text-[11px] text-[var(--color-text-muted)] mt-0.5">{row.profileId}</div></td>
                        <td className="px-4 py-2 font-mono text-xs text-[var(--color-text-muted)]">{row.storageId}</td>
                        <td className="px-4 py-2 text-[var(--color-text-secondary)]">{row.wordCount}</td>
                        <td className={`px-4 py-2 text-right ${row.running ? 'text-green-600' : 'text-blue-600'}`}>{row.running ? '已启动，将直接导入' : '待自动启动'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : (
            <button onClick={selectRabbyImportFile} disabled={rabbyPreparing} className="w-full rounded-xl border-2 border-dashed border-[var(--color-border-default)] px-4 py-10 text-sm text-[var(--color-text-muted)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:opacity-50">
              {rabbyPreparing ? '正在安全读取并校验…' : '点击选择已填写的 CSV 或 TXT 文件'}
            </button>
          )}

          {rabbyPreview && !rabbyResult && (
            <div className="space-y-3">
              <FormItem label={`2. 设置 ${walletLabels[walletType]} 本地解锁密码`} required hint={`本批次所有 ${walletLabels[walletType]} 使用同一个本地解锁密码；密码仅用于本次导入，不保存到数据库或日志`}>
                <div className="relative">
                  <KeyRound className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-[var(--color-text-muted)]" />
                  <Input type="password" autoComplete="new-password" value={rabbyPassword} onChange={event => setRabbyPassword(event.target.value)} placeholder="至少 8 个字符" className="pl-9" disabled={rabbyExecuting} />
                </div>
              </FormItem>
              <label className="flex items-start gap-2 text-sm text-[var(--color-text-secondary)] cursor-pointer">
                <input type="checkbox" className="mt-0.5 accent-[var(--color-accent)]" checked={rabbyConfirmed} onChange={event => setRabbyConfirmed(event.target.checked)} disabled={rabbyExecuting} />
                <span>我确认：文件中的助记词已安全备份；未修改模板内的环境映射信息；目标 {walletLabels[walletType]} 均未初始化。客户端将自动启动环境，导入完成后保持窗口打开。</span>
              </label>
            </div>
          )}

          {rabbyProgress && !rabbyResult && (
            <div className="rounded-xl bg-[var(--color-bg-muted)] px-4 py-3">
              <div className="flex items-center justify-between text-sm"><span className="text-[var(--color-text-primary)]">{rabbyProgress.profileName || '准备中'}</span><span className="text-[var(--color-text-muted)]">{rabbyProgress.completed}/{rabbyProgress.total}</span></div>
              <div className="h-2 rounded-full bg-[var(--color-border-muted)] mt-2 overflow-hidden"><div className="h-full bg-[var(--color-accent)] transition-all" style={{ width: `${rabbyProgress.total ? Math.round(rabbyProgress.completed / rabbyProgress.total * 100) : 0}%` }} /></div>
              <div className="text-xs text-[var(--color-text-muted)] mt-2">{rabbyProgress.message}</div>
            </div>
          )}

          {rabbyResult && (
            <div className="rounded-xl border border-[var(--color-border-default)] overflow-hidden">
              <div className="px-4 py-3 bg-[var(--color-bg-muted)] text-sm font-medium text-[var(--color-text-primary)]">{rabbyResult.message}</div>
              <div className="max-h-64 overflow-auto divide-y divide-[var(--color-border-muted)]">
                {rabbyResult.rows.map(row => (
                  <div key={`${row.rowNumber}-${row.profileId}`} className="grid grid-cols-[1fr_110px_2fr] gap-3 px-4 py-3 text-sm">
                    <div><div className="text-[var(--color-text-primary)]">{row.profileName}</div><div className="font-mono text-xs text-[var(--color-text-muted)] mt-0.5">{row.profileId}</div></div>
                    <div className={row.status === 'success' ? 'text-green-600' : 'text-red-600'}>{row.status === 'success' ? '成功' : '失败'}</div>
                    <div className="min-w-0"><div className="font-mono text-xs text-[var(--color-text-secondary)] break-all">{row.address || '-'}</div><div className="text-xs text-[var(--color-text-muted)] mt-0.5">{row.message}</div></div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </Modal>
    </div>
  )
}
