/**
 * 项目配置文件
 * 
 * 基于此脚手架创建新项目时，修改此文件即可完成定制
 */

// 项目基础信息
export const projectConfig = {
  name: 'BrowserStudio',
  shortName: 'Boost',
  description: '面向多账号隔离、代理绑定和本地环境管理的桌面浏览器工具',
  primaryColor: 'primary',
}

// 导航菜单配置
export interface NavItem {
  name: string
  path: string
  icon: string
}

export interface NavSection {
  title: string
  items: NavItem[]
}

// Navigation is ordered by daily use. Secondary tools stay available by route
// but are not flattened into a long primary list that forces scanning.
export const navigationConfig: NavSection[] = [
  {
    title: '日常',
    items: [
      { name: '环境列表', path: '/browser/list', icon: 'Monitor' },
      { name: '代理池', path: '/browser/proxy-pool', icon: 'Globe' },
      { name: '扩展管理', path: '/browser/extensions', icon: 'Puzzle' },
      { name: '窗口同步', path: '/browser/sync', icon: 'Activity' },
    ]
  },
  {
    title: '配置',
    items: [
      { name: '内核管理', path: '/browser/cores', icon: 'Cpu' },
      { name: '标签管理', path: '/browser/tags', icon: 'Tag' },
      { name: '默认书签', path: '/browser/bookmarks', icon: 'Bookmark' },
      { name: '系统设置', path: '/settings', icon: 'Settings' },
    ]
  },
  {
    title: '帮助',
    items: [
      { name: '使用教程', path: '/system/tutorial', icon: 'BookOpen' },
      { name: '接口文档', path: '/browser/launch-api', icon: 'FileText' },
      { name: '日志', path: '/browser/logs', icon: 'FileText' },
    ]
  },
]

// 功能开关
export const featuresConfig = {
  dashboard: true,
  data: true,
  settings: true,
}

// UI 配置
export const uiConfig = {
  pagination: {
    defaultPageSize: 20,
    pageSizeOptions: [10, 20, 50, 100],
  },
  dateFormat: 'YYYY-MM-DD HH:mm:ss',
  locale: 'zh-CN',
}

export default {
  project: projectConfig,
  navigation: navigationConfig,
  features: featuresConfig,
  ui: uiConfig,
}
