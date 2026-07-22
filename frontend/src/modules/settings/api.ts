// Settings 模块 API
import type { AppSettings } from './types'
import { defaultSettings } from './types'
import { getCacheCleanSettings, saveCacheCleanSettings } from '../browser/api'

// 本地存储 key
const SETTINGS_KEY = 'app_settings'

const getBindings = async () => {
  try {
    return await import('../../wailsjs/go/main/App')
  } catch {
    return null
  }
}

export interface BackupActionResult {
  cancelled?: boolean
  message?: string
  zipPath?: string
  resetFirst?: boolean
  imported?: number
  skipped?: number
  conflicts?: number
  partial?: boolean
  componentTotal?: number
  componentSuccess?: number
  componentFailed?: number
  failedComponents?: Array<{
    componentId?: string
    componentName?: string
    error?: string
  }>
}

export interface LegacyDataRecoveryRow {
  environmentNumber: number
  profileId: string
  profileName: string
  userDataDir: string
  directoryExists: boolean
  status: 'ready' | 'conflict' | 'missing' | 'success' | 'failed'
  message: string
}

export interface LegacyDataRecoveryPreview {
  cancelled?: boolean
  sessionId: string
  sourcePath: string
  total: number
  restorable: number
  conflicts: number
  missing: number
  rows: LegacyDataRecoveryRow[]
  message?: string
}

export interface LegacyDataRecoveryResult {
  imported: number
  skipped: number
  failed: number
  backupPath: string
  rows: LegacyDataRecoveryRow[]
  message?: string
}

// 获取设置
export async function fetchSettings(): Promise<AppSettings> {
  let settings = defaultSettings
  try {
    const stored = localStorage.getItem(SETTINGS_KEY)
    if (stored) {
      settings = { ...defaultSettings, ...JSON.parse(stored) }
    }
  } catch {
  }
  try {
    const cache = await getCacheCleanSettings()
    settings = {
      ...settings,
      cacheAutoCleanEnabled: !!cache.autoCleanEnabled,
      cacheAutoCleanIntervalDays: Number(cache.intervalDays) || 30,
      cacheLastCleanAt: cache.lastCleanAt || '',
      cacheNextCleanAt: cache.nextCleanAt || '',
    }
  } catch {
  }
  return settings
}

// 保存设置
export async function saveSettings(settings: AppSettings): Promise<boolean> {
  try {
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings))
    await saveCacheCleanSettings(!!settings.cacheAutoCleanEnabled)
    return true
  } catch {
	return false
  }
}

// 重置设置
export async function resetSettings(): Promise<AppSettings> {
  localStorage.removeItem(SETTINGS_KEY)
  return defaultSettings
}

export async function initializeSystemData(): Promise<BackupActionResult> {
  const bindings: any = await getBindings()
  if (!bindings?.BackupInitializeSystem) {
    return { cancelled: false, message: '当前环境不支持后端初始化接口' }
  }
  return (await bindings.BackupInitializeSystem()) || {}
}

export async function exportSystemConfig(): Promise<BackupActionResult> {
  const bindings: any = await getBindings()
  if (!bindings?.BackupExportPackage) {
    return { cancelled: false, message: '当前环境不支持后端导出接口' }
  }
  return (await bindings.BackupExportPackage()) || {}
}

export async function importSystemConfig(resetFirst: boolean): Promise<BackupActionResult> {
  const bindings: any = await getBindings()
  if (!bindings?.BackupImportPackage) {
    return { cancelled: false, message: '当前环境不支持后端加载接口' }
  }
  return (await bindings.BackupImportPackage(resetFirst)) || {}
}

export async function prepareLegacyDataRecovery(): Promise<LegacyDataRecoveryPreview> {
  const bindings: any = await getBindings()
  if (!bindings?.LegacyDataRecoveryPrepare) {
    throw new Error('当前客户端不支持旧版 data 识别')
  }
  return await bindings.LegacyDataRecoveryPrepare()
}

export async function executeLegacyDataRecovery(sessionId: string): Promise<LegacyDataRecoveryResult> {
  const bindings: any = await getBindings()
  if (!bindings?.LegacyDataRecoveryExecute) {
    throw new Error('当前客户端不支持旧版 data 恢复')
  }
  return await bindings.LegacyDataRecoveryExecute(sessionId)
}

export async function cancelLegacyDataRecovery(sessionId: string): Promise<void> {
  const bindings: any = await getBindings()
  if (bindings?.LegacyDataRecoveryCancel && sessionId) {
    await bindings.LegacyDataRecoveryCancel(sessionId)
  }
}
