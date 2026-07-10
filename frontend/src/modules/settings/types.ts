// Settings 模块类型定义

export interface AppSettings {
  id?: number
  // 基础设置
  appName: string
  appDescription: string

  // 外观设置
  theme: 'light' | 'dark' | 'system'
  primaryColor: string
  language: string

  // 功能设置
  enableNotifications: boolean
  enableAutoSave: boolean
  autoSaveInterval: number

  // 高级设置
  maxUploadSize: number
  sessionTimeout: number
  cacheEnabled: boolean
  logLevel: 'debug' | 'info' | 'warn' | 'error'

  // 运行时设置
  maxMemoryMB: number
  gcPercent: number

  // 缓存清理
  cacheAutoCleanEnabled: boolean
  cacheAutoCleanIntervalDays: number
  cacheLastCleanAt?: string
  cacheNextCleanAt?: string
}

export const defaultSettings: AppSettings = {
  appName: 'Boost Browser',
  appDescription: '基于 Wails + React 的桌面应用',
  theme: 'light',
  primaryColor: '#3B82F6',
  language: 'zh-CN',
  enableNotifications: true,
  enableAutoSave: true,
  autoSaveInterval: 30,
  maxUploadSize: 10,
  sessionTimeout: 30,
  cacheEnabled: true,
  logLevel: 'info',
  maxMemoryMB: 1024,
  gcPercent: 100,
  cacheAutoCleanEnabled: false,
  cacheAutoCleanIntervalDays: 30,
  cacheLastCleanAt: '',
  cacheNextCleanAt: '',
}
