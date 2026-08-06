import { CheckCircle, XCircle, AlertCircle, Info, X } from 'lucide-react'
import { create } from 'zustand'

type ToastType = 'success' | 'error' | 'warning' | 'info'

interface ToastAction {
  label: string
  onClick: () => void
}

interface Toast {
  id: string
  type: ToastType
  message: string
  duration?: number
  action?: ToastAction
}

interface ToastStore {
  toasts: Toast[]
  addToast: (toast: Omit<Toast, 'id'>) => void
  removeToast: (id: string) => void
}

export const useToastStore = create<ToastStore>((set) => ({
  toasts: [],
  addToast: (toast) => {
    const id = Math.random().toString(36).substring(7)
    set((state) => ({
      toasts: [...state.toasts, { ...toast, id }],
    }))

    // 自动移除
    const duration = toast.duration ?? 3000
    if (duration > 0) {
      setTimeout(() => {
        set((state) => ({
          toasts: state.toasts.filter((t) => t.id !== id),
        }))
      }, duration)
    }
  },
  removeToast: (id) =>
    set((state) => ({
      toasts: state.toasts.filter((t) => t.id !== id),
    })),
}))

// Toast 工具函数
export const toast = {
  success: (message: string, duration?: number) =>
    useToastStore.getState().addToast({ type: 'success', message, duration }),
  error: (message: string, duration?: number) =>
    useToastStore.getState().addToast({ type: 'error', message, duration }),
  warning: (message: string, duration?: number) =>
    useToastStore.getState().addToast({ type: 'warning', message, duration }),
  info: (message: string, duration?: number) =>
    useToastStore.getState().addToast({ type: 'info', message, duration }),
  /** Warning with an action button (e.g. “不再提醒”); auto-dismiss after 8s. */
  warningWithAction: (message: string, actionLabel: string, onClick: () => void) =>
    useToastStore.getState().addToast({ type: 'warning', message, duration: 8000, action: { label: actionLabel, onClick } }),
}

const icons = {
  success: CheckCircle,
  error: XCircle,
  warning: AlertCircle,
  info: Info,
}

const styles = {
  success: 'bg-[var(--color-bg-surface)] text-[var(--color-success)] border-[var(--color-success)]/30 shadow-lg shadow-[var(--color-success)]/5',
  error: 'bg-[var(--color-bg-surface)] text-[var(--color-error)] border-[var(--color-error)]/30 shadow-lg shadow-[var(--color-error)]/5',
  warning: 'bg-[var(--color-bg-surface)] text-[var(--color-warning)] border-[var(--color-warning)]/30 shadow-lg shadow-[var(--color-warning)]/5',
  info: 'bg-[var(--color-bg-surface)] text-[var(--color-accent)] border-[var(--color-accent)]/30 shadow-lg shadow-[var(--color-accent)]/5',
}

function ToastItem({ toast: t }: { toast: Toast }) {
  const removeToast = useToastStore((state) => state.removeToast)
  const Icon = icons[t.type]

  const handleAction = () => {
    t.action?.onClick()
    removeToast(t.id)
  }

  return (
    <div
      className={`flex items-start gap-3 px-4 py-3 rounded-lg border shadow-lg animate-slide-in-right ${styles[t.type]}`}
    >
      <Icon className="w-5 h-5 flex-shrink-0 mt-0.5" />
      <p className="flex-1 text-sm font-medium leading-relaxed">{t.message}</p>
      {t.action && (
        <button
          onClick={handleAction}
          className="flex-shrink-0 rounded-md border border-current px-2.5 py-1 text-xs font-semibold hover:opacity-70 transition-opacity"
        >
          {t.action.label}
        </button>
      )}
      <button
        onClick={() => removeToast(t.id)}
        className="p-0.5 rounded hover:bg-black/10 transition-colors"
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  )
}

export function ToastContainer() {
  const toasts = useToastStore((state) => state.toasts)

  return (
    <div className="fixed top-4 right-4 z-50 flex flex-col gap-2 max-w-md">
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} />
      ))}
    </div>
  )
}
