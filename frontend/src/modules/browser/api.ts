import type { BrowserProfile, BrowserProfileInput, BrowserTab, BrowserSettings, BrowserCore, BrowserCoreInput, BrowserCoreValidateResult, BrowserProxy, BrowserCoreExtended, CookieInfo, SnapshotInfo, BrowserBookmark, BrowserGroup, BrowserGroupInput, BrowserGroupWithCount, ProxyIPHealthResult, ExtensionImportResult, GlobalManagedExtension, RabbyWalletBatchExecuteInput, RabbyWalletImportPreview, RabbyWalletImportResult, WalletBatchExecuteInput, WalletImportPreview, WalletImportResult, WalletImportType } from './types'

const getBindings = async () => {
  try {
    return await import('../../wailsjs/go/main/App')
  } catch {
    return null
  }
}

let mockProfiles: BrowserProfile[] = [
  {
    profileId: 'mock-1',
    profileName: '默认指纹配置',
    userDataDir: 'data/default',
    coreId: 'default',
    fingerprintArgs: ['--fingerprint-brand=Chrome', '--fingerprint-platform=windows'],
    proxyId: '',
    proxyConfig: '',
    launchArgs: ['--disable-features=Translate'],
    tags: ['默认'],
    keywords: [],
    running: false,
    debugPort: 0,
    debugReady: false,
    pid: 0,
    runtimeWarning: '',
    lastError: '',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  },
]

let mockCores: BrowserCore[] = []

let mockProxies: BrowserProxy[] = []

// ============================================================================
// Profile API
// ============================================================================

/**
 * Explicitly re-scan the system for still-running browser instances and take
 * them over (watchdog-restart recovery / manual refresh). The ordinary profile
 * list deliberately does NOT do this on every load — that used to trigger a
 * full PowerShell process scan on every focus/visibility/lifecycle event.
 */
export async function refreshBrowserRuntimeState(): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.RefreshBrowserRuntimeState) {
    try {
      return (await bindings.RefreshBrowserRuntimeState()) === true
    } catch {
      return false
    }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.RefreshBrowserRuntimeState) {
    try {
      return (await goApp.RefreshBrowserRuntimeState()) === true
    } catch {
      return false
    }
  }
  return false
}

export async function fetchBrowserProfiles(): Promise<BrowserProfile[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileList) {
    return (await bindings.BrowserProfileList()) || []
  }
  return mockProfiles
}

export async function fetchBrowserProfilesByTag(tag: string): Promise<BrowserProfile[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileListByTag) {
    return (await bindings.BrowserProfileListByTag(tag)) || []
  }
  return mockProfiles.filter(p => p.tags?.includes(tag))
}

export async function fetchAllTags(): Promise<string[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserGetAllTags) {
    return (await bindings.BrowserGetAllTags()) || []
  }
  const set = new Set<string>()
  mockProfiles.forEach(p => p.tags?.forEach(t => set.add(t)))
  return Array.from(set).sort()
}

export async function createBrowserProfile(input: BrowserProfileInput): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileCreate) {
    return (await bindings.BrowserProfileCreate(input)) || null
  }
  const profile: BrowserProfile = {
    profileId: `mock-${Date.now()}`,
    ...input,
    keywords: input.keywords || {},
    running: false,
    debugPort: 0,
    debugReady: false,
    pid: 0,
    runtimeWarning: '',
    lastError: '',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  }
  mockProfiles = [profile, ...mockProfiles]
  return profile
}

export async function batchCreateBrowserProfiles(
  prefix: string,
  startIndex: number,
  count: number,
  input: BrowserProfileInput
): Promise<BrowserProfile[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileBatchCreate) {
    const result = await bindings.BrowserProfileBatchCreate(prefix, startIndex, count, input)
    return result || []
  }
  // mock fallback
  const created: BrowserProfile[] = []
  for (let i = 0; i < count; i++) {
    const profile: BrowserProfile = {
      profileId: `mock-batch-${Date.now()}-${i}`,
      ...input,
      profileName: `${prefix}-${startIndex + i}`,
      keywords: input.keywords || [],
      running: false,
      debugPort: 0,
      debugReady: false,
      pid: 0,
      runtimeWarning: '',
      lastError: '',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    }
    created.push(profile)
  }
  mockProfiles = [...created, ...mockProfiles]
  return created
}

export async function randomizeProfileFingerprint(profileId: string): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileRandomizeFingerprint) {
    return await bindings.BrowserProfileRandomizeFingerprint(profileId)
  }
  // mock fallback
  const idx = mockProfiles.findIndex(p => p.profileId === profileId)
  if (idx === -1) return null
  const seed = `--fingerprint=${Math.floor(Math.random() * 2147483646) + 1}`
  const rest = (mockProfiles[idx].fingerprintArgs || []).filter(a => !a.toLowerCase().startsWith('--fingerprint='))
  mockProfiles[idx] = {
    ...mockProfiles[idx],
    fingerprintArgs: [...rest, seed],
    updatedAt: new Date().toISOString(),
  }
  return mockProfiles[idx]
}

export async function updateBrowserProfile(profileId: string, input: BrowserProfileInput): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileUpdate) {
    return (await bindings.BrowserProfileUpdate(profileId, input)) || null
  }
  const index = mockProfiles.findIndex(item => item.profileId === profileId)
  if (index === -1) return null
  mockProfiles[index] = { ...mockProfiles[index], ...input, updatedAt: new Date().toISOString() }
  return mockProfiles[index]
}

/** Pause/resume remote proxy for one environment without unbinding pool proxy. */
export async function setBrowserProfileProxyPaused(profileId: string, paused: boolean): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileSetProxyPaused) {
    return (await bindings.BrowserProfileSetProxyPaused(profileId, paused)) || null
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserProfileSetProxyPaused) {
    return (await goApp.BrowserProfileSetProxyPaused(profileId, paused)) || null
  }
  const index = mockProfiles.findIndex(item => item.profileId === profileId)
  if (index === -1) return null
  mockProfiles[index] = { ...mockProfiles[index], proxyPaused: paused, updatedAt: new Date().toISOString() }
  return mockProfiles[index]
}

export async function deleteBrowserProfile(profileId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileDeleteWithCache) {
    await bindings.BrowserProfileDeleteWithCache(profileId, true)
    return true
  }
  if (bindings?.BrowserProfileDelete) {
    await bindings.BrowserProfileDelete(profileId)
    return true
  }
  mockProfiles = mockProfiles.filter(item => item.profileId !== profileId)
  return true
}

export async function copyBrowserProfile(profileId: string, newName: string): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileCopy) {
    return (await bindings.BrowserProfileCopy(profileId, newName)) || null
  }
  // mock
  const src = mockProfiles.find(p => p.profileId === profileId)
  if (!src) return null
  const copy: BrowserProfile = {
    ...src,
    profileId: `mock-${Date.now()}`,
    profileName: newName || src.profileName + ' (副本)',
    userDataDir: `mock-${Date.now()}`,
    running: false,
    debugReady: false,
    runtimeWarning: '',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  }
  mockProfiles = [copy, ...mockProfiles]
  return copy
}

export async function importExtensionToBrowserProfiles(profileIds: string[], downloadAddress: string): Promise<ExtensionImportResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileImportExtension) {
    return await bindings.BrowserProfileImportExtension(profileIds, downloadAddress)
  }
  mockProfiles = mockProfiles.map(item => {
    if (!profileIds.includes(item.profileId)) return item
    const nextArg = `--load-extension=mock-extension-from-${downloadAddress}`
    return { ...item, launchArgs: [...(item.launchArgs || []), nextArg], updatedAt: new Date().toISOString() }
  })
  return {
    extensionDir: `mock-extension-from-${downloadAddress}`,
    extensionId: 'mock-extension',
    updatedProfiles: profileIds,
    message: `扩展已绑定到 ${profileIds.length} 个实例`,
  }
}

export async function importGlobalExtension(downloadAddress: string): Promise<ExtensionImportResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserGlobalExtensionImport) {
    return await bindings.BrowserGlobalExtensionImport(downloadAddress)
  }
  return {
    extensionDir: `mock-global-extension-from-${downloadAddress}`,
    extensionId: 'mock-global-extension',
    updatedProfiles: mockProfiles.map(item => item.profileId),
    message: `扩展已设为全局使用并同步到 ${mockProfiles.length} 个实例`,
  }
}

export async function removeGlobalExtension(downloadAddress: string): Promise<ExtensionImportResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserGlobalExtensionRemove) {
    return await bindings.BrowserGlobalExtensionRemove(downloadAddress)
  }
  return {
    extensionDir: '',
    extensionId: 'mock-global-extension',
    updatedProfiles: mockProfiles.map(item => item.profileId),
    message: '全局扩展已移除',
  }
}

export async function fetchGlobalExtensions(): Promise<GlobalManagedExtension[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserGlobalExtensionList) {
    return (await bindings.BrowserGlobalExtensionList()) || []
  }
  return []
}

export async function removeExtensionFromBrowserProfiles(profileIds: string[], downloadAddress: string): Promise<ExtensionImportResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileRemoveExtension) {
    return await bindings.BrowserProfileRemoveExtension(profileIds, downloadAddress)
  }
  mockProfiles = mockProfiles.map(item => {
    if (!profileIds.includes(item.profileId)) return item
    return {
      ...item,
      launchArgs: (item.launchArgs || []).filter(arg => !String(arg).includes(downloadAddress)),
      updatedAt: new Date().toISOString(),
    }
  })
  return {
    extensionDir: `mock-extension-from-${downloadAddress}`,
    extensionId: 'mock-extension',
    updatedProfiles: profileIds,
    message: `扩展已从 ${profileIds.length} 个实例解绑`,
  }
}

export interface ExtensionIntegrityScanResult {
  totalProfiles: number
  complete: number
  incomplete: number
  repaired: number
  skippedRunning: number
  incompleteIds: string[]
  dismissedIncomplete: number
  message: string
  alreadyScanned: boolean
}

/** One-shot integrity scan on client open (backend de-dupes per session). */
export async function scanExtensionIntegrityAll(force = false): Promise<ExtensionIntegrityScanResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserExtensionIntegrityScanAll) {
    return await bindings.BrowserExtensionIntegrityScanAll(force)
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserExtensionIntegrityScanAll) {
    return await goApp.BrowserExtensionIntegrityScanAll(force)
  }
  return {
    totalProfiles: 0,
    complete: 0,
    incomplete: 0,
    repaired: 0,
    skippedRunning: 0,
    incompleteIds: [],
    dismissedIncomplete: 0,
    message: '扩展巡检不可用（开发预览）',
    alreadyScanned: true,
  }
}

/** Sync all known managed packages onto the given environments. */
/** Permanently stop reporting the given environments' first-adapt notice. */
export async function dismissExtensionIntegrityNotice(profileIds: string[]): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserExtensionIntegrityDismissNotice) {
    try {
      await bindings.BrowserExtensionIntegrityDismissNotice(profileIds)
      return true
    } catch {
      return false
    }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserExtensionIntegrityDismissNotice) {
    try {
      await goApp.BrowserExtensionIntegrityDismissNotice(profileIds)
      return true
    } catch {
      return false
    }
  }
  return true
}

/** Re-enable the first-adapt notice for all previously dismissed environments. */
export async function clearExtensionIntegrityDismissed(): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserExtensionIntegrityClearDismissed) {
    try {
      await bindings.BrowserExtensionIntegrityClearDismissed()
      return true
    } catch {
      return false
    }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserExtensionIntegrityClearDismissed) {
    try {
      await goApp.BrowserExtensionIntegrityClearDismissed()
      return true
    } catch {
      return false
    }
  }
  return true
}

// ============================================================================
// Legacy-data auto scan (leftover folders self-identification)
// ============================================================================

export interface LegacyDataAutoFolder {
  folderKey: string
  folderName: string
  profileName: string
  sizeBytes: number
}

export interface LegacyDataAutoPreview {
  folders: LegacyDataAutoFolder[]
  dismissed: number
  message: string
}

/** Scan the active data root for Chrome data folders not attached to any environment. */
export async function scanLegacyDataAuto(): Promise<LegacyDataAutoPreview> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserLegacyDataAutoScan) {
    try {
      return (await bindings.BrowserLegacyDataAutoScan()) || { folders: [], dismissed: 0, message: '' }
    } catch {
      return { folders: [], dismissed: 0, message: '' }
    }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserLegacyDataAutoScan) {
    try {
      return (await goApp.BrowserLegacyDataAutoScan()) || { folders: [], dismissed: 0, message: '' }
    } catch {
      return { folders: [], dismissed: 0, message: '' }
    }
  }
  return { folders: [], dismissed: 0, message: '' }
}

/** Import unregistered data folders as new environments (in place, no copy). */
export async function importLegacyDataFolders(folderKeys: string[]): Promise<LegacyDataAutoPreview> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserLegacyDataImportFolders) {
    return (await bindings.BrowserLegacyDataImportFolders(folderKeys)) || { folders: [], dismissed: 0, message: '' }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserLegacyDataImportFolders) {
    return (await goApp.BrowserLegacyDataImportFolders(folderKeys)) || { folders: [], dismissed: 0, message: '' }
  }
  return { folders: [], dismissed: 0, message: '旧数据导入不可用（当前为开发预览）' }
}

/** Record folders the user chose not to import; never suggest them again. */
export async function dismissLegacyDataFolders(folderKeys: string[]): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserLegacyDataDismissFolders) {
    try {
      await bindings.BrowserLegacyDataDismissFolders(folderKeys)
      return true
    } catch {
      return false
    }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserLegacyDataDismissFolders) {
    try {
      await goApp.BrowserLegacyDataDismissFolders(folderKeys)
      return true
    } catch {
      return false
    }
  }
  return true
}

/** Re-enable leftover-data suggestions previously ignored. */
export async function clearLegacyDataDismissed(): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserLegacyDataClearDismissed) {
    try {
      await bindings.BrowserLegacyDataClearDismissed()
      return true
    } catch {
      return false
    }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserLegacyDataClearDismissed) {
    try {
      await goApp.BrowserLegacyDataClearDismissed()
      return true
    } catch {
      return false
    }
  }
  return true
}

export async function syncKnownExtensionsToProfiles(profileIds: string[]): Promise<ExtensionImportResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserExtensionSyncKnownToProfiles) {
    return await bindings.BrowserExtensionSyncKnownToProfiles(profileIds)
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserExtensionSyncKnownToProfiles) {
    return await goApp.BrowserExtensionSyncKnownToProfiles(profileIds)
  }
  return {
    extensionDir: '',
    extensionId: '',
    updatedProfiles: profileIds,
    message: '当前没有可同步的扩展（开发预览）',
  }
}

export async function listKnownExtensionPackages(): Promise<Array<{ extensionId: string; name: string; packagePath: string }>> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserExtensionListKnownPackages) {
    return (await bindings.BrowserExtensionListKnownPackages()) || []
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserExtensionListKnownPackages) {
    return (await goApp.BrowserExtensionListKnownPackages()) || []
  }
  return []
}

// ============================================================================
// Instance API
// ============================================================================

export async function startBrowserInstance(profileId: string): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserInstanceStart) {
    return (await bindings.BrowserInstanceStart(profileId)) || null
  }
  mockProfiles = mockProfiles.map(item =>
    item.profileId === profileId ? { ...item, running: true, debugPort: 9222, debugReady: true, pid: Math.floor(Math.random() * 100000), runtimeWarning: '', lastStartAt: new Date().toISOString() } : item
  )
  return mockProfiles.find(item => item.profileId === profileId) || null
}

export async function startBrowserInstanceByCode(code: string): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserInstanceStartByCode) {
    return (await bindings.BrowserInstanceStartByCode(code)) || null
  }
  const normalized = code.trim().toUpperCase()
  const profile = mockProfiles.find(item => (item.launchCode || '').toUpperCase() === normalized)
  if (!profile) {
    throw new Error('launch code not found')
  }
  return await startBrowserInstance(profile.profileId)
}

export async function stopBrowserInstance(profileId: string): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserInstanceStop) {
    return (await bindings.BrowserInstanceStop(profileId)) || null
  }
  mockProfiles = mockProfiles.map(item =>
    item.profileId === profileId ? { ...item, running: false, debugReady: false, debugPort: 0, pid: 0, runtimeWarning: '', lastStopAt: new Date().toISOString() } : item
  )
  return mockProfiles.find(item => item.profileId === profileId) || null
}

export async function restartBrowserInstance(profileId: string): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserInstanceRestart) {
    return (await bindings.BrowserInstanceRestart(profileId)) || null
  }
  await stopBrowserInstance(profileId)
  return await startBrowserInstance(profileId)
}

export async function openBrowserUrl(profileId: string, targetUrl: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserInstanceOpenUrl) {
    return (await bindings.BrowserInstanceOpenUrl(profileId, targetUrl)) === true
  }
  return true
}

export async function fetchBrowserTabs(profileId: string): Promise<BrowserTab[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserInstanceGetTabs) {
    return (await bindings.BrowserInstanceGetTabs(profileId)) || []
  }
  return [
    { tabId: 'tab-1', title: '新标签页', url: 'about:blank', active: true },
    { tabId: 'tab-2', title: '示例站点', url: 'https://example.com', active: false },
  ]
}

// ============================================================================
// Settings API
// ============================================================================

export async function fetchBrowserSettings(): Promise<BrowserSettings> {
  const bindings: any = await getBindings()
  if (bindings?.GetBrowserSettings) {
    return (await bindings.GetBrowserSettings()) || { userDataRoot: 'data', defaultFingerprintArgs: [], defaultLaunchArgs: [], defaultProxy: '', proxyNetworkMode: 'auto', localVpnProxy: '', startReadyTimeoutMs: 3000, startStableWindowMs: 1200 }
  }
  return { userDataRoot: 'data', defaultFingerprintArgs: [], defaultLaunchArgs: [], defaultProxy: '', proxyNetworkMode: 'auto', localVpnProxy: '', startReadyTimeoutMs: 3000, startStableWindowMs: 1200 }
}

export async function saveBrowserSettings(settings: BrowserSettings): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.SaveBrowserSettings) {
    await bindings.SaveBrowserSettings(settings)
    return true
  }
  return true
}

// ============================================================================
// Core API
// ============================================================================

export async function fetchBrowserCores(): Promise<BrowserCore[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreList) {
    return (await bindings.BrowserCoreList()) || []
  }
  return mockCores
}

export async function saveBrowserCore(input: BrowserCoreInput): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreSave) {
    await bindings.BrowserCoreSave(input)
    return true
  }
  const index = mockCores.findIndex(c => c.coreId === input.coreId)
  if (index >= 0) {
    mockCores[index] = input
  } else {
    mockCores.push({ ...input, coreId: input.coreId || `core-${Date.now()}` })
  }
  return true
}

export async function deleteBrowserCore(coreId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreDelete) {
    await bindings.BrowserCoreDelete(coreId)
    return true
  }
  mockCores = mockCores.filter(c => c.coreId !== coreId)
  return true
}

export async function setDefaultBrowserCore(coreId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreSetDefault) {
    await bindings.BrowserCoreSetDefault(coreId)
    return true
  }
  mockCores = mockCores.map(c => ({ ...c, isDefault: c.coreId === coreId }))
  return true
}

export async function validateBrowserCorePath(corePath: string): Promise<BrowserCoreValidateResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreValidate) {
    return (await bindings.BrowserCoreValidate(corePath)) || { valid: false, message: '验证失败' }
  }
  return { valid: true, message: '路径有效（模拟）' }
}

export async function fetchCoreExtendedInfo(): Promise<BrowserCoreExtended[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreExtendedInfo) {
    return (await bindings.BrowserCoreExtendedInfo()) || []
  }
  return []
}

export async function scanBrowserCores(): Promise<BrowserCore[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreScan) {
    return (await bindings.BrowserCoreScan()) || []
  }
  return mockCores
}

export async function BrowserCoreDownload(coreName: string, url: string, proxyConfig?: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCoreDownload) {
    await bindings.BrowserCoreDownload(coreName, url, proxyConfig || '')
    return true
  }
  return true
}

// ============================================================================
// Proxy API
// ============================================================================

export async function fetchBrowserProxies(): Promise<BrowserProxy[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyList) {
    return (await bindings.BrowserProxyList()) || []
  }
  return mockProxies
}

export async function fetchBrowserProxyGroups(): Promise<string[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyListGroups) {
    return (await bindings.BrowserProxyListGroups()) || []
  }
  return []
}

export async function fetchBrowserProxiesByGroup(groupName: string): Promise<BrowserProxy[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyListByGroup) {
    return (await bindings.BrowserProxyListByGroup(groupName)) || []
  }
  return mockProxies.filter(p => p.groupName === groupName)
}

export interface ClashImportURLResult {
  url: string
  content: string
  proxyCount: number
  dnsServers?: string
  suggestedGroup?: string
  profileTitle?: string
  profileUpdateInterval?: string
  subscriptionInfo?: {
    uploadBytes?: number
    downloadBytes?: number
    totalBytes?: number
    usedBytes?: number
    remainingBytes?: number
    expireAt?: string
  }
}

export async function fetchClashImportFromURL(targetURL: string): Promise<ClashImportURLResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyFetchClashByURL) {
    return (await bindings.BrowserProxyFetchClashByURL(targetURL)) || {
      url: targetURL,
      content: '',
      proxyCount: 0,
    }
  }

  // 兜底：wailsjs 尚未刷新时，直接通过 window.go 调用后端绑定
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserProxyFetchClashByURL) {
    return (await goApp.BrowserProxyFetchClashByURL(targetURL)) || {
      url: targetURL,
      content: '',
      proxyCount: 0,
    }
  }

  throw new Error('当前环境不支持 URL 导入 Clash 配置')
}

export async function saveBrowserProxies(proxies: BrowserProxy[]): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.SaveBrowserProxies) {
    await bindings.SaveBrowserProxies(proxies)
    return true
  }
  mockProxies = proxies
  return true
}

export async function upsertBrowserProxy(proxy: BrowserProxy): Promise<BrowserProxy> {
  const bindings: any = await getBindings()
  if (bindings?.UpsertBrowserProxy) {
    return (await bindings.UpsertBrowserProxy(proxy)) || proxy
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.UpsertBrowserProxy) {
    return (await goApp.UpsertBrowserProxy(proxy)) || proxy
  }
  const index = mockProxies.findIndex(item => item.proxyId === proxy.proxyId)
  if (index >= 0) mockProxies[index] = proxy
  else mockProxies.push(proxy)
  return proxy
}

export async function deleteBrowserProxies(proxyIds: string[]): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.DeleteBrowserProxies) {
    await bindings.DeleteBrowserProxies(proxyIds)
    return true
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.DeleteBrowserProxies) {
    await goApp.DeleteBrowserProxies(proxyIds)
    return true
  }
  const ids = new Set(proxyIds)
  mockProxies = mockProxies.filter(item => !ids.has(item.proxyId))
  return true
}

export async function validateProxyConfig(proxyConfig: string, proxyId: string): Promise<{ supported: boolean; errorMsg: string }> {
  const bindings: any = await getBindings()
  if (bindings?.ValidateProxyConfig) {
    return (await bindings.ValidateProxyConfig(proxyConfig, proxyId)) || { supported: true, errorMsg: '' }
  }
  return { supported: true, errorMsg: '' }
}

export async function testProxyConnectivity(proxyId: string, proxyConfig: string): Promise<{ proxyId: string; ok: boolean; latencyMs: number; error: string }> {
  const bindings: any = await getBindings()
  if (bindings?.TestProxyConnectivity) {
    return (await bindings.TestProxyConnectivity(proxyId, proxyConfig)) || { proxyId, ok: false, latencyMs: 0, error: '调用失败' }
  }
  return { proxyId, ok: false, latencyMs: 0, error: 'Wails 绑定不可用，请重新打包客户端' }
}

export async function testProxyRealConnectivity(proxyId: string): Promise<{ proxyId: string; ok: boolean; latencyMs: number; error: string }> {
  const bindings: any = await getBindings()
  if (bindings?.TestProxyRealConnectivity) {
    return (await bindings.TestProxyRealConnectivity(proxyId)) || { proxyId, ok: false, latencyMs: 0, error: '调用失败' }
  }
  return { proxyId, ok: false, latencyMs: 0, error: 'Wails 绑定不可用，请重新打包客户端' }
}

export interface ProxyConnectivityTestResult {
  proxyId: string
  ok: boolean
  latencyMs: number
  error: string
  resolvedConfig?: string
}

export async function testProxyConfigRealConnectivity(proxyConfig: string): Promise<ProxyConnectivityTestResult> {
  const bindings: any = await getBindings()
  if (bindings?.TestProxyConfigRealConnectivity) {
    return (await bindings.TestProxyConfigRealConnectivity(proxyConfig)) || { proxyId: '', ok: false, latencyMs: 0, error: '调用失败' }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.TestProxyConfigRealConnectivity) {
    return (await goApp.TestProxyConfigRealConnectivity(proxyConfig)) || { proxyId: '', ok: false, latencyMs: 0, error: '调用失败' }
  }
  return { proxyId: '', ok: false, latencyMs: 0, error: 'Wails 绑定不可用，请重新打包客户端' }
}

export type ProxyFullCheckResult = ProxyConnectivityTestResult & {
  protocol?: string
  message?: string
  exitIP?: string
  country?: string
  city?: string
  timezoneHint?: string
  isResidential?: boolean
  dnsViaProxy?: boolean
  resolvedConfig?: string
}

export async function browserProxyTestSpeed(proxyId: string): Promise<ProxyConnectivityTestResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyTestSpeed) {
    return (await bindings.BrowserProxyTestSpeed(proxyId)) || { proxyId, ok: false, latencyMs: 0, error: '调用失败' }
  }
  return { proxyId, ok: false, latencyMs: 0, error: 'Wails 绑定不可用，请重新打包客户端' }
}

/** One-click check: latency + protocol + exit IP/geo summary. */
export async function browserProxyFullCheck(proxyId: string): Promise<ProxyFullCheckResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyFullCheck) {
    return (await bindings.BrowserProxyFullCheck(proxyId)) || { proxyId, ok: false, latencyMs: 0, error: '调用失败' }
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.BrowserProxyFullCheck) {
    return (await goApp.BrowserProxyFullCheck(proxyId)) || { proxyId, ok: false, latencyMs: 0, error: '调用失败' }
  }
  // Fallback: speed only when binding not regenerated yet.
  return browserProxyTestSpeed(proxyId)
}

/** IANA timezone hint from proxy exit country (for fingerprint alignment). */
export async function suggestTimezoneFromProxy(proxyId: string): Promise<string> {
  const bindings: any = await getBindings()
  if (bindings?.SuggestTimezoneFromProxy) {
    return String((await bindings.SuggestTimezoneFromProxy(proxyId)) || '')
  }
  const goApp = (window as any).go?.main?.App
  if (goApp?.SuggestTimezoneFromProxy) {
    return String((await goApp.SuggestTimezoneFromProxy(proxyId)) || '')
  }
  return ''
}

export function applyTimezoneToFingerprintArgs(args: string[] | undefined, timezone: string): string[] {
  const tz = timezone.trim()
  const base = Array.isArray(args) ? args.filter(a => !/^--timezone=/i.test(String(a).trim())) : []
  if (!tz) return base
  return [...base, `--timezone=${tz}`]
}

export async function browserProxyBatchTestSpeed(proxyIds: string[], concurrency: number = 8): Promise<ProxyConnectivityTestResult[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyBatchTestSpeed) {
    return (await bindings.BrowserProxyBatchTestSpeed(proxyIds, concurrency)) || []
  }
  return proxyIds.map(id => ({ proxyId: id, ok: false, latencyMs: 0, error: 'Wails 绑定不可用，请重新打包客户端' }))
}

export async function browserProxyCheckIPHealth(proxyId: string): Promise<ProxyIPHealthResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyCheckIPHealth) {
    return (await bindings.BrowserProxyCheckIPHealth(proxyId)) || {
      proxyId,
      ok: false,
      source: 'ippure',
      error: '调用失败',
      ip: '',
      fraudScore: 0,
      isResidential: false,
      isBroadcast: false,
      country: '',
      region: '',
      city: '',
      asOrganization: '',
      rawData: {},
      updatedAt: new Date().toISOString(),
    }
  }
  return {
    proxyId,
    ok: false,
    source: 'ippure',
    error: 'Wails 绑定不可用，请重新打包客户端',
    ip: '',
    fraudScore: 0,
    isResidential: false,
    isBroadcast: false,
    country: '',
    region: '',
    city: '',
    asOrganization: '',
    rawData: {},
    updatedAt: new Date().toISOString(),
  }
}

export async function browserProxyBatchCheckIPHealth(proxyIds: string[], concurrency: number = 10): Promise<ProxyIPHealthResult[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProxyBatchCheckIPHealth) {
    return (await bindings.BrowserProxyBatchCheckIPHealth(proxyIds, concurrency)) || []
  }
  return proxyIds.map(proxyId => ({
    proxyId,
    ok: false,
    source: 'ippure',
    error: 'Wails 绑定不可用，请重新打包客户端',
    ip: '',
    fraudScore: 0,
    isResidential: false,
    isBroadcast: false,
    country: '',
    region: '',
    city: '',
    asOrganization: '',
    rawData: {},
    updatedAt: new Date().toISOString(),
  }))
}

export async function openUserDataDir(userDataDir: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.OpenUserDataDir) {
    await bindings.OpenUserDataDir(userDataDir)
    return true
  }
  return false
}

export async function openCorePath(corePath: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.OpenCorePath) {
    await bindings.OpenCorePath(corePath)
    return true
  }
  return false
}

// ============================================================================
// Cookie API
// ============================================================================

export async function fetchBrowserCookies(profileId: string): Promise<CookieInfo[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserGetCookies) {
    return (await bindings.BrowserGetCookies(profileId)) || []
  }
  // mock data
  return [
    { name: 'session', value: 'abc123', domain: '.example.com', path: '/', expires: Date.now() / 1000 + 3600, httpOnly: true, secure: true, sameSite: 'Lax' },
    { name: 'pref', value: 'dark', domain: 'example.com', path: '/', expires: -1, httpOnly: false, secure: false, sameSite: 'None' },
  ]
}

export async function clearBrowserCookies(profileId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserClearCookies) {
    await bindings.BrowserClearCookies(profileId)
    return true
  }
  return true
}

export async function exportBrowserCookies(profileId: string): Promise<string> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserExportCookies) {
    return (await bindings.BrowserExportCookies(profileId)) || ''
  }
  return '# Netscape HTTP Cookie File\n# Generated by BrowserManager\n\n.example.com\tTRUE\t/\tTRUE\t0\tsession\tabc123\n'
}

// ============================================================================
// Snapshot API
// ============================================================================

export async function listSnapshots(profileId: string): Promise<SnapshotInfo[]> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserSnapshotList) {
    return (await bindings.BrowserSnapshotList(profileId)) || []
  }
  return []
}

export async function createSnapshot(profileId: string, name: string): Promise<SnapshotInfo | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserSnapshotCreate) {
    return (await bindings.BrowserSnapshotCreate(profileId, name)) || null
  }
  // mock
  return {
    snapshotId: `snap-${Date.now()}`,
    profileId,
    name,
    sizeMB: 12.5,
    createdAt: new Date().toISOString(),
  }
}

export async function restoreSnapshot(profileId: string, snapshotId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserSnapshotRestore) {
    await bindings.BrowserSnapshotRestore(profileId, snapshotId)
    return true
  }
  return true
}

export async function deleteSnapshot(profileId: string, snapshotId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserSnapshotDelete) {
    await bindings.BrowserSnapshotDelete(profileId, snapshotId)
    return true
  }
  return true
}

export interface CacheCleanResult {
  profilesScanned: number
  profilesCleaned: number
  filesRemoved: number
  dirsRemoved: number
  bytesRemoved: number
  errors: number
  skippedRunning: number
  cleanedProfiles: string[]
  message: string
}

export interface CacheCleanSettings {
  autoCleanEnabled: boolean
  intervalDays: number
  lastCleanAt?: string
  nextCleanAt?: string
}

export async function cleanBrowserCache(includeRunning = false): Promise<CacheCleanResult> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserCleanCache) {
    return await bindings.BrowserCleanCache(includeRunning)
  }
  return {
    profilesScanned: 0,
    profilesCleaned: 0,
    filesRemoved: 0,
    dirsRemoved: 0,
    bytesRemoved: 0,
    errors: 0,
    skippedRunning: 0,
    cleanedProfiles: [],
    message: '当前环境不支持清理缓存接口',
  }
}

export async function getCacheCleanSettings(): Promise<CacheCleanSettings> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserGetCacheCleanSettings) {
    return await bindings.BrowserGetCacheCleanSettings()
  }
  return { autoCleanEnabled: false, intervalDays: 30 }
}

export async function saveCacheCleanSettings(enabled: boolean): Promise<CacheCleanSettings> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserSaveCacheCleanSettings) {
    return await bindings.BrowserSaveCacheCleanSettings(enabled)
  }
  return { autoCleanEnabled: enabled, intervalDays: 30 }
}

// ============================================================================
// Bookmark API
// ============================================================================

export async function fetchBookmarks(): Promise<BrowserBookmark[]> {
  const bindings: any = await getBindings()
  if (bindings?.BookmarkList) {
    return (await bindings.BookmarkList()) || []
  }
  return []
}

export async function saveBookmarks(items: BrowserBookmark[]): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BookmarkSave) {
    await bindings.BookmarkSave(items)
    return true
  }
  return true
}

export async function resetBookmarks(): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BookmarkReset) {
    await bindings.BookmarkReset()
    return true
  }
  return true
}

// ============================================================================
// Keywords API
// ============================================================================

export async function setProfileKeywords(profileId: string, keywords: string[]): Promise<BrowserProfile | null> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileSetKeywords) {
    return (await bindings.BrowserProfileSetKeywords(profileId, keywords)) || null
  }
  mockProfiles = mockProfiles.map(p =>
    p.profileId === profileId ? { ...p, keywords, updatedAt: new Date().toISOString() } : p
  )
  return mockProfiles.find(p => p.profileId === profileId) || null
}

// ============================================================================
// LaunchCode API
// ============================================================================

export interface LaunchServerInfo {
  host: string
  port: number
  preferredPort: number
  baseUrl: string
  cdpUrl: string
  activeDebugPort: number
  ready: boolean
  apiAuth: {
    requested: boolean
    configured: boolean
    enabled: boolean
    header: string
  }
}

function normalizeLaunchServerInfo(payload: any): LaunchServerInfo {
  const host = String(payload?.host || '127.0.0.1')
  const port = Number(payload?.port) || 0
  const preferredPort = Number(payload?.preferredPort) || 0
  const fallbackPort = preferredPort > 0 ? preferredPort : 19876
  const effectivePort = port > 0 ? port : fallbackPort
  const baseUrl = String(payload?.baseUrl || (effectivePort > 0 ? `http://${host}:${effectivePort}` : ''))
  const cdpUrl = String(payload?.cdpUrl || baseUrl)
  const activeDebugPort = Number(payload?.activeDebugPort) || 0
  const apiAuthPayload = payload?.apiAuth || {}
  const apiAuth = {
    requested: !!apiAuthPayload?.requested,
    configured: !!apiAuthPayload?.configured,
    enabled: !!apiAuthPayload?.enabled,
    header: String(apiAuthPayload?.header || 'X-Boost-Api-Key'),
  }

  return {
    host,
    port: effectivePort,
    preferredPort,
    baseUrl,
    cdpUrl,
    activeDebugPort,
    ready: !!payload?.ready && port > 0,
    apiAuth,
  }
}

export async function fetchLaunchServerInfo(): Promise<LaunchServerInfo> {
  const bindings: any = await getBindings()
  if (bindings?.GetLaunchServerInfo) {
    return normalizeLaunchServerInfo(await bindings.GetLaunchServerInfo())
  }

  const goApp = (window as any).go?.main?.App
  if (goApp?.GetLaunchServerInfo) {
    return normalizeLaunchServerInfo(await goApp.GetLaunchServerInfo())
  }

  return {
    host: '127.0.0.1',
    port: 19876,
    preferredPort: 19876,
    baseUrl: 'http://127.0.0.1:19876',
    cdpUrl: 'http://127.0.0.1:19876',
    activeDebugPort: 0,
    ready: false,
    apiAuth: {
      requested: false,
      configured: false,
      enabled: false,
      header: 'X-Boost-Api-Key',
    },
  }
}

export async function getBrowserProfileCode(profileId: string): Promise<string> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileGetCode) {
    return (await bindings.BrowserProfileGetCode(profileId)) || ''
  }
  return ''
}

export async function regenerateBrowserProfileCode(profileId: string): Promise<string> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileRegenerateCode) {
    return (await bindings.BrowserProfileRegenerateCode(profileId)) || ''
  }
  return ''
}

export async function setBrowserProfileCode(profileId: string, code: string): Promise<string> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileSetCode) {
    return (await bindings.BrowserProfileSetCode(profileId, code)) || ''
  }
  return code.trim().toUpperCase()
}


export async function batchSetProfileTags(profileIds: string[], tags: string[], replace: boolean): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileBatchSetTags) {
    await bindings.BrowserProfileBatchSetTags(profileIds, tags, replace)
    return true
  }
  return true
}

export async function batchRemoveProfileTags(profileIds: string[], tags: string[]): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserProfileBatchRemoveTags) {
    await bindings.BrowserProfileBatchRemoveTags(profileIds, tags)
    return true
  }
  return true
}

export async function renameBrowserTag(oldName: string, newName: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.BrowserRenameTag) {
    await bindings.BrowserRenameTag(oldName, newName)
    return true
  }
  return true
}

// ============================================================================
// Group API
// ============================================================================

export async function fetchGroups(): Promise<BrowserGroupWithCount[]> {
  const bindings: any = await getBindings()
  if (bindings?.ListGroups) {
    return (await bindings.ListGroups()) || []
  }
  return []
}

export async function createGroup(input: BrowserGroupInput): Promise<BrowserGroup | null> {
  const bindings: any = await getBindings()
  if (bindings?.CreateGroup) {
    return (await bindings.CreateGroup(input)) || null
  }
  return null
}

export async function updateGroup(groupId: string, input: BrowserGroupInput): Promise<BrowserGroup | null> {
  const bindings: any = await getBindings()
  if (bindings?.UpdateGroup) {
    return (await bindings.UpdateGroup(groupId, input)) || null
  }
  return null
}

export async function deleteGroup(groupId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.DeleteGroup) {
    await bindings.DeleteGroup(groupId)
    return true
  }
  return false
}

export async function moveInstancesToGroup(profileIds: string[], groupId: string): Promise<boolean> {
  const bindings: any = await getBindings()
  if (bindings?.MoveInstancesToGroup) {
    await bindings.MoveInstancesToGroup(profileIds, groupId)
    return true
  }
  return false
}

// ============================================================================
// Rabby wallet batch import
// ============================================================================

export async function prepareWalletImport(walletType: WalletImportType): Promise<WalletImportPreview> {
  const bindings: any = await getBindings()
  if (!bindings?.WalletBatchPrepare) {
    throw new Error('当前客户端不支持钱包批量导入，请更新后重试')
  }
  return await bindings.WalletBatchPrepare(walletType)
}

export async function exportWalletImportTemplate(walletType: WalletImportType): Promise<Record<string, any>> {
  const bindings: any = await getBindings()
  if (!bindings?.WalletExportImportTemplate) {
    throw new Error('当前客户端不支持钱包导入模板，请更新后重试')
  }
  return await bindings.WalletExportImportTemplate(walletType)
}

export async function executeWalletImport(input: WalletBatchExecuteInput): Promise<WalletImportResult> {
  const bindings: any = await getBindings()
  if (!bindings?.WalletBatchExecute) {
    throw new Error('当前客户端不支持钱包批量导入，请更新后重试')
  }
  return await bindings.WalletBatchExecute(input)
}

export async function cancelWalletImport(sessionId: string): Promise<void> {
  if (!sessionId) return
  const bindings: any = await getBindings()
  if (bindings?.WalletBatchCancel) await bindings.WalletBatchCancel(sessionId)
}

export async function prepareRabbyWalletImport(): Promise<RabbyWalletImportPreview> {
  const bindings: any = await getBindings()
  if (!bindings?.RabbyWalletBatchPrepare) {
    throw new Error('当前客户端不支持 Rabby 批量导入，请更新后重试')
  }
  return await bindings.RabbyWalletBatchPrepare()
}

export async function exportRabbyWalletImportTemplate(): Promise<Record<string, any>> {
  const bindings: any = await getBindings()
  if (!bindings?.RabbyWalletExportImportTemplate) {
    throw new Error('当前客户端不支持导出 Rabby 模板，请更新后重试')
  }
  return await bindings.RabbyWalletExportImportTemplate()
}

export async function executeRabbyWalletImport(input: RabbyWalletBatchExecuteInput): Promise<RabbyWalletImportResult> {
  const bindings: any = await getBindings()
  if (!bindings?.RabbyWalletBatchExecute) {
    throw new Error('当前客户端不支持 Rabby 批量导入，请更新后重试')
  }
  return await bindings.RabbyWalletBatchExecute(input)
}

export async function cancelRabbyWalletImport(sessionId: string): Promise<void> {
  if (!sessionId) return
  const bindings: any = await getBindings()
  if (bindings?.RabbyWalletBatchCancel) {
    await bindings.RabbyWalletBatchCancel(sessionId)
  }
}
