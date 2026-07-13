import { Link, useLocation } from 'react-router-dom'
import {
  Activity,
  Bookmark,
  BookOpen,
  FileText,
  LayoutDashboard,
  ListChecks,
  Monitor,
  Settings,
  Database,
  ChevronLeft,
  ChevronRight,
  Layers,
  PieChart,
  Cpu,
  Globe,
  Tag,
  Bot,
  Puzzle,
  type LucideIcon
} from 'lucide-react'
import clsx from 'clsx'
import { useLayoutStore } from '../../store/layoutStore'
import { projectConfig, navigationConfig } from '../../config'
import { OpenWindowSyncPanel } from '../../wailsjs/go/main/App'
import { toast } from '../components'


const iconMap: Record<string, LucideIcon> = {
  LayoutDashboard,
  Settings,
  Database,
  Layers,
  PieChart,
  Monitor,
  ListChecks,
  Activity,
  FileText,
  Cpu,
  Globe,
  Bookmark,
  BookOpen,
  Tag,
  Bot,
  Puzzle,
}

function getIcon(iconName: string): LucideIcon {
  return iconMap[iconName] || LayoutDashboard
}

export function Sidebar() {
  const location = useLocation()
  const { sidebarCollapsed, toggleSidebar } = useLayoutStore()

  const handleOpenSyncPanel = async () => {
    try {
      await OpenWindowSyncPanel()
    } catch (error: any) {
      toast.error(error?.message || '打开窗口同步面板失败')
    }
  }

  return (
    <aside className={clsx(
      'bg-[var(--color-bg-surface)] flex flex-col transition-all duration-300 border-r border-[var(--color-border-default)]',
      sidebarCollapsed ? 'w-16' : 'w-60'
    )}>
      {/* Logo */}
      <div className={clsx(
        'h-14 flex items-center overflow-hidden border-b border-[var(--color-border-muted)] transition-[padding] duration-300 ease-out',
        sidebarCollapsed ? 'px-4' : 'px-5'
      )}>
        <div className="flex min-w-0 items-center gap-2">
          <div className="flex h-8 w-8 flex-none items-center justify-center rounded-full bg-[var(--color-accent)] shadow-sm transition-transform duration-300 ease-out">
            <span className="text-xs font-bold text-[var(--color-text-inverse)]">
              {projectConfig.shortName.charAt(0)}
            </span>
          </div>
          <h2
            aria-hidden={sidebarCollapsed}
            className={clsx(
              'whitespace-nowrap text-base font-semibold tracking-tight text-[var(--color-text-primary)] transition-[max-width,opacity,transform] duration-300 ease-out',
              sidebarCollapsed ? 'max-w-0 -translate-x-2 opacity-0' : 'max-w-44 translate-x-0 opacity-100'
            )}
          >
            {projectConfig.name}
          </h2>
        </div>
      </div>

      {/* Navigation */}
      <nav className="flex-1 py-4 px-3 space-y-6 overflow-y-auto">
        {navigationConfig.map((section) => (
          <div key={section.title}>
            {!sidebarCollapsed && (
              <h3 className="px-3 mb-2 text-[10px] font-semibold text-[var(--color-text-muted)] uppercase tracking-widest">
                {section.title}
              </h3>
            )}
            <div className="space-y-1">
              {section.items.map((item) => {
                const Icon = getIcon(item.icon)
                const isSyncPanelEntry = item.path === '/browser/sync'
                const isActive = isSyncPanelEntry ? false : location.pathname === item.path

                if (isSyncPanelEntry) {
                  return (
                    <button
                      key={item.path}
                      type="button"
                      onClick={() => void handleOpenSyncPanel()}
                      title={sidebarCollapsed ? item.name : undefined}
                      className={clsx(
                        'flex w-full items-center rounded-lg transition-all duration-150 text-[var(--color-text-secondary)] hover:bg-[var(--color-accent-muted)] hover:text-[var(--color-text-primary)]',
                        sidebarCollapsed
                          ? 'justify-center w-10 h-10 mx-auto'
                          : 'px-3 py-2.5 gap-3'
                      )}
                    >
                      <Icon className="w-[18px] h-[18px] flex-shrink-0" />
                      {!sidebarCollapsed && (
                        <span className="text-sm font-medium truncate">{item.name}</span>
                      )}
                    </button>
                  )
                }

                return (
                  <Link
                    key={item.path}
                    to={item.path}
                    title={sidebarCollapsed ? item.name : undefined}
                    className={clsx(
                      'flex items-center rounded-lg transition-all duration-150',
                      isActive
                        ? 'bg-[var(--color-accent)] text-[var(--color-text-inverse)] shadow-sm'
                        : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-accent-muted)] hover:text-[var(--color-text-primary)]',
                      sidebarCollapsed
                        ? 'justify-center w-10 h-10 mx-auto'
                        : 'px-3 py-2.5 gap-3'
                    )}
                  >
                    <Icon className="w-[18px] h-[18px] flex-shrink-0" />
                    {!sidebarCollapsed && (
                      <span className="text-sm font-medium truncate">{item.name}</span>
                    )}
                  </Link>
                )
              })}
            </div>
          </div>
        ))}
      </nav>

      {/* Toggle Button */}
      <div className="p-3 border-t border-[var(--color-border-muted)]">
        <button
          onClick={toggleSidebar}
          className={clsx(
            'flex items-center rounded-lg text-[var(--color-text-muted)] hover:bg-[var(--color-accent-muted)] hover:text-[var(--color-text-secondary)] transition-all duration-150',
            sidebarCollapsed 
              ? 'justify-center w-10 h-10 mx-auto' 
              : 'w-full px-3 py-2 gap-3'
          )}
          title={sidebarCollapsed ? '展开' : '收起'}
        >
          {sidebarCollapsed ? (
            <ChevronRight className="w-[18px] h-[18px]" />
          ) : (
            <>
              <ChevronLeft className="w-[18px] h-[18px]" />
              <span className="text-sm">收起侧边栏</span>
            </>
          )}
        </button>
      </div>
    </aside>
  )
}
